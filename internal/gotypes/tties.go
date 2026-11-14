package gotypes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"runtime"
	"sync"
)

// Which methods are tied together by a package's assignments of types to interfaces (ADR-062).
//
// A method's rename has to know them: renaming an interface's method renames the methods of the types assigned to
// that interface, and a concrete method an interface requires is not renamed on its own. gopls finds them with
// x/tools' refactor/satisfy (tsatisfy.go here), which reads the type of every expression, and the tables a held
// program keeps do not have that. Recording it while loading was measured: it made every load 40 % slower (0.85 s to
// 1.20 s on goinfer), for the sake of one kind of rename. So a package is type-checked once more, in full, the first
// time a rename asks for its ties, against the dependencies the program already has; what is kept is small (goinfer:
// 2,032 constraints in 127 packages) and stands until the package or something it imports changes.
//
// A tie names its two methods by where they are declared, not by object: the package checked again here has new
// objects for its own declarations, at the positions the program's objects have.

// methodTie says that somewhere in a package a type is used as an interface, and so that this method of the
// interface and this method of the type must keep the same name.
type methodTie struct {
	lhs, rhs         targetKey // the interface's method, the type's
	rhsIface         bool      // the type is itself an interface
	lhsType, rhsType string    // the interface and the type, for a message
	lhsShort         string    // the interface as a reader names it: "interface Shape"
	lhsDesc, rhsDesc string    // the two methods, for a message
	// at is set when the tie comes from a type assertion x.(T), or a case of a type switch: the one use the
	// compiler does not hold to it, since an assertion that cannot succeed is still legal between interfaces.
	at token.Pos
}

// pkgTies are the ties of one package, by method name.
type pkgTies struct {
	byName map[string][]methodTie
	// partial: part of the package did not type-check, and a tie made only there is not known.
	partial bool
}

func keyOf(obj types.Object) targetKey {
	k := targetKey{pos: obj.Pos()}
	if obj.Pkg() != nil {
		k.pkgPath = obj.Pkg().Path()
	}
	return k
}

// tiesOf returns tp's ties, finding them on first use.
func (p *program) tiesOf(tp *tPackage) *pkgTies {
	if t := tp.ties.Load(); t != nil {
		return t
	}
	tp.tiesMu.Lock()
	defer tp.tiesMu.Unlock()
	if t := tp.ties.Load(); t != nil {
		return t
	}
	t := p.findTies(tp)
	tp.ties.Store(t)
	return t
}

// tiesWork lets one program at a time find ties. Finding them uses every processor and, while it runs, about the
// memory of loading the packages: three configurations doing it side by side would finish no sooner and hold three
// times as much.
var tiesWork sync.Mutex

// ensureTies finds the ties of every package of pkgs that does not have them yet, on every processor.
func (p *program) ensureTies(pkgs map[*tPackage]bool) {
	var todo []*tPackage
	for tp := range pkgs {
		if tp.ties.Load() == nil {
			todo = append(todo, tp)
		}
	}
	if len(todo) == 0 {
		return
	}
	tiesWork.Lock()
	defer tiesWork.Unlock()
	next := make(chan *tPackage)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tp := range next {
				p.tiesOf(tp)
			}
		}()
	}
	for _, tp := range todo {
		next <- tp
	}
	close(next)
	wg.Wait()
}

// findTies type-checks tp again, with everything recorded, and reads its ties off the result.
func (p *program) findTies(tp *tPackage) *pkgTies {
	out := &pkgTies{byName: map[string][]methodTie{}}
	var files []*ast.File
	for _, name := range tp.Files {
		p.parseMu.Lock()
		tf := p.tfiles[name]
		p.parseMu.Unlock()
		src, ok := p.overlay[name]
		if !ok {
			var err error
			if src, err = os.ReadFile(name); err != nil {
				out.partial = true
				continue
			}
		}
		f := reparse(tf, name, src, parser.SkipObjectResolution)
		if f == nil {
			out.partial = true // not the file the program read, or one that does not parse
			continue
		}
		files = append(files, f)
	}
	info := &types.Info{
		Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
		Instances: map[*ast.Ident]types.Instance{},
	}
	conf := types.Config{
		Importer:    tImporter{p, tp},
		Sizes:       p.sizes,
		FakeImportC: true,
		Error: func(err error) {
			if te, ok := err.(types.Error); !ok || !te.Soft {
				out.partial = true
			}
		},
	}
	if tp.GoVersion != "" {
		conf.GoVersion = "go" + tp.GoVersion
	}
	_, _ = conf.Check(tp.PkgPath, p.fset, files, info)
	f := &satFinder{}
	f.find(info, files)
	if f.skipped > 0 {
		out.partial = true
	}
	seen := map[methodTie]bool{}
	add := func(c satConstraint, at token.Pos) {
		lms, rms := f.methodSet(c.LHS), f.methodSet(c.RHS)
		// (a type parameter stands for its constraint: a value passed as a T must have the constraint's methods)
		iface := types.Unalias(c.LHS)
		if param, ok := iface.(*types.TypeParam); ok {
			iface = types.Unalias(param.Constraint())
		}
		short := iface.String()
		if named, ok := iface.(*types.Named); ok {
			short = "interface " + named.Obj().Name()
		}
		for i := range lms.Len() {
			l, ok := lms.At(i).Obj().(*types.Func)
			if !ok {
				continue
			}
			rsel := rms.Lookup(l.Pkg(), l.Name())
			if rsel == nil {
				continue
			}
			r, ok := rsel.Obj().(*types.Func)
			if !ok {
				continue
			}
			// As declarations: the method of Getter[int] is the method of Getter[T].
			l, r = l.Origin(), r.Origin()
			t := methodTie{lhs: keyOf(l), rhs: keyOf(r), rhsIface: types.IsInterface(c.RHS), at: at,
				lhsType: c.LHS.String(), rhsType: c.RHS.String(), lhsShort: short,
				lhsDesc: types.ObjectString(l, nil), rhsDesc: types.ObjectString(r, nil)}
			if !seen[t] {
				seen[t] = true
				out.byName[l.Name()] = append(out.byName[l.Name()], t)
			}
		}
	}
	for c := range f.result {
		add(c, token.NoPos)
	}
	for _, a := range f.asserts {
		add(a.c, a.at)
	}
	return out
}
