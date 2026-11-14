package gotypes

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"time"
)

// The searches of this file follow gopls v0.23.0's (golang.org/x/tools/gopls: internal/golang/references.go and
// implementation.go, internal/cache/xrefs; golang.org/x/tools/go/types/objectpath), Copyright The Go Authors, under
// the BSD-style license reproduced in the NOTICE file at the root of this repository.
//
// References and implementations over a type-checked program, following gopls's own search step for step, because
// the answers are held to gopls's (ADR-061). The steps, and the gopls source each follows, are named where they
// happen. Positions are token.Pos in the program's one file set; a declaration is the same Pos in every package
// variant that compiles its file.

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func originOf(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Var:
		return o.Origin()
	case *types.Func:
		return o.Origin()
	}
	return obj
}

// effectiveReceiver returns the receiver type used for method-set comparisons of obj, a method, or nil.
func effectiveReceiver(obj types.Object) types.Type {
	if fn, ok := obj.(*types.Func); ok {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			return ensurePointer(sig.Recv().Type())
		}
	}
	return nil
}

// reachable returns the objects of pkg that have a name another package can use for them: package-level objects, and
// the fields, methods, parameters and results reached from package-level declarations without entering a function
// body. It is the set for which golang.org/x/tools/go/types/objectpath finds a path, which is what gopls's
// cross-package index is keyed by: a use of anything else is not indexed, and a search for anything else stays
// inside its own package.
func (p *program) reachable(pkg *types.Package) map[types.Object]bool {
	p.reachMu.Lock()
	defer p.reachMu.Unlock()
	if p.reach == nil {
		p.reach = map[*types.Package]map[types.Object]bool{}
	}
	if m := p.reach[pkg]; m != nil {
		return m
	}
	m := map[types.Object]bool{}
	seenT := map[types.Type]bool{}
	var visit func(t types.Type)
	object := func(o types.Object) {
		m[o] = true
		visit(o.Type())
	}
	tparams := func(list *types.TypeParamList) {
		for i := range list.Len() {
			visit(list.At(i))
		}
	}
	visit = func(t types.Type) {
		if t == nil || seenT[t] {
			return
		}
		switch t := t.(type) {
		case *types.Alias:
			seenT[t] = true
			visit(t.Rhs())
		case *types.Basic, *types.Named:
			// a named type of this package is a root of its own; one of another package has no path here
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Signature:
			seenT[t] = true
			tparams(t.RecvTypeParams())
			tparams(t.TypeParams())
			visit(t.Params())
			visit(t.Results())
		case *types.Struct:
			seenT[t] = true
			for i := range t.NumFields() {
				object(t.Field(i))
			}
		case *types.Tuple:
			for i := range t.Len() {
				object(t.At(i))
			}
		case *types.Interface:
			seenT[t] = true
			for i := range t.NumMethods() {
				fn := t.Method(i)
				if fn.Pkg() != nil && fn.Pkg() != pkg {
					continue // embedded from another package
				}
				if !m[fn] {
					object(fn)
				}
			}
		case *types.TypeParam:
			tn := t.Obj()
			if tn.Pkg() != nil && tn.Pkg() != pkg {
				return
			}
			if !m[tn] {
				m[tn] = true
				visit(t.Constraint())
			}
		}
	}
	scope := pkg.Scope()
	names := scope.Names()
	for _, name := range names {
		obj := scope.Lookup(name)
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		m[tn] = true
		switch t := tn.Type().(type) {
		case *types.Alias:
			tparams(t.TypeParams())
			visit(t.Rhs())
		case *types.Named:
			tparams(t.TypeParams())
			visit(t.Underlying())
		}
	}
	for _, name := range names {
		obj := scope.Lookup(name)
		if tn, ok := obj.(*types.TypeName); !ok {
			if obj.Exported() {
				m[obj] = true
				visit(obj.Type())
			}
		} else if named, ok := types.Unalias(tn.Type()).(*types.Named); ok {
			for i := range named.NumMethods() {
				object(named.Method(i))
			}
		}
	}
	p.reach[pkg] = m
	return m
}

// hasPath reports whether obj has an object path in its package.
func (p *program) hasPath(obj types.Object) bool {
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	if pkg.Scope().Lookup(obj.Name()) == obj {
		// only exported package-level objects, and types, have one
		_, isType := obj.(*types.TypeName)
		return isType || obj.Exported()
	}
	return p.reachable(pkg)[obj]
}

// xrefKey names a symbol the way the cross-package index does: by the path of its package and its declaration.
type xrefKey struct {
	pkgPath string
	pos     token.Pos
}

// xref is one use, in a workspace package, of a symbol of another package.
type xref struct {
	in  *tPackage
	pos token.Pos
	// beyond marks a use gopls's index does not hold: the identifier resolves to a field of an instantiated generic
	// type (Box[int]{}.Item), which is a different object from the declared field and has no object path. It is the
	// same declaration all the same. It is left out of an answer only when gopls's exact lines are asked for.
	beyond bool
}

// buildXrefs indexes, for every searched package, each exported identifier that uses a symbol of a different
// package and that has an object path there. (gopls: internal/cache/xrefs.NewIndex.)
func (p *program) buildXrefs() {
	p.xrefsOnce.Do(func() {
		p.xrefs = map[xrefKey][]xref{}
		for _, tp := range p.workspacePackages() {
			if tp.types == nil {
				continue
			}
			for name, recs := range tp.uses {
				if !token.IsExported(name) {
					continue
				}
				for _, u := range recs {
					obj := u.obj
					if obj.Pkg() == nil || obj.Pkg() == tp.types {
						continue
					}
					beyond := false
					switch o := obj.(type) {
					case *types.Func:
						obj = o.Origin()
					case *types.Var:
						// gopls looks up the instantiated field as it is, finds no path, and drops the use.
						if o.Origin() != o {
							obj, beyond = o.Origin(), true
						}
					}
					if !p.hasPath(obj) {
						continue
					}
					k := xrefKey{obj.Pkg().Path(), obj.Pos()}
					p.xrefs[k] = append(p.xrefs[k], xref{tp, u.pos, beyond})
				}
			}
		}
	})
}

var errNoObject = errors.New("no identifier or object at the declaration's position")

// refHit is one identifier a references query found. through is nil when the identifier names the queried
// declaration itself; otherwise it is the other method the identifier names, which corresponds to the queried one
// through an interface (the interface method a concrete method satisfies, or the method of a type that implements
// the queried interface).
type refHit struct {
	pos     token.Pos
	through types.Object
}

// methodName names a method the way a symbol argument does: pkg.Type.Method, the package by its name. A method of the
// predeclared error interface is error.Error; one of an interface with no name is (interface).Method.
func methodName(obj types.Object) string {
	owner := "(interface)"
	if fn, ok := obj.(*types.Func); ok {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			t := types.Unalias(sig.Recv().Type())
			if ptr, ok := t.(*types.Pointer); ok {
				t = types.Unalias(ptr.Elem())
			}
			if named, ok := t.(*types.Named); ok {
				owner = named.Obj().Name()
			}
		}
	}
	if obj.Pkg() == nil {
		return owner + "." + obj.Name()
	}
	return obj.Pkg().Name() + "." + owner + "." + obj.Name()
}

// declOf finds the position of the declaring identifier at file:line:col and the narrowest package that compiles the
// file.
func (p *program) declOf(file string, line, col int) (token.Pos, *tPackage, error) {
	vs := p.variants(file, false)
	if len(vs) == 0 {
		return token.NoPos, nil, fmt.Errorf("no package metadata for file %s", file)
	}
	pos := p.posAt(file, line, col)
	if !pos.IsValid() {
		return token.NoPos, nil, errNoObject
	}
	return pos, vs[0], nil
}

// objectOf returns the object the identifier at pos declares in tp, else the one it uses.
func objectOf(tp *tPackage, pos token.Pos) types.Object {
	if o := tp.defs[pos]; o != nil {
		return o
	}
	return tp.useAtDecl[pos]
}

// references returns the positions that refer to the object declared at file:line:col, the declaration itself left
// out. (gopls: internal/golang/references.go, ordinaryReferences.)
//
// With exact set, the answer is gopls's, line for line. Without it, the answer also has the uses gopls's index
// drops though they are uses of the same declaration (xref.beyond): a superset, and the default (ADR-061).
func (p *program) references(file string, line, col int, exact bool) ([]token.Pos, error) {
	hits, err := p.referencesLabelled(file, line, col, exact)
	out := make([]token.Pos, len(hits))
	for i, h := range hits {
		out[i] = h.pos
	}
	return out, err
}

// referencesLabelled is references with, for each position, whether it names the queried declaration or a
// corresponding method.
func (p *program) referencesLabelled(file string, line, col int, exact bool) ([]refHit, error) {
	at, pkg0, err := p.declOf(file, line, col)
	if err != nil {
		return nil, err
	}
	obj := objectOf(pkg0, at)
	if obj == nil {
		return nil, errNoObject
	}
	if !obj.Pos().IsValid() {
		return nil, fmt.Errorf("references to builtin %q are not supported", obj.Name())
	}
	// found maps each position to the corresponding method it names, or to nil when it names the declaration itself
	// (which wins when a position is reached both ways).
	found := map[token.Pos]types.Object{}
	note := func(pos token.Pos, through types.Object) {
		if prev, seen := found[pos]; !seen || (prev != nil && through == nil) {
			found[pos] = through
		}
	}

	// Every package holding the declaring file, intermediate test variants included: their importers count.
	variants := p.variants(file, true)

	// The cross-package search, for an exported object that other packages can name.
	scope := map[*tPackage]bool{}
	targets := map[xrefKey]types.Object{} // the corresponding method each stands for; nil for the declaration itself
	expansions := map[*tPackage]bool{}
	if obj.Exported() && p.hasPath(obj) {
		targets[xrefKey{variants[0].PkgPath, obj.Pos()}] = nil
		ws := p.workspacePackages()
		inWS := map[*tPackage]bool{}
		for _, tp := range ws {
			inWS[tp] = true
		}
		addRdeps := func(tp *tPackage, transitive bool) {
			for q := range p.rdeps(tp, transitive) {
				if inWS[q] {
					scope[q] = true
				}
			}
		}
		// A package-level object is named only by direct importers; a field or method travels with its type.
		transitive := obj.Pkg().Scope().Lookup(obj.Name()) != obj
		for _, v := range variants {
			addRdeps(v, transitive)
		}
		// A method: every method in the workspace that corresponds to it through interface satisfaction, in
		// either direction, is a target too, and the importers of the packages declaring those types are searched.
		if recv := effectiveReceiver(obj); recv != nil {
			if key := msetOf(recv); len(key.methods) > 0 {
				for _, tp := range ws {
					res := p.msetIndexOf(tp).search(key, relSupertype|relSubtype, obj.(*types.Func))
					if len(res) == 0 {
						continue
					}
					addRdeps(tp, true)
					expansions[tp] = true
					for _, r := range res {
						k := xrefKey{r.fn.Pkg().Path(), r.fn.Pos()}
						if _, have := targets[k]; !have && r.fn.Pos() != obj.Pos() {
							targets[k] = r.fn
						}
					}
				}
			}
		}
	}

	// Inside each (searched) variant of the declaring package: uses of the object, and for a method, uses of any
	// method of the same name whose receiver implements, or is implemented by, this one's.
	for _, v := range variants {
		if v.itv() || v.uses == nil {
			continue
		}
		target := objectOf(v, at)
		if target == nil {
			continue
		}
		p.localReferences(v, map[types.Object]bool{target: true}, true, obj.Pos(), note)
	}
	// Inside each package that declares a corresponding type: uses of the targets that are its own.
	for e := range expansions {
		if e.uses == nil {
			continue
		}
		own := map[types.Object]bool{}
		for k := range targets {
			if k.pkgPath != e.PkgPath {
				continue
			}
			if o := e.defs[k.pos]; o != nil && o.Pkg() == e.types && o.Pos() == k.pos {
				own[o] = true
			}
		}
		if len(own) > 0 {
			p.localReferences(e, own, false, obj.Pos(), note)
		}
	}
	// Across packages: the index, for the packages in scope.
	if len(scope) > 0 {
		p.buildXrefs()
		for k, through := range targets {
			for _, x := range p.xrefs[k] {
				if scope[x.in] && !(exact && x.beyond) {
					note(x.pos, through)
				}
			}
		}
	}
	// Only uses are collected: the declaration is not one, so it is not in the answer unless the same identifier is
	// also a use (the T of an embedded field struct{T} is), which gopls keeps too.
	out := make([]refHit, 0, len(found))
	for pos, through := range found {
		out = append(out, refHit{pos, through})
	}
	return out, nil
}

// localReferences reports to note each identifier in tp that uses one of targets or, when correspond is set and a
// target is a method, a method of the same name with a mutually assignable receiver. A use of anything but the queried
// declaration (which is declared at queried) is reported with the method it names.
// (gopls: internal/golang/references.go, localReferences.)
func (p *program) localReferences(tp *tPackage, targets map[types.Object]bool, correspond bool, queried token.Pos, note func(token.Pos, types.Object)) {
	var methodRecvs []types.Type
	methodName := ""
	if correspond {
		for obj := range targets {
			if t := effectiveReceiver(obj); t != nil {
				methodRecvs = append(methodRecvs, t)
				methodName = obj.Name()
			}
		}
	}
	origins := map[types.Object]bool{}
	names := map[string]bool{}
	for obj := range targets {
		origins[originOf(obj)] = true
		names[obj.Name()] = true
	}
	matches := func(obj types.Object) bool {
		if targets[obj] || origins[originOf(obj)] {
			return true
		}
		if methodRecvs != nil && obj.Name() == methodName {
			if orecv := effectiveReceiver(obj); orecv != nil {
				for _, mrecv := range methodRecvs {
					if implementsTyped(orecv, mrecv) || implementsTyped(mrecv, orecv) {
						return true
					}
				}
			}
		}
		return false
	}
	for name := range names {
		for _, u := range tp.uses[name] {
			if matches(u.obj) {
				if o := originOf(u.obj); o.Pos() == queried {
					note(u.pos, nil)
				} else {
					note(u.pos, o)
				}
			}
		}
	}
}

// implementations returns the declarations of the types that implement the interface declared at file:line:col (or,
// for a concrete type, the interfaces it satisfies); for a method, the corresponding methods of those types.
// (gopls: internal/golang/implementation.go, implementationsMsets and localImplementations.)
func (p *program) implementations(file string, line, col int) ([]token.Pos, error) {
	at, pkg0, err := p.declOf(file, line, col)
	if err != nil {
		return nil, err
	}
	obj := pkg0.useAtDecl[at] // a use first, then a declaration, as gopls reads the identifier here
	if obj == nil {
		obj = pkg0.defs[at]
	}
	var queryType types.Type
	var queryMethod *types.Func
	switch o := obj.(type) {
	case *types.TypeName:
		queryType = o.Type()
	case *types.Func:
		sig, _ := o.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			return nil, fmt.Errorf("%s is a function, not a method", o.Name())
		}
		queryType, queryMethod = sig.Recv().Type(), o
	case nil:
		return nil, errNoObject
	default:
		return nil, fmt.Errorf("%s is not a type or method", o.Name())
	}
	key := msetOf(queryType)
	if len(key.methods) == 0 {
		return nil, nil // every type satisfies an interface with no methods
	}
	rel := relSupertype
	if types.IsInterface(queryType) {
		rel = relSubtype
	}
	found := map[token.Pos]bool{}

	// Inside each searched variant of the declaring package, by type: this also sees types local to a function.
	for _, v := range p.variants(file, false) {
		p.localImplementations(v, at, rel, found)
	}
	// Everywhere else, by name.
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	for _, tp := range p.order {
		if tp.itv() || tp.PkgPath == pkgPath || tp.types == nil {
			continue
		}
		for _, r := range p.msetIndexOf(tp).search(key, rel, queryMethod) {
			if queryMethod != nil {
				found[r.fn.Pos()] = true
			} else {
				found[r.tname.Pos()] = true
			}
		}
	}
	out := make([]token.Pos, 0, len(found))
	for pos := range found {
		out = append(out, pos)
	}
	return out, nil
}

func (p *program) localImplementations(tp *tPackage, at token.Pos, rel int, found map[token.Pos]bool) {
	var queryType types.Type
	var queryMethod *types.Func
	switch o := tp.defs[at].(type) {
	case *types.TypeName:
		queryType = o.Type()
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			queryType, queryMethod = sig.Recv().Type(), o
		}
	}
	if queryType == nil {
		return
	}
	queryType = ensurePointer(queryType)
	matches := func(cand types.Type) bool {
		return rel&relSupertype != 0 && implementsTyped(queryType, cand) ||
			rel&relSubtype != 0 && implementsTyped(cand, queryType)
	}
	for _, spec := range tp.typeSpecs {
		if spec.pos == at {
			continue // the query type itself
		}
		if spec.def.IsAlias() {
			continue
		}
		cand := ensurePointer(spec.def.Type())
		if !matches(cand) {
			continue
		}
		ms := types.NewMethodSet(cand)
		if ms.Len() == 0 {
			continue
		}
		if queryMethod == nil {
			found[spec.pos] = true
			continue
		}
		for i := range ms.Len() {
			m := ms.At(i).Obj()
			if m.Pos() == at {
				continue // the query method itself
			}
			if m.Id() == queryMethod.Id() {
				found[m.Pos()] = true
				break
			}
		}
	}
}
