package gotypes

import (
	"fmt"
	"go/types"
	"reflect"
	"strconv"
	"strings"
	"text/scanner"
)

// The functions of this file are ports of gopls v0.23.0's (golang.org/x/tools/gopls: internal/util/fingerprint,
// internal/cache/methodsets, internal/golang/implementation.go), Copyright The Go Authors, under the BSD-style
// license reproduced in the NOTICE file at the root of this repository.
//
// Which methods "correspond" through interface satisfaction is decided twice in gopls, and both ways are followed
// here because the answers are held to gopls's (ADR-061):
//
//   - across packages, by name: each package-level named type's method set is summarized as method IDs and a
//     fingerprint of each signature, a string in which a named type is its package path and name. Two method sets
//     from different packages (or different variants of one) are compared as strings. An anonymous non-empty
//     interface in a signature, or a type parameter, makes a fingerprint "tricky": it is then compared by
//     unification, in which any two non-empty interface literals match and a type parameter matches anything
//     consistently. This is the rule for "which types in the workspace implement this", and it is approximate by
//     design. (gopls: internal/cache/methodsets, internal/util/fingerprint.)
//   - inside one type-checked package, by type: implementsTyped and unifyTypes below, on go/types values.
//     (gopls: internal/golang/implementation.go.)

// ensurePointer wraps a named non-interface type in a pointer, so that its full method set is considered.
func ensurePointer(t types.Type) types.Type {
	if _, ok := types.Unalias(t).(*types.Named); ok && !types.IsInterface(t) {
		return types.NewPointer(t)
	}
	return t
}

// fingerprintOf encodes t (a method signature, without its receiver) as a string such that, for types that are not
// tricky, equal strings mean identical types.
func fingerprintOf(t types.Type) (string, bool) {
	var buf strings.Builder
	tricky := false
	var print func(t types.Type)
	print = func(t types.Type) {
		switch t := t.(type) {
		case *types.Alias:
			print(types.Unalias(t))
		case *types.Named:
			targs := t.TypeArgs()
			if targs != nil {
				buf.WriteString("(inst ")
			}
			tname := t.Obj()
			if tname.Pkg() != nil {
				fmt.Fprintf(&buf, "(qual %q %s)", tname.Pkg().Path(), tname.Name())
			} else {
				buf.WriteString(tname.Name()) // error, comparable
			}
			if targs != nil {
				for i := range targs.Len() {
					buf.WriteByte(' ')
					print(targs.At(i))
				}
				buf.WriteString(")")
			}
		case *types.Array:
			fmt.Fprintf(&buf, "(array %d ", t.Len())
			print(t.Elem())
			buf.WriteByte(')')
		case *types.Slice:
			buf.WriteString("(slice ")
			print(t.Elem())
			buf.WriteByte(')')
		case *types.Pointer:
			buf.WriteString("(ptr ")
			print(t.Elem())
			buf.WriteByte(')')
		case *types.Map:
			buf.WriteString("(map ")
			print(t.Key())
			buf.WriteByte(' ')
			print(t.Elem())
			buf.WriteByte(')')
		case *types.Chan:
			fmt.Fprintf(&buf, "(chan %d ", t.Dir())
			print(t.Elem())
			buf.WriteByte(')')
		case *types.Tuple:
			buf.WriteString("(tuple")
			for i := range t.Len() {
				buf.WriteByte(' ')
				print(t.At(i).Type())
			}
			buf.WriteByte(')')
		case *types.Basic:
			switch t.Kind() {
			case types.Byte:
				buf.WriteString("byte")
			case types.Rune:
				buf.WriteString("rune")
			case types.UnsafePointer:
				buf.WriteString(`(qual "unsafe" Pointer)`)
			default:
				buf.WriteString(t.String()) // an invalid type prints as "invalid type"; it then matches only itself
			}
		case *types.Signature:
			buf.WriteString("(func ")
			print(t.Params())
			if t.Variadic() {
				buf.WriteString(" v")
			}
			buf.WriteByte(' ')
			print(t.Results())
			buf.WriteByte(')')
		case *types.Struct:
			buf.WriteString("(struct")
			for i := range t.NumFields() {
				f := t.Field(i)
				name := f.Name()
				if !f.Exported() && f.Pkg() != nil {
					name = fmt.Sprintf("(qual %q %s)", f.Pkg().Path(), name)
				}
				fmt.Fprintf(&buf, " %s %t %q ", name, f.Embedded(), t.Tag(i))
				print(f.Type())
			}
			buf.WriteByte(')')
		case *types.Interface:
			if t.NumMethods() == 0 {
				buf.WriteString("any")
			} else {
				tricky = true
				buf.WriteString("(interface)")
			}
		case *types.TypeParam:
			tricky = true
			fmt.Fprintf(&buf, "(%s %d)", symTypeparam, t.Index())
		default: // a union, or anything new: a string that matches only itself
			fmt.Fprintf(&buf, "(other %q)", t.String())
		}
	}
	print(t)
	return buf.String(), tricky
}

const symTypeparam = "typeparam"

// An S-expression: string | int | sym | *cons | nil.
type (
	sexpr any
	sym   string
	cons  struct{ car, cdr sexpr }
)

func parseFingerprint(fp string) (tree sexpr, ok bool) {
	defer func() {
		if recover() != nil {
			tree, ok = nil, false
		}
	}()
	var scan scanner.Scanner
	scan.Error = func(*scanner.Scanner, string) { panic("scan") }
	scan.Init(strings.NewReader(fp))
	var tok rune
	next := func() { tok = scan.Scan() }
	next()
	var parse func() sexpr
	parse = func() sexpr {
		if tok == '(' {
			next()
			var head sexpr
			tail := &head
			for tok != ')' {
				if tok == scanner.EOF {
					panic("eof")
				}
				cell := &cons{car: parse()}
				*tail = cell
				tail = &cell.cdr
			}
			next()
			return head
		}
		s := scan.TokenText()
		switch tok {
		case scanner.Ident:
			next()
			return sym(s)
		case scanner.Int:
			next()
			i, err := strconv.Atoi(s)
			if err != nil {
				panic(err)
			}
			return i
		case scanner.String:
			next()
			u, err := strconv.Unquote(s)
			if err != nil {
				panic(err)
			}
			return u
		}
		panic("token")
	}
	return parse(), true
}

func typeParamIndex(x sexpr) int {
	if c, ok := x.(*cons); ok {
		if s, ok := c.car.(sym); ok && s == symTypeparam {
			if d, ok := c.cdr.(*cons); ok {
				if i, ok := d.car.(int); ok {
					return i
				}
			}
		}
	}
	return -1
}

// unifySexpr reports whether two fingerprint trees match when each side's type parameters may be bound, each
// consistently, to anything.
func unifySexpr(x, y sexpr) bool {
	var maxTP func(x sexpr) int
	maxTP = func(x sexpr) int {
		if i := typeParamIndex(x); i >= 0 {
			return i
		}
		if c, ok := x.(*cons); ok {
			return max(maxTP(c.car), maxTP(c.cdr))
		}
		return -1
	}
	xb := make([]*sexpr, maxTP(x)+1)
	for i := range xb {
		xb[i] = new(sexpr)
	}
	yb := make([]*sexpr, maxTP(y)+1)
	for i := range yb {
		yb[i] = new(sexpr)
	}
	bind := func(b *sexpr, s sexpr, bindings []*sexpr) bool {
		var occurs func(s sexpr) bool
		occurs = func(s sexpr) bool {
			if j := typeParamIndex(s); j >= 0 {
				return b == bindings[j]
			}
			if c, ok := s.(*cons); ok {
				return occurs(c.car) || occurs(c.cdr)
			}
			return false
		}
		if occurs(s) {
			return false
		}
		*b = s
		return true
	}
	var uni func(x, y sexpr) bool
	uni = func(x, y sexpr) bool {
		var bx, by *sexpr
		ix := typeParamIndex(x)
		if ix >= 0 {
			bx = xb[ix]
		}
		iy := typeParamIndex(y)
		if iy >= 0 {
			by = yb[iy]
		}
		if bx != nil || by != nil {
			if bx != nil && by != nil && *bx == nil && *by == nil {
				xb[ix] = yb[iy]
				return true
			}
			if bx != nil && *bx != nil {
				x = *bx
			}
			if by != nil && *by != nil {
				y = *by
			}
			if bx != nil && *bx == nil {
				return bind(bx, y, yb)
			}
			if by != nil && *by == nil {
				return bind(by, x, xb)
			}
			return uni(x, y)
		}
		if reflect.TypeOf(x) != reflect.TypeOf(y) {
			return false
		}
		switch x := x.(type) {
		case nil, string, int, sym:
			return x == y
		case *cons:
			y := y.(*cons)
			if !uni(x.car, y.car) {
				return false
			}
			if x.cdr == nil {
				return y.cdr == nil
			}
			if y.cdr == nil {
				return false
			}
			return uni(x.cdr, y.cdr)
		}
		return false
	}
	return uni(x, y)
}

// msetMethod is one method of a summarized method set.
type msetMethod struct {
	id     string // types.Func.Id: the name, package-qualified when unexported
	fp     string
	tricky bool
	fn     *types.Func // nil in a search key
}

// mset is the summary of one type's method set.
type mset struct {
	tname       *types.TypeName // nil in a search key
	isInterface bool
	methods     []msetMethod
}

// msetOf summarizes the method set of t (of *t for a named non-interface type). Generic methods are left out: they
// do not take part in interface satisfaction.
func msetOf(t types.Type) *mset {
	ms := types.NewMethodSet(ensurePointer(t))
	out := &mset{isInterface: types.IsInterface(t)}
	for i := range ms.Len() {
		m, ok := ms.At(i).Obj().(*types.Func)
		if !ok {
			continue
		}
		sig, _ := m.Type().(*types.Signature)
		if sig == nil || sig.TypeParams().Len() > 0 {
			continue
		}
		fp, tricky := fingerprintOf(sig)
		out.methods = append(out.methods, msetMethod{id: m.Id(), fp: fp, tricky: tricky, fn: m})
	}
	return out
}

// implementsByName reports whether method set x implements interface method set y, by ID and fingerprint.
func implementsByName(x, y *mset) bool {
	if !y.isInterface {
		return false
	}
	for i := range y.methods {
		my := &y.methods[i]
		found := false
		for j := range x.methods {
			mx := &x.methods[j]
			if mx.id != my.id {
				continue
			}
			found = true
			if !mx.tricky && !my.tricky {
				if mx.fp != my.fp {
					return false
				}
			} else {
				tx, okx := parseFingerprint(mx.fp)
				ty, oky := parseFingerprint(my.fp)
				if !okx || !oky || !unifySexpr(tx, ty) {
					return false
				}
			}
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// msetIndex is the method sets of one package's package-level named types that have methods. Aliases are left out.
type msetIndex struct {
	pkg  *tPackage
	sets []*mset
}

func (p *program) msetIndexOf(tp *tPackage) *msetIndex {
	p.msetsMu.Lock()
	defer p.msetsMu.Unlock()
	if p.msets == nil {
		p.msets = map[*tPackage]*msetIndex{}
	}
	if ix := p.msets[tp]; ix != nil {
		return ix
	}
	ix := &msetIndex{pkg: tp}
	if tp.types != nil {
		scope := tp.types.Scope()
		for _, name := range scope.Names() {
			if tn, ok := scope.Lookup(name).(*types.TypeName); ok && !tn.IsAlias() {
				if ms := msetOf(tn.Type()); len(ms.methods) > 0 {
					ms.tname = tn
					ix.sets = append(ix.sets, ms)
				}
			}
		}
	}
	p.msets[tp] = ix
	return ix
}

// A relation between a candidate type and the query type.
const (
	relSupertype = 1 // the query type implements the candidate
	relSubtype   = 2 // the candidate implements the query type
)

// msetResult is one type (or, with a method asked for, that type's method of the same ID) found by a search.
type msetResult struct {
	tname *types.TypeName
	fn    *types.Func // the corresponding method, when one was asked for
}

// search returns the indexed types related to key in a wanted direction. With method set, each result carries the
// candidate's method of that ID; error.Error, which has no package, is not reported.
func (ix *msetIndex) search(key *mset, want int, method *types.Func) []msetResult {
	var out []msetResult
	for _, cand := range ix.sets {
		got := 0
		if want&relSubtype != 0 && implementsByName(cand, key) {
			got |= relSubtype
		}
		if want&relSupertype != 0 && implementsByName(key, cand) {
			got |= relSupertype
		}
		if got == 0 {
			continue
		}
		if method == nil {
			out = append(out, msetResult{tname: cand.tname})
			continue
		}
		for i := range cand.methods {
			if m := &cand.methods[i]; m.id == method.Id() {
				if m.fn.Pkg() != nil {
					out = append(out, msetResult{tname: cand.tname, fn: m.fn.Origin()})
				}
				break
			}
		}
	}
	return out
}

// implementsTyped reports whether x implements y within one type-checked package: every method of interface y is a
// method of x with a signature that unifies with it. For generic types it reports whether some instantiation could.
func implementsTyped(x, y types.Type) bool {
	if !types.IsInterface(y) {
		return false
	}
	ymset := types.NewMethodSet(y)
	for i := range ymset.Len() {
		ym, ok := ymset.At(i).Obj().(*types.Func)
		if !ok {
			continue
		}
		ysig, _ := ym.Type().(*types.Signature)
		if ysig == nil || ysig.TypeParams().Len() > 0 {
			continue
		}
		xobj, _, _ := types.LookupFieldOrMethod(x, false, ym.Pkg(), ym.Name())
		xm, ok := xobj.(*types.Func)
		if !ok {
			return false
		}
		xsig, _ := xm.Type().(*types.Signature)
		if xsig == nil || xsig.TypeParams().Len() > 0 {
			return false
		}
		if !unifyTypes(xsig, ysig) {
			return false
		}
	}
	return true
}

// unifyTypes reports whether x and y match, binding type parameters consistently. Aliases are seen through. Two
// interface types match when they have the same number of methods, as in gopls.
func unifyTypes(x, y types.Type) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false // a type this does not know (a union): no match
		}
	}()
	bindings := map[*types.TypeParam]*types.Type{}
	bindingFor := func(tp *types.TypeParam) *types.Type {
		if tp == nil {
			return nil
		}
		b := bindings[tp]
		if b == nil {
			b = new(types.Type)
			bindings[tp] = b
		}
		return b
	}
	var free func(t types.Type, seen map[types.Type]bool, yield func(*types.TypeParam) bool) bool
	free = func(t types.Type, seen map[types.Type]bool, yield func(*types.TypeParam) bool) bool {
		t = types.Unalias(t)
		if seen[t] {
			return true
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.TypeParam:
			return yield(t)
		case *types.Array:
			return free(t.Elem(), seen, yield)
		case *types.Slice:
			return free(t.Elem(), seen, yield)
		case *types.Pointer:
			return free(t.Elem(), seen, yield)
		case *types.Chan:
			return free(t.Elem(), seen, yield)
		case *types.Map:
			return free(t.Key(), seen, yield) && free(t.Elem(), seen, yield)
		case *types.Named:
			for i := range t.TypeArgs().Len() {
				if !free(t.TypeArgs().At(i), seen, yield) {
					return false
				}
			}
		case *types.Signature:
			return free(t.Params(), seen, yield) && free(t.Results(), seen, yield)
		case *types.Struct:
			for i := range t.NumFields() {
				if !free(t.Field(i).Type(), seen, yield) {
					return false
				}
			}
		case *types.Tuple:
			for i := range t.Len() {
				if !free(t.At(i).Type(), seen, yield) {
					return false
				}
			}
		}
		return true
	}
	bind := func(b *types.Type, t types.Type) bool {
		occurs := false
		free(t, map[types.Type]bool{}, func(tp *types.TypeParam) bool {
			if b == bindings[tp] {
				occurs = true
				return false
			}
			return true
		})
		if occurs {
			return false
		}
		*b = t
		return true
	}
	depth := 0
	var uni func(x, y types.Type) bool
	uni = func(x, y types.Type) bool {
		depth++
		defer func() { depth-- }()
		if depth > 100 {
			panic("unify: too deep")
		}
		x, y = types.Unalias(x), types.Unalias(y)
		tpx, _ := x.(*types.TypeParam)
		tpy, _ := y.(*types.TypeParam)
		if tpx != nil || tpy != nil {
			if tpx == tpy {
				return true
			}
			bx, by := bindingFor(tpx), bindingFor(tpy)
			if bx != nil && by != nil && *bx == nil && *by == nil {
				bindings[tpx] = by
				return true
			}
			if bx != nil && *bx != nil {
				x = *bx
			}
			if by != nil && *by != nil {
				y = *by
			}
			if bx != nil && *bx == nil {
				return bind(bx, y)
			}
			if by != nil && *by == nil {
				return bind(by, x)
			}
			return uni(x, y)
		}
		if reflect.TypeOf(x) != reflect.TypeOf(y) {
			return false
		}
		switch x := x.(type) {
		case *types.Array:
			y := y.(*types.Array)
			return x.Len() == y.Len() && uni(x.Elem(), y.Elem())
		case *types.Basic:
			return x.Kind() == y.(*types.Basic).Kind()
		case *types.Chan:
			y := y.(*types.Chan)
			return x.Dir() == y.Dir() && uni(x.Elem(), y.Elem())
		case *types.Interface:
			return x.NumMethods() == y.(*types.Interface).NumMethods()
		case *types.Map:
			y := y.(*types.Map)
			return uni(x.Key(), y.Key()) && uni(x.Elem(), y.Elem())
		case *types.Named:
			y := y.(*types.Named)
			if x.Origin() != y.Origin() {
				return false
			}
			xa, ya := x.TypeArgs(), y.TypeArgs()
			if xa.Len() != ya.Len() {
				return false
			}
			for i := range xa.Len() {
				if !uni(xa.At(i), ya.At(i)) {
					return false
				}
			}
			return true
		case *types.Pointer:
			return uni(x.Elem(), y.(*types.Pointer).Elem())
		case *types.Signature:
			y := y.(*types.Signature)
			return x.Variadic() == y.Variadic() && uni(x.Params(), y.Params()) && uni(x.Results(), y.Results())
		case *types.Slice:
			return uni(x.Elem(), y.(*types.Slice).Elem())
		case *types.Struct:
			y := y.(*types.Struct)
			if x.NumFields() != y.NumFields() {
				return false
			}
			for i := range x.NumFields() {
				xf, yf := x.Field(i), y.Field(i)
				if xf.Embedded() != yf.Embedded() || xf.Name() != yf.Name() || x.Tag(i) != y.Tag(i) ||
					!xf.Exported() && xf.Pkg() != yf.Pkg() || !uni(xf.Type(), yf.Type()) {
					return false
				}
			}
			return true
		case *types.Tuple:
			y := y.(*types.Tuple)
			if x.Len() != y.Len() {
				return false
			}
			for i := range x.Len() {
				if !uni(x.At(i).Type(), y.At(i).Type()) {
					return false
				}
			}
			return true
		}
		panic("unify: unexpected type")
	}
	return uni(x, y)
}
