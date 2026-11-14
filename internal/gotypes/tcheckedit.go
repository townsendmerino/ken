package gotypes

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

// Checking an edit by making it.
//
// A rename plan is checked by applying its patch in memory and comparing the program before with the program after
// (tverify.go). The same two programs answer a wider question: what would this edit, which somebody else wrote, do
// to the build? The edited files stand in for the files on disk, the packages that compile them and the packages
// that import those are type-checked again, and the comparison reports:
//
//   - every compile error that was not there before, in the edited files and in everything that depends on them.
//     That includes the errors the type checker calls soft (an unused variable, an unused import): the compiler
//     rejects those too;
//   - every identifier on a line the edit does not touch that now refers to another declaration: a name the edit
//     shadowed or captured. The code compiles and means something else;
//   - what the edit takes away from, or changes in, the exported declarations of the packages it edits: code outside
//     the repository cannot be type-checked here, and this is what it would see.
//
// Unlike a rename's patch, an edit may change anything, so there is no token-by-token map between the old and new
// source and no narrowing to the names that changed: the lines are matched by a diff (lineMap), every function body
// of every package the edit reaches is checked, and the comparison reads every identifier of those packages. That is
// what a refresh after the same edit on disk costs.
//
// What it does not see is what no type checker sees: behaviour. An edit that passes still has tests to pass.

// lineMap matches the lines of a file before an edit with its lines after: a line is either the same line on the
// other side, or part of a hunk the edit changed.
type lineMap struct {
	oldStarts, newStarts []int // the offset each line starts at
	o2n, n2o             []int // the matching line on the other side, or -1-h for a line of hunk h
	hunks                int
	lastEmpty            bool // the new source ends in a newline: its last element is not a line
}

// newLineMap diffs old against new line by line.
func newLineMap(old, new []byte) *lineMap {
	a, b := bytes.Split(old, []byte{'\n'}), bytes.Split(new, []byte{'\n'})
	m := &lineMap{oldStarts: lineStarts(a), newStarts: lineStarts(b), o2n: make([]int, len(a)), n2o: make([]int, len(b)),
		lastEmpty: len(b[len(b)-1]) == 0}
	pre := 0
	for pre < len(a) && pre < len(b) && bytes.Equal(a[pre], b[pre]) {
		m.o2n[pre], m.n2o[pre] = pre, pre
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && bytes.Equal(a[len(a)-1-suf], b[len(b)-1-suf]) {
		m.o2n[len(a)-1-suf], m.n2o[len(b)-1-suf] = len(b)-1-suf, len(a)-1-suf
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	// The middle: a longest common subsequence when it is small enough to table, else one hunk.
	match := make([]int, len(ma)) // index in mb, or -1
	for i := range match {
		match[i] = -1
	}
	if n := len(ma) * len(mb); n > 0 && n <= 4<<20 {
		w := len(mb) + 1
		tab := make([]int32, (len(ma)+1)*w)
		for i := len(ma) - 1; i >= 0; i-- {
			for j := len(mb) - 1; j >= 0; j-- {
				switch {
				case bytes.Equal(ma[i], mb[j]):
					tab[i*w+j] = tab[(i+1)*w+j+1] + 1
				case tab[(i+1)*w+j] >= tab[i*w+j+1]:
					tab[i*w+j] = tab[(i+1)*w+j]
				default:
					tab[i*w+j] = tab[i*w+j+1]
				}
			}
		}
		for i, j := 0, 0; i < len(ma) && j < len(mb); {
			switch {
			case bytes.Equal(ma[i], mb[j]):
				match[i] = j
				i, j = i+1, j+1
			case tab[(i+1)*w+j] >= tab[i*w+j+1]:
				i++
			default:
				j++
			}
		}
	}
	// Number the hunks: a run of unmatched lines on either side, between two matches.
	i, j, hunk, open := 0, 0, 0, false
	for i < len(ma) || j < len(mb) {
		if i < len(ma) && match[i] == j {
			m.o2n[pre+i], m.n2o[pre+j] = pre+j, pre+i
			i, j = i+1, j+1
			if open {
				hunk, open = hunk+1, false
			}
			continue
		}
		open = true
		if i < len(ma) && match[i] < 0 {
			m.o2n[pre+i] = -1 - hunk
			i++
		} else {
			m.n2o[pre+j] = -1 - hunk
			j++
		}
	}
	if open {
		hunk++
	}
	m.hunks = hunk
	return m
}

func lineStarts(lines [][]byte) []int {
	starts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1
	}
	return starts
}

func lineAt(starts []int, off int) int {
	return max(sort.SearchInts(starts, off+1)-1, 0)
}

// toNew carries an offset of the old source to the new: the offset there and -1, or the hunk the offset's line is in.
func (m *lineMap) toNew(off int) (int, int) {
	l := lineAt(m.oldStarts, off)
	if n := m.o2n[l]; n >= 0 {
		return m.newStarts[n] + off - m.oldStarts[l], -1
	}
	return 0, -1 - m.o2n[l]
}

// newHunk returns the hunk an offset of the new source is in, or -1 on a line the edit left alone.
func (m *lineMap) newHunk(off int) int {
	if o := m.n2o[lineAt(m.newStarts, off)]; o < 0 {
		return -1 - o
	}
	return -1
}

// changedLines returns the 1-based lines of the new source the edit wrote, and how many lines of the old it removed.
func (m *lineMap) changedLines() (lines []int, removed int) {
	for j, o := range m.n2o {
		if o < 0 && !(m.lastEmpty && j == len(m.n2o)-1) {
			lines = append(lines, j+1)
		}
	}
	for _, n := range m.o2n {
		if n < 0 {
			removed++
		}
	}
	return lines, removed
}

// editDiff is what one configuration's program says an edit changes.
type editDiff struct {
	errs      []verifyConflict // compile errors that were not there
	moved     []verifyConflict // identifiers on untouched lines that refer to another declaration
	api       []string         // exported declarations removed or changed
	fixed     int              // errors that were there and are gone
	rechecked int              // packages type-checked again
}

// checkEdit applies an edit (each file's new contents) to the program in memory and returns what it changes; nil when
// the edit touches nothing this configuration compiles. maps holds each edited file's lineMap (none for a new file).
func (p *program) checkEdit(patch map[string][]byte, maps map[string]*lineMap, env patchEnv) (*editDiff, error) {
	changed := make([]string, 0, len(patch))
	for file := range patch {
		changed = append(changed, file)
	}
	sort.Strings(changed)
	var post *program
	ok := false
	if !env.full {
		release, err := env.hold()
		if err != nil {
			return nil, err
		}
		post, ok = p.refreshWith(changed, false, &patchProgram{overlay: patch, read: env.read})
		release()
	}
	if !ok {
		// The edit changes what the go command lists (an import, a package clause, a constraint, a new file): the
		// configuration is listed again with the edit in place, and type-checked whole.
		force := map[string]bool{}
		for _, tp := range p.order {
			if tp.bodies {
				force[tp.ID] = true
			}
		}
		var err error
		if post, err = env.reload(buildOpts{overlay: patch, forceBodies: force, read: env.read}); err != nil {
			return nil, err
		}
	}
	if post == p {
		return nil, nil
	}
	defer post.discard()
	return compareEdit(p, post, patch, maps), nil
}

// compareEdit returns the ways post, pre with an edit applied, differs from pre.
func compareEdit(pre, post *program, patch map[string][]byte, maps map[string]*lineMap) *editDiff {
	d := &editDiff{}
	// at carries a place of pre to post: the place there, or the hunk of an edited file it is in.
	type spot struct {
		posKey
		hunk int // -1: an exact place
	}
	at := func(k posKey) spot {
		if m := maps[k.file]; m != nil {
			off, hunk := m.toNew(k.off)
			if hunk >= 0 {
				return spot{posKey{k.file, 0}, hunk}
			}
			return spot{posKey{k.file, off}, -1}
		}
		return spot{k, -1}
	}
	// edited reports whether a place of post is on a line the edit wrote.
	edited := func(k posKey) bool {
		if m := maps[k.file]; m != nil {
			return m.newHunk(k.off) >= 0
		}
		_, isNew := patch[k.file] // a file the edit creates has no map: all of it is new
		return isNew
	}
	postSpot := func(k posKey) spot {
		if m := maps[k.file]; m != nil {
			if h := m.newHunk(k.off); h >= 0 {
				return spot{posKey{k.file, 0}, h}
			}
		}
		return spot{k, -1}
	}

	var ids []string
	for id := range post.pkgs {
		if post.rechecked == nil || post.rechecked[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	d.rechecked = len(ids)

	// Errors. A package and its test variant report the same error at the same place, and which of the two checks a
	// function's body can differ between the programs, so the errors of all of them are compared as one set.
	type errKey struct {
		spot
		msg string
	}
	had, hadAt := map[errKey]bool{}, map[spot]bool{}
	for _, id := range ids {
		if a := pre.pkgs[id]; a != nil {
			for _, list := range [][]typeErr{a.errs, a.soft} {
				for _, e := range list {
					s := at(pre.place(e.pos))
					had[errKey{s, e.msg}], hadAt[s] = true, true
				}
			}
		}
	}
	now, nowAt := map[errKey]bool{}, map[spot]bool{}
	seen := map[verifyConflict]bool{}
	said := map[string]bool{} // the packages whose list error was reported where they are imported
	for _, id := range ids {
		b := post.pkgs[id]
		for _, list := range [][]typeErr{b.errs, b.soft} {
			for _, e := range list {
				k := post.place(e.pos)
				s := postSpot(k)
				now[errKey{s, e.msg}], nowAt[s] = true, true
				// On a line the edit left alone an error at the same place is the error that was there, whatever it
				// now says. Inside a hunk there are no places to match, so it is the same error only if it says the
				// same thing.
				if s.hunk < 0 && hadAt[s] || s.hunk >= 0 && had[errKey{s, e.msg}] {
					continue
				}
				msg := e.msg
				// An import the go command could not resolve: its reason says more than the type checker's, and
				// is said here, at the import, once.
				if rest, ok := strings.CutPrefix(msg, "could not import "); ok {
					path, _, _ := strings.Cut(rest, " ")
					if dep := post.pkgs[b.Imports[path]]; dep != nil && dep.listErr != "" {
						msg = "could not import " + path + ": " + firstLine(strings.TrimSpace(dep.listErr))
						said[dep.ID] = true
					}
				}
				c := verifyConflict{k.file, k.off, msg}
				if !seen[c] {
					seen[c] = true
					d.errs = append(d.errs, c)
				}
			}
		}
	}
	for _, id := range ids {
		b := post.pkgs[id]
		if b.listErr != "" && !said[id] && (pre.pkgs[id] == nil || pre.pkgs[id].listErr != b.listErr) {
			c := verifyConflict{msg: b.ID + ": " + strings.TrimSpace(b.listErr)}
			if !seen[c] {
				seen[c] = true
				d.errs = append(d.errs, c)
			}
		}
	}
	for k := range had {
		if k.hunk < 0 && !nowAt[k.spot] || k.hunk >= 0 && !now[k] {
			d.fixed++
		}
	}

	// Identifiers. On every line the edit left alone, each identifier refers to what it referred to.
	describe := func(prog *program, obj types.Object) string {
		if !obj.Pos().IsValid() {
			return obj.Name() + " (built in)"
		}
		return fmt.Sprintf("%s (declared at %s)", obj.Name(), prog.where(obj.Pos()))
	}
	sameDecl := func(a, b types.Object) bool {
		if a == b {
			return true
		}
		ka, kb := pre.place(a.Pos()), post.place(b.Pos())
		samePkg := (a.Pkg() == nil) == (b.Pkg() == nil) && (a.Pkg() == nil || a.Pkg().Path() == b.Pkg().Path())
		if ka.file == "" || kb.file == "" {
			return ka == kb && a.Name() == b.Name() && samePkg
		}
		if s := at(ka); s.hunk < 0 {
			return s.posKey == kb
		}
		// The declaration was on a line the edit rewrote or removed. It is the same declaration if the edit wrote
		// one of that name and kind: the line changed around it, or the declaration moved.
		return edited(kb) && a.Name() == b.Name() && samePkg && objKind(a) == objKind(b)
	}
	for _, id := range ids {
		a, b := pre.pkgs[id], post.pkgs[id]
		if a == nil || a.uses == nil || b.uses == nil {
			continue
		}
		for name, was := range a.uses {
			var after map[posKey]types.Object
			for _, u := range was {
				s := at(pre.place(u.pos))
				if s.hunk >= 0 {
					continue // on a line the edit rewrote
				}
				if after == nil {
					after = make(map[posKey]types.Object, len(b.uses[name]))
					for _, v := range b.uses[name] {
						after[post.place(v.pos)] = v.obj
					}
				}
				// An identifier that no longer refers to anything is a compile error, reported above.
				if nowObj, ok := after[s.posKey]; ok && !sameDecl(u.obj, nowObj) {
					c := verifyConflict{s.file, s.off, fmt.Sprintf("%s would refer to %s, not to %s as it does now", name, describe(post, nowObj), describe(pre, u.obj))}
					if !seen[c] {
						seen[c] = true
						d.moved = append(d.moved, c)
					}
				}
			}
		}
	}

	// The exported declarations of the packages the edit writes in.
	apiIDs := map[string]bool{}
	for file := range patch {
		for _, prog := range []*program{pre, post} {
			for _, tp := range prog.byFile[file] {
				if tp.ForTest == "" {
					apiIDs[tp.ID] = true
				}
			}
		}
	}
	for id := range apiIDs {
		a, b := pre.pkgs[id], post.pkgs[id]
		if a == nil || b == nil || a.types == nil || b.types == nil || a.types == b.types || a.Name == "main" {
			continue
		}
		for _, name := range a.types.Scope().Names() {
			if !token.IsExported(name) {
				continue
			}
			was := apiShape(a.types.Scope().Lookup(name))
			obj := b.types.Scope().Lookup(name)
			switch {
			case obj == nil:
				d.api = append(d.api, fmt.Sprintf("%s: removed: %s", a.PkgPath, was))
			case apiShape(obj) != was:
				d.api = append(d.api, fmt.Sprintf("%s: changed: %s\n    now: %s", a.PkgPath, was, apiShape(obj)))
			}
		}
	}
	sort.Strings(d.api)

	for _, list := range [][]verifyConflict{d.errs, d.moved} {
		sort.Slice(list, func(i, j int) bool {
			if list[i].file != list[j].file {
				return list[i].file < list[j].file
			}
			if list[i].off != list[j].off {
				return list[i].off < list[j].off
			}
			return list[i].msg < list[j].msg
		})
	}
	return d
}

// objKind names what kind of thing an object is, as far as telling one declaration from another needs.
func objKind(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Var:
		if o.IsField() {
			return "field"
		}
		return "var"
	case *types.Func:
		if recv := o.Signature().Recv(); recv != nil {
			t := recv.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if n, ok := t.(*types.Named); ok {
				return "method of " + n.Obj().Name()
			}
			return "method"
		}
		return "func"
	}
	return fmt.Sprintf("%T", obj)
}

// apiShape describes an exported declaration as code outside its package sees it: its type, and for a named type its
// exported fields and its exported methods.
func apiShape(obj types.Object) string {
	qual := func(p *types.Package) string {
		if p == obj.Pkg() {
			return ""
		}
		return p.Path()
	}
	tn, ok := obj.(*types.TypeName)
	if !ok || tn.IsAlias() {
		return types.ObjectString(obj, qual)
	}
	var b strings.Builder
	b.WriteString("type " + tn.Name())
	if named, ok := tn.Type().(*types.Named); ok && named.TypeParams().Len() > 0 {
		b.WriteString("[")
		for i := range named.TypeParams().Len() {
			if i > 0 {
				b.WriteString(", ")
			}
			tp := named.TypeParams().At(i)
			b.WriteString(tp.Obj().Name() + " " + types.TypeString(tp.Constraint(), qual))
		}
		b.WriteString("]")
	}
	switch u := tn.Type().Underlying().(type) {
	case *types.Struct:
		b.WriteString(" struct{")
		n := 0
		for i := range u.NumFields() {
			f := u.Field(i)
			if !f.Exported() {
				continue
			}
			if n > 0 {
				b.WriteString("; ")
			}
			n++
			if !f.Embedded() {
				b.WriteString(f.Name() + " ")
			}
			b.WriteString(types.TypeString(f.Type(), qual))
		}
		b.WriteString("}")
	default:
		b.WriteString(" " + types.TypeString(u, qual))
	}
	if _, isIface := tn.Type().Underlying().(*types.Interface); !isIface {
		ms := types.NewMethodSet(types.NewPointer(tn.Type()))
		var methods []string
		for i := range ms.Len() {
			if m := ms.At(i).Obj(); m.Exported() {
				methods = append(methods, m.Name()+strings.TrimPrefix(types.TypeString(m.Type(), qual), "func"))
			}
		}
		if len(methods) > 0 {
			b.WriteString(" with methods " + strings.Join(methods, "; "))
		}
	}
	return b.String()
}
