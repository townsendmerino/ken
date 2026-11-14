package gotypes

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The rename of this file follows gopls v0.23.0's (golang.org/x/tools/gopls: internal/golang/rename.go and
// rename_check.go), and several of its functions are ports of functions there, Copyright The Go Authors, under the
// BSD-style license reproduced in the NOTICE file at the root of this repository.
//
// A rename computed in this process (ADR-062), following gopls's rename for what is edited and which other
// declarations must be renamed with the one asked for:
//
//   - every identifier that defines or uses the object, an instantiation of it included;
//   - the old name as a word in the doc comment of each renamed declaration, and in doc links ([Name],
//     [pkg.Type.Method]) in package and declaration comments;
//   - a type and the embedded fields named after it, together;
//   - a method of an interface and every method tied to it by an assignment of a type to an interface somewhere in
//     the searched packages (tties.go), together. A concrete method tied to an interface method that way is not
//     renamed at all when the rename starts from it: the interface method is the one to rename.
//
// It works on the tables of a program loaded for searching (tload.go): which object each identifier uses, by name,
// and which each declares, by position. That is all the edits need; a file is read and parsed again only for its
// comments, and a package is type-checked again only for its ties, once, when a method is renamed. So a rename
// costs a held program a table lookup per searched package.
//
// It departs from gopls in seven places, each because gopls's edits there do not compile or are not the edits asked
// for:
//
//   - gopls visits the packages once, dependencies first, so a method found to be tied to the renamed one late is
//     not renamed in the packages already visited (golang/go#58461). Here the packages are revisited until nothing
//     new is found, and the search widens to the importers of each newly found declaration.
//   - gopls decides package by package whether a rename may take tied methods with it: by whether an interface
//     method is among the targets that package sees. A rename started at an interface method then refuses, or
//     stops short, in a package that sees only an implementation. Here it is decided once, by where the rename
//     started.
//   - gopls compares methods as objects, so a tie made through an instantiated generic type (Box[int] assigned to
//     Getter[int]) is missed: the interface's method is renamed without the type's, or the type's without a
//     refusal. Here they are compared as declarations.
//   - gopls ties a type to a constraint when a value of the type is passed to a generic function, and not when the
//     function or type is only instantiated with it (tsatisfy.go, instantiations): the constraint's method is then
//     renamed without the type's. Here an instantiation ties them.
//   - a rename that would have to rename a declaration outside the workspace (an interface method tied to
//     (*os.File).Read, say) is refused; gopls edits the workspace's calls to it.
//   - an embedded field declared through an alias (type A = B; struct{ A }) takes the alias's name; gopls renames
//     B along with A.
//   - gopls refuses to rename a method anywhere near a package with a type error. Here the rename goes ahead, and
//     the answer says that the edits inside code that does not type-check may be incomplete.
//
// gopls's own safety checks (shadowing, ambiguous selectors, name clashes) are not ported. Their place is taken by
// something stronger: the finished patch is applied in memory and what it touches is type-checked again
// (tverify.go).

// renameEdit replaces bytes [start, end) of a file with text.
type renameEdit struct {
	start, end int
	text       string
}

// filePos is a place in a file, as a symbol's declaration is given: 1-based line and byte column.
type filePos struct {
	file      string
	line, col int
}

// targetKey names a declaration across the package variants of one program.
type targetKey struct {
	pkgPath string
	pos     token.Pos
}

// renameRefusal is a rename that must not be made, with the reasons.
type renameRefusal struct{ reasons []string }

func (e *renameRefusal) Error() string { return strings.Join(e.reasons, "\n") }

// renameResult is one configuration's rename of one declaration.
type renameResult struct {
	edits   map[string][]renameEdit // by file; sorted and without duplicates
	old     map[string][]byte       // the source of each edited file
	method  bool                    // a method was renamed: which types satisfy which interfaces is in play
	targets int                     // declarations renamed
	partial string                  // why the edits may be incomplete; "" when they are not
}

// sourceCache reads, and parses for comments, each file at most once for a rename plan: its configurations mostly
// compile the same files.
type sourceCache struct {
	mu sync.Mutex
	m  map[string]*sourceFile
}

type sourceFile struct {
	read  sync.Once
	src   []byte
	err   error
	parse sync.Once
	doc   *docFile
}

// docFile is a file parsed on its own, with its comments. Positions in it are its own: only offsets are shared with
// the program.
type docFile struct {
	tf   *token.File
	file *ast.File
}

func (c *sourceCache) entry(file string) *sourceFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*sourceFile{}
	}
	e := c.m[file]
	if e == nil {
		e = &sourceFile{}
		c.m[file] = e
	}
	return e
}

// source returns a file's contents.
func (c *sourceCache) source(file string) ([]byte, error) {
	e := c.entry(file)
	e.read.Do(func() { e.src, e.err = os.ReadFile(file) })
	return e.src, e.err
}

// comments returns a file parsed with its comments, or nil when it cannot be read or parsed.
func (c *sourceCache) comments(file string) *docFile {
	e := c.entry(file)
	e.parse.Do(func() {
		src, err := c.source(file)
		if err != nil {
			return
		}
		fset := token.NewFileSet()
		f, _ := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
		if f != nil {
			e.doc = &docFile{fset.File(f.FileStart), f}
		}
	})
	return e.doc
}

func isPackageLevel(obj types.Object) bool {
	return obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope()
}

func isMethod(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	return ok && fn.Signature().Recv() != nil
}

// receiverNamed returns the named type of a method's receiver, through a pointer and aliases, or nil.
func receiverNamed(recv *types.Var) *types.Named {
	t := recv.Type()
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := types.Unalias(t).(*types.Named)
	return named
}

// renameScope returns the packages a rename of something declared in file works on: every package that compiles the
// file and every package that imports one of those, directly or (transitive) through any chain; never a package
// recompiled for another package's test (gopls: typeCheckReverseDependencies), and only the ones checked with
// function bodies. That leaves out a plain package whose test variant is in the scope and stands for it: the
// variant holds every file of it, so each identifier of those files is edited there (see needsBodies).
func (p *program) renameScope(file string, transitive bool) map[*tPackage]bool {
	out := map[*tPackage]bool{}
	for _, v := range p.variants(file, true) {
		out[v] = true
		for q := range p.rdeps(v, transitive) {
			out[q] = true
		}
	}
	for q := range out {
		if q.itv() || !q.bodies || q.uses == nil {
			delete(out, q)
		}
	}
	return out
}

// depsFirst returns pkgs ordered so that a package comes after the packages it imports.
func (p *program) depsFirst(pkgs map[*tPackage]bool) []*tPackage {
	var out []*tPackage
	seen := map[*tPackage]bool{}
	var visit func(tp *tPackage)
	visit = func(tp *tPackage) {
		if seen[tp] {
			return
		}
		seen[tp] = true
		for _, d := range tp.deps {
			if dp := p.pkgs[d]; dp != nil {
				visit(dp)
			}
		}
		if pkgs[tp] {
			out = append(out, tp)
		}
	}
	for _, tp := range p.order {
		visit(tp)
	}
	return out
}

// depsByPath returns, for each package path asked for, the package variant that pkg sees under that path: pkg
// itself, or the one among its dependencies.
func (p *program) depsByPath(pkg *tPackage, paths map[string]bool) map[string]*tPackage {
	out := map[string]*tPackage{}
	seen := map[*tPackage]bool{}
	var visit func(tp *tPackage)
	visit = func(tp *tPackage) {
		if seen[tp] || len(out) == len(paths) {
			return
		}
		seen[tp] = true
		if paths[tp.PkgPath] {
			if _, have := out[tp.PkgPath]; !have {
				out[tp.PkgPath] = tp
			}
		}
		for _, d := range tp.deps {
			if dp := p.pkgs[d]; dp != nil {
				visit(dp)
			}
		}
	}
	visit(pkg)
	return out
}

// objAt returns the object tp declares at pos, or nil. A searched package has that in its tables. An intermediate
// test variant does not (it is imported, never searched), and the declarations another package can name are
// indexed for it on first use.
func (p *program) objAt(tp *tPackage, pos token.Pos) types.Object {
	if tp == nil || tp.types == nil {
		return nil
	}
	if tp.defs != nil {
		if o := tp.defs[pos]; o != nil && o.Pos() == pos {
			return o
		}
		return nil
	}
	reach := p.reachable(tp.types)
	p.byPosMu.Lock()
	defer p.byPosMu.Unlock()
	idx, ok := p.byPos[tp]
	if !ok {
		idx = map[token.Pos]types.Object{}
		for o := range reach {
			idx[o.Pos()] = o
		}
		if p.byPos == nil {
			p.byPos = map[*tPackage]map[token.Pos]types.Object{}
		}
		p.byPos[tp] = idx
	}
	return idx[pos]
}

// declaredInWorkspace reports whether pos is in a file of the workspace: something a rename can edit.
func (p *program) declaredInWorkspace(pos token.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	tf := p.fset.File(pos)
	return tf != nil && p.inWorkspace(tf.Name())
}

// renameAt computes the rename of the declaration at a place to the name to, or says why it cannot be done.
func (p *program) renameAt(at filePos, to string, srcs *sourceCache) (*renameResult, error) {
	for range 4 {
		res, instead, err := p.renameOnce(at, to, srcs)
		if instead == nil {
			return res, err
		}
		at = *instead // an embedded field is renamed by renaming its type
	}
	return nil, errors.New("internal error: an embedded field's type led back to an embedded field")
}

func (p *program) renameOnce(at filePos, to string, srcs *sourceCache) (*renameResult, *filePos, error) {
	vs := p.variants(at.file, false)
	if len(vs) == 0 {
		return nil, nil, fmt.Errorf("no package metadata for file %s", at.file)
	}
	// The widest variant: for a name no other package can use, it is the only package that matters.
	widest := vs[len(vs)-1]
	if widest.defs == nil || !widest.bodies {
		return nil, nil, fmt.Errorf("%s is not in a package of the workspace", at.file)
	}
	pos := p.posAt(at.file, at.line, at.col)
	if !pos.IsValid() {
		return nil, nil, errNoObject
	}
	obj := widest.defs[pos]
	if obj == nil {
		return nil, nil, errNoObject
	}
	if obj.Name() == to {
		return nil, nil, fmt.Errorf("old and new names are the same: %s", to)
	}
	if obj.Pkg() == nil || obj.Pkg().Path() == "unsafe" {
		return nil, nil, fmt.Errorf("%s is built in and cannot be renamed", obj.Name())
	}
	if obj.Name() == "_" {
		return nil, nil, errors.New(`can't rename "_"`)
	}
	// Two names the language gives a meaning to, which no type error guards: a function called init runs before
	// main and cannot be called, and main in package main is where the program starts.
	if fn, ok := obj.(*types.Func); ok && fn.Signature().Recv() == nil && (isPackageLevel(fn) || fn.Name() == "init") {
		special := func(name string) bool { return name == "init" || name == "main" && fn.Pkg().Name() == "main" }
		switch {
		case special(fn.Name()):
			return nil, nil, &renameRefusal{[]string{fmt.Sprintf("%s in package %s is called by the Go runtime under that name: renamed, it would no longer run", fn.Name(), fn.Pkg().Name())}}
		case special(to):
			return nil, nil, &renameRefusal{[]string{fmt.Sprintf("a function named %s in package %s is run by the Go runtime: renaming %s to it would change what the program does", to, fn.Pkg().Name(), fn.Name())}}
		}
	}

	// An embedded field has its type's name: rename the type.
	if v, ok := obj.(*types.Var); ok && v.Embedded() {
		if tn := widest.useAtDecl[pos]; tn != nil {
			if !p.declaredInWorkspace(tn.Pos()) {
				return nil, nil, &renameRefusal{[]string{fmt.Sprintf(
					"the embedded field %s takes its name from the type %s, which is declared outside the workspace",
					v.Name(), types.ObjectString(tn, nil))}}
			}
			w := p.fset.PositionFor(tn.Pos(), false)
			return nil, &filePos{w.Filename, w.Line, w.Column}, nil
		}
	}

	// Can another package name it? (gopls: an exported object with an object path.)
	global := false
	if obj.Exported() {
		ok := true
		switch o := obj.(type) {
		case *types.TypeName:
			if _, isParam := types.Unalias(o.Type()).(*types.TypeParam); isParam {
				ok = false
			}
		case *types.Func:
			obj = o.Origin()
		case *types.Var:
			if !o.IsField() && !isPackageLevel(o) {
				ok = false // a capitalized parameter or result
			}
		}
		global = ok && p.hasPath(obj)
	}
	// A rename that starts at an interface method is taken as the intention to rename the methods tied to it, the
	// concrete ones and those of other interfaces, as far as the ties go. That holds for the whole rename: a package
	// that sees only a concrete method among the targets is still renaming on the interface's behalf. (gopls decides
	// it package by package, and so refuses, or stops short, in a package that cannot see the interface.)
	rn := &rename{p: p, from: obj.Name(), to: to, srcs: srcs}
	if fn, ok := obj.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil && types.IsInterface(recv.Type().Underlying()) {
			rn.changeMethods = true
		}
	}

	// Which objects each package renames. This is the part with consequences, so it goes package by package.
	final := map[*tPackage]*renamer{}
	targets := map[targetKey]types.Object{}
	if !global {
		r, err := rn.objects(widest, obj)
		if err != nil {
			return nil, nil, err
		}
		final[widest] = r
		for o := range r.objsToUpdate {
			targets[keyOf(originOf(o))] = o
		}
	} else {
		// A package-level function, variable or constant is named only by direct importers; a type can be embedded,
		// and a field or method travels with its type, so those are searched through every chain of imports.
		transitive := false
		switch o := obj.(type) {
		case *types.TypeName:
			transitive = isPackageLevel(o)
		case *types.Var:
			transitive = o.IsField()
		case *types.Func:
			transitive = o.Signature().Recv() != nil
		}
		work := p.renameScope(p.fset.File(obj.Pos()).Name(), transitive)
		targets[keyOf(obj)] = obj
		done := map[*tPackage]int{} // how many targets each package has been renamed for
		for {
			if isMethod(obj) {
				p.ensureTies(work) // every package's at once, on every processor
			}
			var found []types.Object
			for _, pkg := range p.depsFirst(work) {
				objects := p.resolveTargets(pkg, targets)
				if len(objects) == 0 || len(objects) == done[pkg] {
					continue // nothing of consequence to this package, or nothing new
				}
				done[pkg] = len(objects)
				r, err := rn.objects(pkg, objects...)
				if err != nil {
					return nil, nil, err
				}
				final[pkg] = r
				// What had to be renamed as a consequence is a target for every other package too.
				for o := range r.objsToUpdate {
					o = originOf(o)
					if o.Pkg() == nil || !p.hasPath(o) {
						continue
					}
					if k := keyOf(o); targets[k] == nil {
						targets[k] = o
						found = append(found, o)
					}
				}
			}
			if len(found) == 0 {
				break
			}
			// The packages already visited have not seen the new targets, and their importers may not be in the
			// search at all: widen it and go round again.
			for _, o := range found {
				for q := range p.renameScope(p.fset.File(o.Pos()).Name(), true) {
					work[q] = true
				}
			}
		}
	}

	// The edits, every package at once: what each renames is settled.
	res := &renameResult{edits: map[string][]renameEdit{}, old: map[string][]byte{}, targets: len(targets)}
	pkgs := make([]*tPackage, 0, len(final))
	for tp := range final {
		pkgs = append(pkgs, tp)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })
	var mu sync.Mutex
	var wg sync.WaitGroup
	next := make(chan *tPackage)
	for range min(runtime.GOMAXPROCS(0), len(pkgs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tp := range next {
				edits := final[tp].update()
				mu.Lock()
				for file, es := range edits {
					res.edits[file] = append(res.edits[file], es...)
				}
				mu.Unlock()
			}
		}()
	}
	for _, tp := range pkgs {
		next <- tp
	}
	close(next)
	wg.Wait()

	bad, first := 0, ""
	for _, tp := range pkgs {
		r := final[tp]
		if r.partial != "" && res.partial == "" {
			res.partial = r.partial
		}
		for o := range r.objsToUpdate {
			res.method = res.method || isMethod(o)
		}
		if tp.nerr > 0 {
			if bad++; first == "" {
				first = tp.ID
			}
		}
	}
	if bad > 0 && res.partial == "" {
		res.partial = fmt.Sprintf("%d of the %d packages searched have type errors (%s): an identifier in code that does not type-check is not renamed",
			bad, len(pkgs), first)
	}
	if err := res.finish(rn); err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}

// resolveTargets returns the target declarations as pkg sees them: the objects of the package variants pkg itself
// depends on.
func (p *program) resolveTargets(pkg *tPackage, targets map[targetKey]types.Object) []types.Object {
	paths := map[string]bool{}
	for k := range targets {
		paths[k.pkgPath] = true
	}
	deps := p.depsByPath(pkg, paths)
	var out []types.Object
	for k := range targets {
		if o := p.objAt(deps[k.pkgPath], k.pos); o != nil {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pos() < out[j].Pos() })
	return out
}

// finish sorts each file's edits, drops the repeats (a file belongs to several package variants, and an identifier
// can both use one renamed object and declare another), and reads the source they apply to. Every edit replaces the
// old name: where the file does not have it there, the file is not the one the program read.
func (res *renameResult) finish(rn *rename) error {
	for file, edits := range res.edits {
		sort.Slice(edits, func(i, j int) bool {
			if edits[i].start != edits[j].start {
				return edits[i].start < edits[j].start
			}
			return edits[i].end < edits[j].end
		})
		edits = slices.Compact(edits)
		src, err := rn.srcs.source(file)
		if err != nil {
			return err
		}
		for i, e := range edits {
			if i > 0 && e.start < edits[i-1].end {
				return fmt.Errorf("internal error: overlapping edits in %s", file)
			}
			if e.end > len(src) || string(src[e.start:e.end]) != rn.from {
				return fmt.Errorf("%s changed while the rename was being computed: run it again", file)
			}
		}
		res.edits[file], res.old[file] = edits, src
	}
	return nil
}

// applyEdits returns src with edits (sorted, not overlapping) made.
func applyEdits(src []byte, edits []renameEdit) []byte {
	var out []byte
	at := 0
	for _, e := range edits {
		out = append(out, src[at:e.start]...)
		out = append(out, e.text...)
		at = e.end
	}
	return append(out, src[at:]...)
}

// rename is one rename under one configuration.
type rename struct {
	p        *program
	from, to string
	srcs     *sourceCache
	// changeMethods: the rename started at an interface method (see renameOnce).
	changeMethods bool
}

// renamer is the part of a rename one package sees. (gopls: renamer, renameObjects.)
type renamer struct {
	*rename
	pkg          *tPackage
	objsToUpdate map[types.Object]bool
	conflicts    []string
	partial      string
}

// objects settles what pkg renames: targets, as pkg sees them, and whatever else has to be renamed with them within
// pkg.
func (rn *rename) objects(pkg *tPackage, targets ...types.Object) (*renamer, error) {
	r := &renamer{rename: rn, pkg: pkg, objsToUpdate: map[types.Object]bool{}}
	for _, obj := range targets {
		r.check(obj)
		if len(r.conflicts) > 0 {
			return nil, &renameRefusal{r.conflicts}
		}
	}
	return r, nil
}

func (r *renamer) errorf(format string, args ...any) {
	r.conflicts = append(r.conflicts, fmt.Sprintf(format, args...))
}

// where describes a declaration's place for a message: file:line, the file by its last two path elements.
func (p *program) where(pos token.Pos) string {
	if !pos.IsValid() {
		return "(built in)"
	}
	at := p.fset.PositionFor(pos, false)
	return shortPath(at.Filename) + ":" + strconv.Itoa(at.Line)
}

// check adds from to the objects to rename, and with it whatever must be renamed for the program to stay as it is.
// (gopls: renamer.check and the parts of rename_check.go that find consequences. Its conflict checks are replaced by
// the type-check of the result.)
func (r *renamer) check(from types.Object) {
	if r.objsToUpdate[from] {
		return
	}
	r.objsToUpdate[from] = true
	if !r.p.declaredInWorkspace(from.Pos()) {
		r.errorf("renaming %q to %q would also have to rename %s, declared outside the workspace at %s",
			r.from, r.to, types.ObjectString(from, nil), r.p.where(from.Pos()))
		return
	}
	switch o := from.(type) {
	case *types.TypeName:
		// A type used as an embedded field names the field: s.T must change with T.
		for _, u := range r.pkg.uses[from.Name()] {
			if u.obj == from {
				if field := r.pkg.defs[u.pos]; field != nil {
					r.check(field)
				}
			}
		}
	case *types.Var:
		if o.IsField() && o.Anonymous() {
			// An embedded field is named by its type: the type name written there must change with it. (For an
			// alias that is the alias, not the type it stands for.) Where the field is declared in this package the
			// name is read off the declaration; elsewhere, off the field's type.
			if tn, ok := r.pkg.useAtDecl[o.Pos()].(*types.TypeName); ok {
				r.check(tn)
				break
			}
			t := o.Type()
			if ptr, ok := t.(*types.Pointer); ok {
				t = ptr.Elem()
			}
			switch t := t.(type) {
			case *types.Alias:
				r.check(t.Obj())
			case *types.Named:
				r.check(t.Obj())
			}
		}
	case *types.Func:
		if o.Signature().Recv() != nil {
			r.checkMethod(o)
		}
	}
}

// checkMethod finds the methods tied to from by assignments of a type to an interface. Starting from an interface
// method they are renamed too; a concrete method tied to an interface method is refused. (gopls: checkMethod.)
func (r *renamer) checkMethod(from *types.Func) {
	ties := r.p.tiesOf(r.pkg)
	if (ties.partial || r.pkg.nerr > 0) && r.partial == "" {
		// gopls refuses here. The ties of code that type-checks are still found; one in code that does not is not,
		// and neither can it be checked afterwards.
		r.partial = fmt.Sprintf("package %s has type errors: a method tied to the renamed one only by code that does not type-check is not renamed", r.pkg.ID)
	}
	k := keyOf(from.Origin())
	iface := types.IsInterface(from.Signature().Recv().Type())
	for _, t := range ties.byName[from.Name()] {
		// If both sides have a method of this name, and one of them is from, the other is tied to it.
		var other targetKey
		var otherDesc string
		switch {
		case iface && k == t.lhs:
			other, otherDesc = t.rhs, t.rhsDesc
		case iface && k == t.rhs:
			other, otherDesc = t.lhs, t.lhsDesc
		case !iface && !t.rhsIface && k == t.rhs:
			// a concrete method, directly or through a type that embeds its receiver, and an interface's
			other, otherDesc = t.lhs, t.lhsDesc
		default:
			continue
		}
		if other == k {
			continue // an interface used as one that embeds it: the same method on both sides
		}
		if !r.changeMethods {
			r.errorf("renaming this method %q to %q would make %s no longer assignable to %s (rename %s, at %s, if you intend to change both types)",
				from.Name(), r.to, t.rhsType, t.lhsShort, otherDesc, r.p.where(other.pos))
			return
		}
		if !r.p.declaredInWorkspace(other.pos) {
			r.errorf("renaming %q to %q would also have to rename %s, declared outside the workspace at %s: %s is used as %s",
				r.from, r.to, otherDesc, r.p.where(other.pos), t.rhsType, t.lhsType)
			return
		}
		coupled := r.p.objAt(r.p.depsByPath(r.pkg, map[string]bool{other.pkgPath: true})[other.pkgPath], other.pos)
		if coupled == nil {
			if r.partial == "" {
				r.partial = fmt.Sprintf("%s is tied to the renamed method in package %s and could not be found there: it is not renamed", otherDesc, r.pkg.ID)
			}
			continue
		}
		r.check(coupled)
		if len(r.conflicts) > 0 {
			return
		}
	}
}

// containsOrigin reports whether obj is one of the objects to rename, or an instantiation of one.
func (r *renamer) containsOrigin(obj types.Object) bool {
	if r.objsToUpdate[obj] {
		return true
	}
	o := originOf(obj)
	if o == obj {
		return false
	}
	for target := range r.objsToUpdate {
		if originOf(target) == o {
			return true
		}
	}
	return false
}

// isDirective reports whether a comment is a directive: //line, or //word:word.
func isDirective(c string) bool {
	if len(c) < 3 || c[1] != '/' {
		return false
	}
	c = c[2:]
	if len(c) == 0 {
		return false
	}
	if strings.HasPrefix(c, "line ") {
		return true
	}
	colon := strings.Index(c, ":")
	if colon <= 0 || colon+1 >= len(c) {
		return false
	}
	for i := 0; i <= colon+1; i++ {
		if i == colon {
			continue
		}
		b := c[i]
		if !('a' <= b && b <= 'z' || '0' <= b && b <= '9') {
			return false
		}
	}
	return true
}

// update returns the edits that rename every identifier of the package defining or using one of the objects, the
// old name in the doc comment of each declaration, and the doc links to them. (gopls: renamer.update.)
func (r *renamer) update() map[string][]renameEdit {
	edits := map[string][]renameEdit{}
	// edit records one replacement, unless the file is not the workspace's to edit (a file the go command
	// generated for cgo).
	edit := func(file string, start int) {
		if !r.p.inWorkspace(file) {
			if r.partial == "" {
				r.partial = "an edit falls in " + file + ", which the go command generated: it is not in the patch"
			}
			return
		}
		edits[file] = append(edits[file], renameEdit{start, start + len(r.from), r.to})
	}
	// Every identifier that uses one of the objects is spelled with the old name.
	for _, u := range r.pkg.uses[r.from] {
		if r.containsOrigin(u.obj) {
			tf := r.p.fset.File(u.pos)
			edit(tf.Name(), tf.Offset(u.pos))
		}
	}
	docRE := regexp.MustCompile(`\b` + r.from + `\b`) // a valid identifier is a valid expression
	for pos, obj := range r.pkg.defs {
		if obj.Name() != r.from || !r.containsOrigin(obj) {
			continue
		}
		tf := r.p.fset.File(pos)
		file, off := tf.Name(), tf.Offset(pos)
		edit(file, off)
		// The old name in the declaration's doc comment, line by line (the parser strips carriage returns from the
		// comment text, so a position is counted from the start of each line).
		df := r.srcs.comments(file)
		if df == nil || off >= df.tf.Size() {
			continue
		}
		doc := docComment(df.file, df.tf.Pos(off))
		if doc == nil {
			continue
		}
		for _, comment := range doc.List {
			if isDirective(comment.Text) {
				continue
			}
			first := df.tf.Line(comment.Pos())
			for i, line := range strings.Split(comment.Text, "\n") {
				lineStart := comment.Pos()
				if i > 0 {
					lineStart = df.tf.LineStart(first + i)
				}
				for _, loc := range docRE.FindAllStringIndex(line, -1) {
					edit(file, df.tf.Offset(lineStart)+loc[0])
				}
			}
		}
	}
	r.updateDocLinks(edit)
	return edits
}

// docComment returns the doc comment of the declaration whose name is the identifier at pos. (gopls: docComment.)
func docComment(f *ast.File, pos token.Pos) *ast.CommentGroup {
	for _, n := range enclosing(f, pos) {
		switch d := n.(type) {
		case *ast.FuncDecl:
			return d.Doc
		case *ast.Field:
			return d.Doc
		case *ast.GenDecl:
			return d.Doc
		case *ast.TypeSpec:
			if d.Doc != nil {
				return d.Doc
			}
		case *ast.ValueSpec:
			if d.Doc != nil {
				return d.Doc
			}
		case *ast.Ident:
		default:
			return nil
		}
	}
	return nil
}

// enclosing returns the nodes of f around the identifier that starts at pos, innermost first; nil when no identifier
// starts there.
func enclosing(f *ast.File, pos token.Pos) []ast.Node {
	var stack, found []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if pos < n.Pos() || pos >= n.End() {
			return false
		}
		stack = append(stack, n)
		if id, ok := n.(*ast.Ident); ok && id.Pos() == pos {
			found = slices.Clone(stack)
			slices.Reverse(found)
			return false
		}
		return true
	})
	return found
}

// docLinkPattern matches doc links to a symbol; its one submatch is the name to replace. (gopls: docLinkPattern.)
func docLinkPattern(pkgName, recvName, objName string, isType bool) *regexp.Regexp {
	pattern := `\[\*?` // the link may have a leading star: [*bytes.Buffer]
	if pkgName != "" {
		pattern += pkgName + `\.`
	}
	if recvName != "" {
		pattern += recvName + `\.`
	}
	pattern += `(` + objName + `)`
	if isType {
		pattern += `(?:\.\w+)*` // and the links to its methods and fields
	}
	// "[text]: url" is a link definition, not a doc link
	pattern += `\](?:[^:]|$)`
	return regexp.MustCompile(pattern)
}

type docLinkRenamer struct {
	isDep                    bool // the object is another package's
	isType                   bool
	packagePath, packageName string
	recvName, objName        string
	re                       *regexp.Regexp
}

// updateDocLinks rewrites the doc links to each renamed object in the package's package and top-level declaration
// comments. (gopls: updateCommentDocLinks.) Only a file that spells a link's end is parsed for its comments.
func (r *renamer) updateDocLinks(edit func(file string, start int)) {
	var links []*docLinkRenamer
	for obj := range r.objsToUpdate {
		if !obj.Exported() || obj.Pkg() == nil {
			continue
		}
		recvName := ""
		// A doc link can name only an exported package-level object, or a method of an exported package-level type.
		if !isPackageLevel(obj) {
			fn, isFunc := obj.(*types.Func)
			if !isFunc {
				continue
			}
			recv := fn.Signature().Recv()
			if recv == nil {
				continue
			}
			named := receiverNamed(recv)
			if named == nil || types.IsInterface(named.Underlying()) {
				continue // not an interface's method either
			}
			name := named.Origin().Obj()
			if !name.Exported() || !isPackageLevel(name) {
				continue
			}
			recvName = name.Name()
		}
		pkgName := ""
		if r.pkg.types != obj.Pkg() {
			pkgName = obj.Pkg().Name()
		}
		_, isType := obj.(*types.TypeName)
		links = append(links, &docLinkRenamer{isDep: r.pkg.types != obj.Pkg(), isType: isType,
			packagePath: obj.Pkg().Path(), packageName: pkgName, recvName: recvName, objName: obj.Name(),
			re: docLinkPattern(pkgName, recvName, obj.Name(), isType)})
	}
	if len(links) == 0 {
		return
	}
	// A link ends "Name]", or for a type goes on "Name.Member]".
	closing, dotted := []byte(r.from+"]"), []byte(r.from+".")
	for _, file := range r.pkg.Files {
		src, err := r.srcs.source(file)
		if err != nil || !bytes.Contains(src, closing) && !bytes.Contains(src, dotted) {
			continue
		}
		df := r.srcs.comments(file)
		if df == nil {
			continue
		}
		for _, d := range links {
			re := d.re
			if d.isDep {
				// the file may import the package under another name
				for _, spec := range df.file.Imports {
					if path, _ := strconv.Unquote(spec.Path.Value); path == d.packagePath {
						if spec.Name == nil || spec.Name.Name == "_" || spec.Name.Name == "." {
							continue
						}
						if spec.Name.Name != d.packageName {
							re = docLinkPattern(spec.Name.Name, d.recvName, d.objName, d.isType)
						}
						break
					}
				}
			}
			update := func(doc *ast.CommentGroup) {
				if doc == nil {
					return
				}
				for _, c := range doc.List {
					for _, loc := range re.FindAllStringSubmatchIndex(c.Text, -1) {
						edit(file, df.tf.Offset(c.Pos())+loc[2])
					}
				}
			}
			update(df.file.Doc)
			for _, decl := range df.file.Decls {
				switch decl := decl.(type) {
				case *ast.GenDecl:
					update(decl.Doc)
				case *ast.FuncDecl:
					update(decl.Doc)
				}
			}
		}
	}
}

// shortPath is a file by its last two path elements.
func shortPath(file string) string {
	parts := strings.Split(filepath.ToSlash(file), "/")
	if n := len(parts); n > 2 {
		parts = parts[n-2:]
	}
	return strings.Join(parts, "/")
}
