package gotypes

import (
	"fmt"
	"go/scanner"
	"go/token"
	"go/types"
	"sort"
)

// Checking a rename by making it (ADR-062).
//
// gopls decides whether a rename is safe by rules about scopes and selectors, applied before the edit. Here the
// finished patch is applied in memory, what it touches is type-checked again, and the two programs are compared.
// The rename is safe when:
//
//   - nothing changed but identifiers and comments;
//   - there is no type error that was not there before (a name clash, a type that no longer implements an interface
//     it is assigned to, an importer left spelling the old name);
//   - every identifier refers to the declaration it referred to before (so nothing became shadowed, captured or
//     ambiguous);
//   - every declared type has the fields and methods it had, each found by selection as before: none hidden behind
//     the new name, merged with it, or made ambiguous by it. With no use of the member in sight this changes no
//     identifier and compiles, and changes what the type is. Nor does a name the patch takes away come to select
//     another method (one the renamed declaration was hiding would answer for the type from then on). It may come
//     to select another field: every use of the name was a use of the declaration that hid it;
//   - every type assertion that could succeed before still can (the one place a method rename can break interface
//     satisfaction and leave the code compiling).
//
// That is the definition of the rename being the same program with one name changed, for everything a type checker
// can see. What it cannot see is outside the guarantee: reflection, struct tags, names in strings and in assembly,
// and a type that starts to satisfy an interface it did not satisfy before.
//
// The second program is the first one refreshed (trefresh.go) with the patched files in place of the files on disk:
// only the packages that compile a patched file, and the packages that import those, are type-checked again, which
// is what an edit of those files costs a held program anyway. And the comparison reads only the names the patch
// changes. An identifier spelled with any other name resolves by declarations and members of that other name, none
// of which the patch renamed, so it can come to mean something else only downstream of an identifier that is
// spelled with a changed name and already means something else, or of a new type error; either is reported where
// it starts.

// tokenMap carries an offset in a file's old source to the same place in its new source, given that the two differ
// only in identifiers and comments. Between two places where they differ the distance is constant, so the map is
// the list of places where it changes.
type tokenMap struct {
	at, delta []int         // from old offset at[i] on, new = old + delta[i]
	changed   []identChange // the identifiers spelled differently
}

// identChange is one identifier the patch spells differently.
type identChange struct {
	off      int // in the old source
	was, now string
}

// newTokenMap returns the map from old to new, or false when they differ in more than identifiers and comments:
// the two are scanned side by side, and every token but an identifier must be the same token with the same text.
func newTokenMap(old, new []byte) (*tokenMap, bool) {
	fset := token.NewFileSet()
	fa, fb := fset.AddFile("", -1, len(old)), fset.AddFile("", -1, len(new))
	var a, b scanner.Scanner
	a.Init(fa, old, nil, 0)
	b.Init(fb, new, nil, 0)
	m := &tokenMap{}
	last := 0
	for {
		pa, ta, la := a.Scan()
		pb, tb, lb := b.Scan()
		if ta != tb {
			return nil, false
		}
		if ta == token.EOF {
			return m, true
		}
		oa, ob := fa.Offset(pa), fb.Offset(pb)
		if la != lb {
			if ta != token.IDENT {
				return nil, false
			}
			m.changed = append(m.changed, identChange{oa, la, lb})
		}
		if d := ob - oa; d != last || len(m.at) == 0 {
			m.at, m.delta = append(m.at, oa), append(m.delta, d)
			last = d
		}
	}
}

// to maps an old offset to the new one.
func (m *tokenMap) to(off int) int {
	i := sort.SearchInts(m.at, off+1) - 1 // the last change at or before off
	if i < 0 {
		return off
	}
	return off + m.delta[i]
}

// verifyConflict is one way the program after a rename is not the program before it.
type verifyConflict struct {
	file string
	off  int // in the new source
	msg  string
}

type posKey struct {
	file string
	off  int
}

// place returns the file and offset of a position of the program, or the zero key for no position.
func (p *program) place(pos token.Pos) posKey {
	if !pos.IsValid() {
		return posKey{}
	}
	tf := p.fset.File(pos)
	if tf == nil {
		return posKey{}
	}
	return posKey{tf.Name(), tf.Offset(pos)}
}

// patchFacts is what a patch changes, read off the program it applies to.
type patchFacts struct {
	names       map[string]bool   // every name an identifier is changed from or to
	oldNames    map[string]bool   // the ones changed from
	renamed     map[posKey]string // a declaration's place in the program -> its new name
	renamedPkgs map[string]bool   // the paths of the packages that declare them
	// members: a field, a method or a type is renamed, so what types have is in play. methods: a method is, so
	// interfaces are. structural: a field or a method is: see stripBodies.
	members, methods, structural bool
}

// factsOf reads a patch: which names it changes, and which of the identifiers it spells differently are
// declarations, of what.
func (p *program) factsOf(maps map[string]*tokenMap) *patchFacts {
	f := &patchFacts{names: map[string]bool{}, oldNames: map[string]bool{}, renamed: map[posKey]string{}, renamedPkgs: map[string]bool{}}
	for file, m := range maps {
		p.parseMu.Lock()
		tf := p.tfiles[file]
		p.parseMu.Unlock()
		for _, c := range m.changed {
			f.names[c.was], f.names[c.now], f.oldNames[c.was] = true, true, true
			if tf == nil || c.off > tf.Size() {
				continue
			}
			pos := tf.Pos(c.off)
			for _, tp := range p.byFile[file] {
				obj := tp.defs[pos]
				if obj == nil {
					continue
				}
				f.renamed[posKey{file, c.off}] = c.now
				if obj.Pkg() != nil {
					f.renamedPkgs[obj.Pkg().Path()] = true
				}
				switch o := obj.(type) {
				case *types.Var:
					if o.IsField() {
						f.members, f.structural = true, true
					}
				case *types.TypeName:
					f.members = true // an embedded field has its type's name
				case *types.Func:
					if o.Signature().Recv() != nil {
						f.members, f.methods, f.structural = true, true, true
					}
				}
			}
		}
	}
	return f
}

// patchEnv is what checking a patch needs from its caller.
type patchEnv struct {
	// hold takes the repository's lock for a type-check.
	hold func() (func(), error)
	// reload loads the configuration afresh with the patch in place of the files, as its buildOpts say, taking the
	// lock itself after its listing. It is called only when the program cannot be refreshed with the patch.
	reload func(buildOpts) (*program, error)
	// read reads a file the patch leaves alone; nil reads the disk.
	read func(file string) ([]byte, error)
	// full checks every function body of every package the patch reaches, whatever the patch renames. It is how
	// the narrow check is tested: the two must agree on whether a patch is safe.
	full bool
}

// checkPatch applies a patch (each file's new contents) to the program in memory and returns the ways the result
// differs from the program in more than the names the patch changes. maps holds each patched file's tokenMap.
func (p *program) checkPatch(patch map[string][]byte, maps map[string]*tokenMap, env patchEnv) ([]verifyConflict, error) {
	changed := make([]string, 0, len(patch))
	for file := range patch {
		changed = append(changed, file)
	}
	sort.Strings(changed)
	facts := p.factsOf(maps)
	pc := &patchProgram{overlay: patch, names: facts.names, narrow: !facts.structural && !env.full, read: env.read, maps: maps}
	release, err := env.hold()
	if err != nil {
		return nil, err
	}
	post, ok := p.refreshWith(changed, false, pc)
	release()
	if !ok {
		force := map[string]bool{}
		for _, tp := range p.order {
			if tp.bodies {
				force[tp.ID] = true
			}
		}
		post, err = env.reload(buildOpts{overlay: patch, forceBodies: force, bodyNames: pc.bodyNames(), leanNames: pc.names, read: env.read})
		if err != nil {
			return nil, err
		}
	}
	if post == p {
		return nil, nil // the patch touches nothing this configuration compiles
	}
	defer post.discard()
	return comparePrograms(p, post, maps, facts), nil
}

// comparePrograms returns the ways post, pre with a patch applied, differs from pre in what it means.
func comparePrograms(pre, post *program, maps map[string]*tokenMap, facts *patchFacts) []verifyConflict {
	var out []verifyConflict
	seen := map[verifyConflict]bool{}
	add := func(at posKey, format string, args ...any) {
		c := verifyConflict{at.file, at.off, fmt.Sprintf(format, args...)}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	// moved carries a place of pre to where it is in post.
	moved := func(k posKey) posKey {
		if m := maps[k.file]; m != nil {
			k.off = m.to(k.off)
		}
		return k
	}
	describe := func(prog *program, obj types.Object) string {
		if !obj.Pos().IsValid() {
			return obj.Name() + " (built in)"
		}
		return fmt.Sprintf("%s (declared at %s)", obj.Name(), prog.where(obj.Pos()))
	}
	// sameDecl reports whether an object of pre and one of post are one declaration.
	sameDecl := func(a, b types.Object) bool {
		ka, kb := pre.place(a.Pos()), post.place(b.Pos())
		if ka.file == "" || kb.file == "" {
			return ka == kb && a.Name() == b.Name() && (a.Pkg() == nil) == (b.Pkg() == nil) &&
				(a.Pkg() == nil || a.Pkg().Path() == b.Pkg().Path())
		}
		return moved(ka) == kb
	}
	names, renamed := facts.names, facts.renamed

	ids := make([]string, 0, len(post.pkgs))
	for id := range post.pkgs {
		if post.rechecked == nil || post.rechecked[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		a, b := pre.pkgs[id], post.pkgs[id]
		if a == nil {
			continue
		}
		// No type error that was not there.
		if len(b.errs) > 0 {
			had := map[posKey]bool{}
			for _, e := range a.errs {
				had[moved(pre.place(e.pos))] = true
			}
			for _, e := range b.errs {
				if at := post.place(e.pos); !had[at] {
					add(at, "would no longer type-check: %s", e.msg)
				}
			}
		}
		if a.uses == nil || b.uses == nil {
			continue
		}
		// Every identifier spelled with a changed name refers to what it referred to.
		after := map[posKey]types.Object{}
		for name := range names {
			for _, u := range b.uses[name] {
				after[post.place(u.pos)] = u.obj
			}
		}
		for name := range names {
			for _, u := range a.uses[name] {
				at := moved(pre.place(u.pos))
				now, ok := after[at]
				switch {
				case !ok:
					add(at, "this identifier would no longer refer to %s", describe(pre, u.obj))
				case !sameDecl(u.obj, now):
					add(at, "this identifier would refer to %s instead of %s", describe(post, now), describe(pre, u.obj))
				}
				delete(after, at)
			}
		}
		for at, now := range after {
			add(at, "this identifier referred to nothing and would refer to %s", describe(post, now))
		}
		// Every type has the fields and methods it had, under the names the patch changes.
		if facts.members {
			was := map[posKey]*types.TypeName{}
			for _, spec := range a.typeSpecs {
				if !spec.def.IsAlias() {
					was[moved(pre.place(spec.pos))] = spec.def
				}
			}
			pa, pb := seenPackages(pre, a, facts.renamedPkgs), seenPackages(post, b, facts.renamedPkgs)
			for _, spec := range b.typeSpecs {
				at := post.place(spec.pos)
				old := was[at]
				if old == nil || spec.def.IsAlias() {
					continue
				}
				ma, mb := membersNamed(old.Type(), names, pa), membersNamed(spec.def.Type(), names, pb)
				for _, o := range ma {
					found := false
					for _, n := range mb {
						found = found || sameDecl(o.obj, n.obj)
					}
					if !found {
						add(at, "this type would lose %s: the new name would hide it, merge with it or make it ambiguous", describe(pre, o.obj))
					}
				}
				// What a type gains matters when it is a method under a name the patch takes away: one that the
				// renamed declaration hid, or was ambiguous with, would from now on answer for the type wherever an
				// interface asks for that name, with nothing failing to compile. A field that comes out from behind
				// a renamed one changes nothing that is written (every use of the name was a use of the other), and
				// what a type gains under the new name is the rename itself, or an addition nothing yet asks for.
				for _, n := range mb {
					if !isMethod(n.obj) || !facts.oldNames[n.name] {
						continue
					}
					found := false
					for _, o := range ma {
						found = found || sameDecl(o.obj, n.obj)
					}
					if !found {
						add(at, "this type's %s would become %s, which the renamed declaration hid or made ambiguous: an interface that asks for %s would get it",
							n.name, describe(post, n.obj), n.name)
					}
				}
			}
		}
		// Every type assertion that could succeed still can: the two methods it ties keep one name.
		if facts.methods && a.bodies {
			ties := pre.tiesOf(a)
			for name := range names {
				for _, t := range ties.byName[name] {
					if !t.at.IsValid() {
						continue // the compiler holds every other tie: breaking one is a type error, found above
					}
					l, r := name, name
					if n, ok := renamed[pre.place(t.lhs.pos)]; ok {
						l = n
					}
					if n, ok := renamed[pre.place(t.rhs.pos)]; ok {
						r = n
					}
					if l != r {
						add(moved(pre.place(t.at)), "this type assertion could no longer succeed: %s would stop implementing %s (%s would be %s, %s would be %s)",
							t.rhsType, t.lhsType, t.lhsDesc, l, t.rhsDesc, r)
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].off != out[j].off {
			return out[i].off < out[j].off
		}
		return out[i].msg < out[j].msg
	})
	return out
}

// seenPackages returns the packages whose unexported members matter to a look-up from tp: tp's own, and each
// package of paths as tp sees it.
func seenPackages(p *program, tp *tPackage, paths map[string]bool) []*types.Package {
	out := []*types.Package{tp.types}
	for _, d := range p.depsByPath(tp, paths) {
		if d != tp && d.types != nil {
			out = append(out, d.types)
		}
	}
	return out
}

// member is a field or method of a type, and a name a selector reaches it under.
type member struct {
	name string
	obj  types.Object
}

// membersNamed returns the fields and methods a selector on a value of type t (or on a pointer to it) reaches under
// one of names: each as selection finds it, so a member that is ambiguous, or hidden by a shallower one, is not
// among them. An unexported name is looked up as each of pkgs would write it.
func membersNamed(t types.Type, names map[string]bool, pkgs []*types.Package) []member {
	recv := ensurePointer(t)
	var out []member
	for name := range names {
		for _, pkg := range pkgs {
			obj, _, _ := types.LookupFieldOrMethod(recv, true, pkg, name)
			if obj == nil {
				continue
			}
			obj = originOf(obj)
			dup := false
			for _, o := range out {
				dup = dup || o.obj == obj
			}
			if !dup {
				out = append(out, member{name, obj})
			}
			if token.IsExported(name) {
				break // the same from every package
			}
		}
	}
	return out
}

// lineOf returns the 1-based line of byte offset off in src.
func lineOf(src []byte, off int) int {
	line := 1
	for i := 0; i < off && i < len(src); i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}
