// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the NOTICE file at the root of this repository.

// This file is adapted from golang.org/x/tools/refactor/satisfy (find.go, at the revision gopls v0.23.0 builds
// with). A rename has to know which methods are tied together by an assignment of a concrete type to an interface,
// and gopls decides that with this finder; the rename here is held to gopls's edits (ADR-062), so it uses the same
// finder. Changes from the original:
//
//   - no dependency on x/tools: the method-set cache is a map, and the type-set terms of a type parameter are read
//     straight off its constraint (unions are expanded, intersections of several embedded unions are not reduced);
//   - each type assertion whose target type satisfies the asserted interface is recorded with its position
//     (asserts), so that a rename can check that it still would: that is the one constraint a broken rename
//     leaves compiling;
//   - a type argument is recorded as satisfying its type parameter's constraint (instantiations): the original
//     leaves instantiations out, so gopls renames a constraint's method without the method of the type a generic
//     function is called with, and the call no longer compiles;
//   - the original panics on input that does not type-check. Here a declaration that cannot be walked is skipped
//     and counted (skipped), because a configuration that does not type-check cleanly is still renamed.

package gotypes

import (
	"go/ast"
	"go/token"
	"go/types"
)

// satConstraint records that the RHS type does and must satisfy the LHS type, which is an interface.
type satConstraint struct {
	LHS, RHS types.Type
}

// satAssert is a type assertion that can succeed: where it is, and the constraint that makes it so.
type satAssert struct {
	at token.Pos
	c  satConstraint
}

// satFinder accumulates the constraints of one package: the part of the "implements" relation the package needs in
// order to compile.
type satFinder struct {
	result  map[satConstraint]bool
	asserts []satAssert // each x.(T), or case T in a type switch, where T satisfies the interface type of x
	skipped int         // declarations that could not be walked (they did not type-check)
	msets   map[types.Type]*types.MethodSet

	info *types.Info
	sig  *types.Signature
}

func (f *satFinder) methodSet(t types.Type) *types.MethodSet {
	if ms, ok := f.msets[t]; ok {
		return ms
	}
	if f.msets == nil {
		f.msets = map[types.Type]*types.MethodSet{}
	}
	ms := types.NewMethodSet(t)
	f.msets[t] = ms
	return ms
}

// find inspects one package.
func (f *satFinder) find(info *types.Info, files []*ast.File) {
	if f.result == nil {
		f.result = make(map[satConstraint]bool)
	}
	f.info = info
	for _, file := range files {
		for _, d := range file.Decls {
			f.decl(d)
		}
	}
	f.instantiations()
	f.info = nil
}

// instantiations records, for each generic function or type the package instantiates (with the type arguments
// written, or inferred), that every type argument satisfies the constraint of its type parameter: F[T Shape] called
// with a Square needs Square to have Shape's methods as surely as an assignment to a Shape does.
func (f *satFinder) instantiations() {
	for id, inst := range f.info.Instances {
		var params *types.TypeParamList
		switch obj := f.info.Uses[id].(type) {
		case *types.Func:
			params = obj.Signature().TypeParams()
		case *types.TypeName:
			switch t := obj.Type().(type) {
			case *types.Named:
				params = t.TypeParams()
			case *types.Alias:
				params = t.TypeParams()
			}
		}
		if params == nil || inst.TypeArgs == nil {
			continue
		}
		for i := 0; i < params.Len() && i < inst.TypeArgs.Len(); i++ {
			f.assign(params.At(i).Constraint(), inst.TypeArgs.At(i))
		}
	}
}

// decl walks one top-level declaration. A panic means the declaration did not type-check: what was found before it
// is kept.
func (f *satFinder) decl(d ast.Decl) {
	defer func() {
		if recover() != nil {
			f.skipped++
			f.sig = nil
		}
	}()
	switch d := d.(type) {
	case *ast.GenDecl:
		if d.Tok == token.VAR { // ignore consts
			for _, spec := range d.Specs {
				f.valueSpec(spec.(*ast.ValueSpec))
			}
		}

	case *ast.FuncDecl:
		if d.Body != nil {
			f.sig = f.info.Defs[d.Name].Type().(*types.Signature)
			f.stmt(d.Body)
			f.sig = nil
		}
	}
}

var (
	tInvalid     = types.Typ[types.Invalid]
	tUntypedBool = types.Typ[types.UntypedBool]
	tUntypedNil  = types.Typ[types.UntypedNil]
)

// exprN visits an expression in a multi-value context.
func (f *satFinder) exprN(e ast.Expr) types.Type {
	typ := f.info.Types[e].Type.(*types.Tuple)
	switch e := e.(type) {
	case *ast.ParenExpr:
		return f.exprN(e.X)

	case *ast.CallExpr:
		// x, err := f(args)
		if sig := hasUnderlyingTermOf[*types.Signature](f.expr(e.Fun)); sig != nil {
			f.call(sig, e.Args)
		}

	case *ast.IndexExpr:
		// y, ok := x[i]
		x := f.expr(e.X)
		if m := hasUnderlyingTermOf[*types.Map](x); m != nil {
			f.assign(f.expr(e.Index), m.Key())
		}

	case *ast.TypeAssertExpr:
		// y, ok := x.(T)
		f.typeAssert(f.expr(e.X), typ.At(0).Type(), e.Pos())

	case *ast.UnaryExpr: // must be receive <-
		// y, ok := <-x
		f.expr(e.X)

	default:
		panic(e)
	}
	return typ
}

func (f *satFinder) call(sig *types.Signature, args []ast.Expr) {
	if len(args) == 0 {
		return
	}

	// Ellipsis call?  e.g. f(x, y, z...)
	if _, ok := args[len(args)-1].(*ast.Ellipsis); ok {
		for i, arg := range args {
			// The final arg is a slice, and so is the final param.
			f.assign(sig.Params().At(i).Type(), f.expr(arg))
		}
		return
	}

	var argtypes []types.Type

	// Gather the effective actual parameter types.
	if tuple, ok := f.info.Types[args[0]].Type.(*types.Tuple); ok {
		// f(g()) call where g has multiple results?
		f.expr(args[0])
		// unpack the tuple
		for i := range tuple.Len() {
			argtypes = append(argtypes, tuple.At(i).Type())
		}
	} else {
		for _, arg := range args {
			argtypes = append(argtypes, f.expr(arg))
		}
	}

	// Assign the actuals to the formals.
	if !sig.Variadic() {
		for i, argtype := range argtypes {
			f.assign(sig.Params().At(i).Type(), argtype)
		}
	} else {
		// The first n-1 parameters are assigned normally.
		nnormals := sig.Params().Len() - 1
		for i, argtype := range argtypes[:nnormals] {
			f.assign(sig.Params().At(i).Type(), argtype)
		}
		// Remaining args are assigned to elements of varargs slice.
		tElem := sig.Params().At(nnormals).Type().(*types.Slice).Elem()
		for i := nnormals; i < len(argtypes); i++ {
			f.assign(tElem, argtypes[i])
		}
	}
}

// builtin visits the arguments of a builtin type with signature sig.
func (f *satFinder) builtin(obj *types.Builtin, sig *types.Signature, args []ast.Expr) {
	switch obj.Name() {
	case "make", "new":
		for i, arg := range args {
			if i == 0 && f.info.Types[arg].IsType() {
				continue // skip the type operand
			}
			f.expr(arg)
		}

	case "append":
		s := f.expr(args[0])
		if _, ok := args[len(args)-1].(*ast.Ellipsis); ok && len(args) == 2 {
			// append(x, y...)   including append([]byte, "foo"...)
			f.expr(args[1])
		} else {
			// append(x, y, z)
			if s := hasUnderlyingTermOf[*types.Slice](s); s != nil {
				for _, arg := range args[1:] {
					f.assign(s.Elem(), f.expr(arg))
				}
			}
		}

	case "delete":
		m := f.expr(args[0])
		k := f.expr(args[1])
		if m := hasUnderlyingTermOf[*types.Map](m); m != nil {
			f.assign(m.Key(), k)
		}

	default:
		// ordinary call
		f.call(sig, args)
	}
}

func (f *satFinder) extract(tuple types.Type, i int) types.Type {
	if tuple, ok := tuple.(*types.Tuple); ok && i < tuple.Len() {
		return tuple.At(i).Type()
	}
	return tInvalid
}

func (f *satFinder) valueSpec(spec *ast.ValueSpec) {
	var T types.Type
	if spec.Type != nil {
		T = f.info.Types[spec.Type].Type
	}
	switch len(spec.Values) {
	case len(spec.Names): // e.g. var x, y = f(), g()
		for _, value := range spec.Values {
			v := f.expr(value)
			if T != nil {
				f.assign(T, v)
			}
		}

	case 1: // e.g. var x, y = f()
		tuple := f.exprN(spec.Values[0])
		for i := range spec.Names {
			if T != nil {
				f.assign(T, f.extract(tuple, i))
			}
		}
	}
}

// assign records pairs of distinct types that are related by assignability, where the left-hand side is an
// interface and both sides have methods.
func (f *satFinder) assign(lhs, rhs types.Type) {
	if lhs == nil || rhs == nil {
		return
	}
	if types.Identical(lhs, rhs) {
		return
	}
	if !types.IsInterface(lhs) {
		return
	}

	if f.methodSet(lhs).Len() == 0 {
		return
	}
	if f.methodSet(rhs).Len() == 0 {
		return
	}
	// record the pair
	f.result[satConstraint{lhs, rhs}] = true
}

// typeAssert must be called for each type assertion x.(T) where x has interface type I.
//
// A type assertion is allowed to be "impossible" between two interfaces, so it is not a constraint that T is
// assignable to I; for a refactoring it is a conditional one: if T is assignable to I before, it should remain so
// after.
func (f *satFinder) typeAssert(I, T types.Type, at token.Pos) {
	if I == nil || T == nil {
		return
	}
	if types.AssignableTo(T, I) {
		f.assign(I, T)
		if c := (satConstraint{I, T}); f.result[c] {
			f.asserts = append(f.asserts, satAssert{at, c})
		}
	}
}

// compare must be called for each comparison x==y.
func (f *satFinder) compare(x, y types.Type) {
	if x == nil || y == nil {
		return
	}
	if types.AssignableTo(x, y) {
		f.assign(y, x)
	} else if types.AssignableTo(y, x) {
		f.assign(x, y)
	}
}

// expr visits a true expression (not a type or defining ident) and returns its type.
func (f *satFinder) expr(e ast.Expr) types.Type {
	tv := f.info.Types[e]
	if tv.Value != nil {
		return tv.Type // prune the descent for constants
	}

	// tv.Type may be nil for an ast.Ident.

	switch e := e.(type) {
	case *ast.BadExpr, *ast.BasicLit:
		// no-op

	case *ast.Ident:
		// (referring idents only)
		if obj, ok := f.info.Uses[e]; ok {
			return obj.Type()
		}
		if e.Name == "_" { // e.g. "for _ = range x"
			return tInvalid
		}
		panic("undefined ident: " + e.Name)

	case *ast.Ellipsis:
		if e.Elt != nil {
			f.expr(e.Elt)
		}

	case *ast.FuncLit:
		saved := f.sig
		f.sig = tv.Type.(*types.Signature)
		f.stmt(e.Body)
		f.sig = saved

	case *ast.CompositeLit:
		for _, term := range terms(derefCore(tv.Type)) {
			switch T := term.Underlying().(type) {
			case *types.Struct:
				for i, elem := range e.Elts {
					if kv, ok := elem.(*ast.KeyValueExpr); ok {
						f.assign(f.info.Uses[kv.Key.(*ast.Ident)].Type(), f.expr(kv.Value))
					} else {
						f.assign(T.Field(i).Type(), f.expr(elem))
					}
				}

			case *types.Map:
				for _, elem := range e.Elts {
					elem := elem.(*ast.KeyValueExpr)
					f.assign(T.Key(), f.expr(elem.Key))
					f.assign(T.Elem(), f.expr(elem.Value))
				}

			case *types.Array, *types.Slice:
				tElem := T.(interface{ Elem() types.Type }).Elem()
				for _, elem := range e.Elts {
					if kv, ok := elem.(*ast.KeyValueExpr); ok {
						// ignore the key
						f.assign(tElem, f.expr(kv.Value))
					} else {
						f.assign(tElem, f.expr(elem))
					}
				}
			default:
				panic("unexpected composite literal type " + tv.Type.String())
			}
		}

	case *ast.ParenExpr:
		f.expr(e.X)

	case *ast.SelectorExpr:
		if seln, ok := f.info.Selections[e]; ok {
			// If e.X is a type (e.g., e is interface{ m() }.m), don't visit it.
			if seln.Kind() != types.MethodExpr {
				f.expr(e.X)
			}
		} else {
			return f.info.Uses[e.Sel].Type() // qualified identifier
		}

	case *ast.IndexExpr:
		if satInstance(f.info, e.X) {
			// f[T] or C[T] -- generic instantiation
		} else {
			// x[i] or m[k] -- index or lookup operation
			x := f.expr(e.X)
			i := f.expr(e.Index)
			if m := hasUnderlyingTermOf[*types.Map](x); m != nil {
				f.assign(m.Key(), i)
			}
		}

	case *ast.IndexListExpr:
		// f[X, Y] -- generic instantiation

	case *ast.SliceExpr:
		f.expr(e.X)
		if e.Low != nil {
			f.expr(e.Low)
		}
		if e.High != nil {
			f.expr(e.High)
		}
		if e.Max != nil {
			f.expr(e.Max)
		}

	case *ast.TypeAssertExpr:
		x := f.expr(e.X)
		f.typeAssert(x, f.info.Types[e.Type].Type, e.Pos())

	case *ast.CallExpr:
		if tvFun := f.info.Types[e.Fun]; tvFun.IsType() {
			// conversion
			arg0 := f.expr(e.Args[0])
			f.assign(tvFun.Type, arg0)
		} else {
			// function call

			// unsafe call. Treat calls to functions in unsafe like ordinary calls,
			// except that their signature cannot be determined by their func obj.
			// Without this special handling, f.expr(e.Fun) would fail below.
			if s, ok := ast.Unparen(e.Fun).(*ast.SelectorExpr); ok {
				if obj, ok := f.info.Uses[s.Sel].(*types.Builtin); ok && obj.Pkg().Path() == "unsafe" {
					sig := f.info.Types[e.Fun].Type.(*types.Signature)
					f.call(sig, e.Args)
					return tv.Type
				}
			}

			// builtin call
			if id, ok := ast.Unparen(e.Fun).(*ast.Ident); ok {
				if obj, ok := f.info.Uses[id].(*types.Builtin); ok {
					sig := f.info.Types[id].Type.(*types.Signature)
					f.builtin(obj, sig, e.Args)
					return tv.Type
				}
			}

			// ordinary call
			if sig := hasUnderlyingTermOf[*types.Signature](f.expr(e.Fun)); sig != nil {
				f.call(sig, e.Args)
			}
		}

	case *ast.StarExpr:
		f.expr(e.X)

	case *ast.UnaryExpr:
		f.expr(e.X)

	case *ast.BinaryExpr:
		x := f.expr(e.X)
		y := f.expr(e.Y)
		if e.Op == token.EQL || e.Op == token.NEQ {
			f.compare(x, y)
		}

	case *ast.KeyValueExpr:
		f.expr(e.Key)
		f.expr(e.Value)

	case *ast.ArrayType,
		*ast.StructType,
		*ast.FuncType,
		*ast.InterfaceType,
		*ast.MapType,
		*ast.ChanType:
		panic(e)
	}

	if tv.Type == nil {
		panic("no type for expression")
	}

	return tv.Type
}

func (f *satFinder) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.BadStmt,
		*ast.EmptyStmt,
		*ast.BranchStmt:
		// no-op

	case *ast.DeclStmt:
		d := s.Decl.(*ast.GenDecl)
		if d.Tok == token.VAR { // ignore consts
			for _, spec := range d.Specs {
				f.valueSpec(spec.(*ast.ValueSpec))
			}
		}

	case *ast.LabeledStmt:
		f.stmt(s.Stmt)

	case *ast.ExprStmt:
		f.expr(s.X)

	case *ast.SendStmt:
		ch := f.expr(s.Chan)
		val := f.expr(s.Value)
		if c := hasUnderlyingTermOf[*types.Chan](ch); c != nil {
			f.assign(c.Elem(), val)
		}

	case *ast.IncDecStmt:
		f.expr(s.X)

	case *ast.AssignStmt:
		switch s.Tok {
		case token.ASSIGN, token.DEFINE:
			// y := x   or   y = x
			var rhsTuple types.Type
			if len(s.Lhs) != len(s.Rhs) {
				rhsTuple = f.exprN(s.Rhs[0])
			}
			for i := range s.Lhs {
				var lhs, rhs types.Type
				if rhsTuple == nil {
					rhs = f.expr(s.Rhs[i]) // 1:1 assignment
				} else {
					rhs = f.extract(rhsTuple, i) // n:1 assignment
				}

				if id, ok := s.Lhs[i].(*ast.Ident); ok {
					if id.Name != "_" {
						if obj, ok := f.info.Defs[id]; ok && obj != nil {
							lhs = obj.Type() // definition
						}
					}
				}
				if lhs == nil {
					lhs = f.expr(s.Lhs[i]) // assignment
				}
				f.assign(lhs, rhs)
			}

		default:
			// y op= x
			f.expr(s.Lhs[0])
			f.expr(s.Rhs[0])
		}

	case *ast.GoStmt:
		f.expr(s.Call)

	case *ast.DeferStmt:
		f.expr(s.Call)

	case *ast.ReturnStmt:
		formals := f.sig.Results()
		switch len(s.Results) {
		case formals.Len(): // 1:1
			for i, result := range s.Results {
				f.assign(formals.At(i).Type(), f.expr(result))
			}

		case 1: // n:1
			tuple := f.exprN(s.Results[0])
			for i := 0; i < formals.Len(); i++ {
				f.assign(formals.At(i).Type(), f.extract(tuple, i))
			}
		}

	case *ast.SelectStmt:
		f.stmt(s.Body)

	case *ast.BlockStmt:
		for _, s := range s.List {
			f.stmt(s)
		}

	case *ast.IfStmt:
		if s.Init != nil {
			f.stmt(s.Init)
		}
		f.expr(s.Cond)
		f.stmt(s.Body)
		if s.Else != nil {
			f.stmt(s.Else)
		}

	case *ast.SwitchStmt:
		if s.Init != nil {
			f.stmt(s.Init)
		}
		var tag types.Type = tUntypedBool
		if s.Tag != nil {
			tag = f.expr(s.Tag)
		}
		for _, cc := range s.Body.List {
			cc := cc.(*ast.CaseClause)
			for _, cond := range cc.List {
				f.compare(tag, f.info.Types[cond].Type)
			}
			for _, s := range cc.Body {
				f.stmt(s)
			}
		}

	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			f.stmt(s.Init)
		}
		var I types.Type
		switch ass := s.Assign.(type) {
		case *ast.ExprStmt: // x.(type)
			I = f.expr(ast.Unparen(ass.X).(*ast.TypeAssertExpr).X)
		case *ast.AssignStmt: // y := x.(type)
			I = f.expr(ast.Unparen(ass.Rhs[0]).(*ast.TypeAssertExpr).X)
		}
		for _, cc := range s.Body.List {
			cc := cc.(*ast.CaseClause)
			for _, cond := range cc.List {
				tCase := f.info.Types[cond].Type
				if tCase != tUntypedNil {
					f.typeAssert(I, tCase, cond.Pos())
				}
			}
			for _, s := range cc.Body {
				f.stmt(s)
			}
		}

	case *ast.CommClause:
		if s.Comm != nil {
			f.stmt(s.Comm)
		}
		for _, s := range s.Body {
			f.stmt(s)
		}

	case *ast.ForStmt:
		if s.Init != nil {
			f.stmt(s.Init)
		}
		if s.Cond != nil {
			f.expr(s.Cond)
		}
		if s.Post != nil {
			f.stmt(s.Post)
		}
		f.stmt(s.Body)

	case *ast.RangeStmt:
		x := f.expr(s.X)
		// No conversions are involved when Tok==DEFINE.
		if s.Tok == token.ASSIGN {
			if s.Key != nil {
				k := f.expr(s.Key)
				// Keys of array, *array, slice, string aren't interesting
				// since the RHS key type is just an int.
				for _, term := range terms(x) {
					switch term := term.Underlying().(type) {
					case *types.Chan:
						f.assign(k, term.Elem())
					case *types.Map:
						f.assign(k, term.Key())
					}
				}
			}
			if s.Value != nil {
				val := f.expr(s.Value)
				// Values of type strings aren't interesting because
				// the RHS value type is just a rune.
				for _, term := range terms(x) {
					switch term := term.Underlying().(type) {
					case *types.Pointer:
						for _, term := range terms(term.Elem()) {
							if array, ok := term.Underlying().(*types.Array); ok {
								f.assign(val, array.Elem())
							}
						}
					case *types.Array:
						f.assign(val, term.Elem())
					case *types.Map:
						f.assign(val, term.Elem())
					case *types.Slice:
						f.assign(val, term.Elem())
					}
				}
			}
		}
		f.stmt(s.Body)

	default:
		panic(s)
	}
}

func satInstance(info *types.Info, expr ast.Expr) bool {
	var id *ast.Ident
	switch x := expr.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return false
	}
	_, ok := info.Instances[id]
	return ok
}

// hasUnderlyingTermOf reports whether terms(t) contains a term whose underlying type has top-level type constructor
// T (e.g. *types.Map for a map). If so, it returns an arbitrary one. Otherwise it returns the zero value (nil).
func hasUnderlyingTermOf[T types.Type](t types.Type) T {
	for _, term := range terms(t) {
		if under, ok := term.Underlying().(T); ok {
			return under
		}
	}
	return *new(T) // e.g. (*types.Map)(nil)
}

// terms returns the types in the type set of t: t itself for anything but an interface or a type parameter, the
// union terms of the constraint for those, and nothing for an interface without type terms.
func terms(t types.Type) []types.Type {
	if t == nil {
		return nil
	}
	var out []types.Type
	seen := map[types.Type]bool{}
	var walk func(t types.Type)
	walk = func(t types.Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		switch u := types.Unalias(t).(type) {
		case *types.TypeParam:
			walk(u.Constraint())
			return
		case *types.Union:
			for i := range u.Len() {
				walk(u.Term(i).Type())
			}
			return
		}
		if iface, ok := t.Underlying().(*types.Interface); ok {
			for i := range iface.NumEmbeddeds() {
				walk(iface.EmbeddedType(i))
			}
			return
		}
		out = append(out, t)
	}
	walk(t)
	return out
}

// derefCore returns the type pointed to by t when every type in t's type set is the same pointer type, else t.
func derefCore(t types.Type) types.Type {
	ts := terms(t)
	if len(ts) == 0 {
		return t
	}
	ptr, ok := ts[0].Underlying().(*types.Pointer)
	if !ok {
		return t
	}
	for _, other := range ts[1:] {
		if !types.Identical(other.Underlying(), ptr) {
			return t
		}
	}
	return ptr.Elem()
}
