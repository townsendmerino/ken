package gotypes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Use is one identifier of the imported package that the importing package names.
type Use struct {
	Name    string   `json:"name"` // Name for a package-level identifier, Type.Member for a method or field
	Kind    string   `json:"kind"` // func, type, const, var, method, field
	NonTest int      `json:"non_test"`
	Test    int      `json:"test"`    // lines in files ending _test.go
	Configs []string `json:"configs"` // every configuration under which some line names it
}

// UsesResult is the merged answer of a Uses query: the surface of one package that another depends on.
type UsesResult struct {
	Importer     string `json:"importer"`               // repository-relative directory
	ImporterPath string `json:"importer_path"`          // its import path
	Imported     string `json:"imported"`               // import path
	ImportedDir  string `json:"imported_dir,omitempty"` // repository-relative directory, when it is in the repository
	Uses         []Use  `json:"uses"`
	// The counts cover every identifier; Uses may be cut shorter by a caller's cap. A member is a method or a field.
	Total               int `json:"total"`
	PackageLevel        int `json:"package_level"`
	PackageLevelNonTest int `json:"package_level_non_test"` // those that some non-test file names
	Members             int `json:"members"`
	MembersNonTest      int `json:"members_non_test"`
	// Lines counts (identifier, line) pairs, so a line naming two identifiers is two; Files counts the files.
	Lines int         `json:"lines"`
	Files int         `json:"files"`
	Runs  []ConfigRun `json:"runs"`
	// Incomplete is true when no listed configuration builds the importer, or a configuration failed to list or
	// did not type-check cleanly: the uses are then a lower bound. A run's Hits counts (identifier, line) pairs.
	Incomplete bool `json:"incomplete"`
}

// listPkg is the part of `go list -json` output Uses reads.
type listPkg struct {
	ImportPath   string
	Name         string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Imports      []string
	TestImports  []string
	XTestImports []string
	ImportMap    map[string]string
	Error        *struct{ Err string }
}

const listFields = "ImportPath,Name,Dir,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Imports,TestImports,XTestImports,ImportMap,Error"

// plainPath strips the test-variant suffix go list adds: "p [q.test]" is p.
func plainPath(p string) string {
	if i := strings.Index(p, " ["); i >= 0 {
		return p[:i]
	}
	return p
}

// packageDirs returns the repository's directories that hold .go files, slash-separated, "." for the root.
func packageDirs(repo string) ([]string, error) {
	var dirs []string
	last := ""
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != repo && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if dir := filepath.Dir(p); strings.HasSuffix(p, ".go") && dir != last {
			last = dir
			rel, _ := filepath.Rel(repo, dir)
			dirs = append(dirs, filepath.ToSlash(rel))
		}
		return nil
	})
	return dirs, err
}

// dirDeclares reports whether some .go file in dir has the package clause name (or name_test).
func dirDeclares(repo, dir, name string) bool {
	ents, _ := os.ReadDir(filepath.Join(repo, filepath.FromSlash(dir)))
	fset := token.NewFileSet()
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, _ := parser.ParseFile(fset, filepath.Join(repo, filepath.FromSlash(dir), e.Name()), nil, parser.PackageClauseOnly)
		if f != nil && f.Name != nil && strings.TrimSuffix(f.Name.Name, "_test") == name {
			return true
		}
	}
	return false
}

func cleanPkgSpec(spec string) string {
	spec = strings.TrimSpace(filepath.ToSlash(spec))
	if spec == "." || spec == "./" {
		return "."
	}
	return strings.Trim(strings.TrimPrefix(spec, "./"), "/")
}

// ResolvePackageDir finds the one directory of the repository that spec names: a repository-relative directory
// (backend/cuda), the tail of one (cuda, when one directory ends that way), or a package name. A directory given in
// full wins over a tail or a name that also matches elsewhere.
func ResolvePackageDir(repo, spec string) (string, error) {
	spec = cleanPkgSpec(spec)
	if spec == "" {
		return "", errors.New("a package is required: a directory (backend/cuda) or a package name")
	}
	dirs, err := packageDirs(repo)
	if err != nil {
		return "", err
	}
	var found []string
	for _, d := range dirs {
		switch {
		case d == spec:
			return d, nil
		case strings.HasSuffix(d, "/"+spec):
			found = append(found, d)
		case !strings.Contains(spec, "/") && token.IsIdentifier(spec) && dirDeclares(repo, d, spec):
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%w: no directory or package %q with Go files in this repository", ErrNotFound, spec)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%w %q: it names %d packages; give the directory: %s", ErrAmbiguous, spec, len(found), strings.Join(found, "; "))
}

// relDir returns dir relative to the repository, slash-separated, or "" when it is outside it.
func (r *Runner) relDir(dir string) string {
	roots := []string{r.Repo}
	if real, err := filepath.EvalSymlinks(r.Repo); err == nil && real != r.Repo {
		roots = append(roots, real)
	}
	for _, root := range roots {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

// usesLoad is one configuration's view of the importing package and everything it depends on.
type usesLoad struct {
	bc     BuildConfig
	run    ConfigRun
	target *listPkg
	pkgs   map[string]*listPkg // by import path, test-variant suffix stripped
	direct map[string]bool     // import paths the importer's own files import
}

// list asks the go command which files the importer and its dependencies are built from under one configuration.
func (r *Runner) list(ctx context.Context, bc BuildConfig, dir string) usesLoad {
	l := usesLoad{bc: bc, run: ConfigRun{Config: bc.Name}, pkgs: map[string]*listPkg{}, direct: map[string]bool{}}
	goBin := r.Go
	if goBin == "" {
		goBin = "go"
	}
	abs := filepath.Join(r.Repo, filepath.FromSlash(dir))
	stdout, stderr, err := r.exec(ctx, bc, goBin, "-C", abs, "list", "-e", "-deps", "-test", "-json="+listFields, ".")
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		l.run.Err = "go list: " + msg
		return l
	}
	var variants []*listPkg
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		p := new(listPkg)
		if err := dec.Decode(p); err == io.EOF {
			break
		} else if err != nil {
			l.run.Err = "go list: unreadable output: " + err.Error()
			return l
		}
		if strings.HasSuffix(p.ImportPath, ".test") && p.Name == "main" {
			continue // the generated test binary
		}
		if plain := plainPath(p.ImportPath); plain != p.ImportPath {
			variants = append(variants, p)
		} else {
			l.pkgs[plain] = p
			if p.Dir != "" && filepath.Base(p.Dir) == filepath.Base(abs) && r.relDir(p.Dir) == dir {
				l.target = p
			}
		}
	}
	// A package reached only through the test variant of another has no plain entry; a variant's import map covers
	// the test files' imports, which the plain entry's does not.
	for _, v := range variants {
		into := l.pkgs[plainPath(v.ImportPath)]
		if l.target != nil && plainPath(v.ImportPath) == l.target.ImportPath+"_test" {
			into = l.target // the external test package is checked with the importer's entry
		}
		if into == nil {
			l.pkgs[plainPath(v.ImportPath)] = v
			continue
		}
		for from, to := range v.ImportMap {
			if into.ImportMap == nil {
				into.ImportMap = map[string]string{}
			}
			if _, has := into.ImportMap[from]; !has {
				into.ImportMap[from] = to
			}
		}
	}
	t := l.target
	switch {
	case t == nil:
		l.run.Err = "go list: did not report the package in " + dir
	case len(t.GoFiles)+len(t.CgoFiles)+len(t.TestGoFiles)+len(t.XTestGoFiles) == 0:
		if t.Error != nil && !strings.Contains(t.Error.Err, "build constraints exclude all Go files") && !strings.Contains(t.Error.Err, "no Go files") {
			l.run.Err = "go list: " + t.Error.Err
		} else {
			l.run.Skipped = "does not build this package"
		}
		l.target = nil
	default:
		for _, list := range [][]string{t.Imports, t.TestImports, t.XTestImports} {
			for _, p := range list {
				if m := t.ImportMap[p]; m != "" {
					p = m
				}
				l.direct[plainPath(p)] = true
			}
		}
	}
	return l
}

// resolveImported picks, among the packages the importer depends on under any configuration, the one spec names: an
// import path, a repository-relative directory, the tail of either, or a package name. A full path or directory wins;
// among several tail or name matches, one the importer imports directly wins.
func (r *Runner) resolveImported(loads []usesLoad, importerDir, spec string) (path, dir string, err error) {
	spec = cleanPkgSpec(spec)
	if spec == "" {
		return "", "", errors.New("the imported package is required: an import path, a directory, or a package name")
	}
	type cand struct {
		dir    string
		direct bool
	}
	exact, loose := map[string]cand{}, map[string]cand{}
	for _, l := range loads {
		for p, lp := range l.pkgs {
			rel := ""
			if lp.Dir != "" {
				rel = r.relDir(lp.Dir)
			}
			c := cand{rel, l.direct[p]}
			switch {
			case p == spec || (rel != "" && rel == spec):
				exact[p] = cand{rel, c.direct || exact[p].direct}
			case strings.HasSuffix(p, "/"+spec) || strings.HasSuffix(rel, "/"+spec) || lp.Name == spec:
				loose[p] = cand{rel, c.direct || loose[p].direct}
			}
		}
	}
	pick := func(m map[string]cand) []string {
		var all, direct []string
		for p, c := range m {
			all = append(all, p)
			if c.direct {
				direct = append(direct, p)
			}
		}
		if len(direct) == 1 {
			return direct
		}
		sort.Strings(all)
		return all
	}
	for _, m := range []map[string]cand{exact, loose} {
		switch got := pick(m); len(got) {
		case 0:
			continue
		case 1:
			return got[0], m[got[0]].dir, nil
		default:
			return "", "", fmt.Errorf("%w %q: it names %d packages %s depends on; give the import path or directory: %s",
				ErrAmbiguous, spec, len(got), importerDir, strings.Join(got, "; "))
		}
	}
	if d, derr := ResolvePackageDir(r.Repo, spec); derr == nil {
		return "", "", fmt.Errorf("%w: %s is a package of this repository, but %s does not depend on it under any listed configuration, so it names nothing from it", ErrNotFound, d, importerDir)
	}
	return "", "", fmt.Errorf("%w: %q matches no package that %s depends on under any listed configuration", ErrNotFound, spec, importerDir)
}

// typeLoader type-checks packages from source, from the file lists go list reported for one configuration.
// Dependencies are checked without function bodies: only their declarations are needed.
type typeLoader struct {
	fset  *token.FileSet
	pkgs  map[string]*listPkg
	done  map[string]*types.Package
	busy  map[string]bool
	sizes types.Sizes
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func (l *typeLoader) importerFor(from *listPkg) types.Importer {
	return importerFunc(func(p string) (*types.Package, error) {
		if p == "unsafe" {
			return types.Unsafe, nil
		}
		if m := from.ImportMap[p]; m != "" {
			p = plainPath(m)
		}
		return l.load(p)
	})
}

func (l *typeLoader) parse(lp *listPkg, names []string) []*ast.File {
	var files []*ast.File
	for _, n := range names {
		// A file with syntax errors still yields what parsed before them.
		if f, _ := parser.ParseFile(l.fset, filepath.Join(lp.Dir, n), nil, parser.SkipObjectResolution); f != nil {
			files = append(files, f)
		}
	}
	return files
}

func (l *typeLoader) load(p string) (*types.Package, error) {
	if pkg := l.done[p]; pkg != nil {
		return pkg, nil
	}
	lp := l.pkgs[p]
	if lp == nil || l.busy[p] {
		return nil, fmt.Errorf("package %s is not among the listed dependencies", p)
	}
	l.busy[p] = true
	conf := types.Config{Importer: l.importerFor(lp), IgnoreFuncBodies: true, FakeImportC: true, Sizes: l.sizes, Error: func(error) {}}
	pkg, _ := conf.Check(p, l.fset, l.parse(lp, append(append([]string{}, lp.GoFiles...), lp.CgoFiles...)), nil)
	l.done[p] = pkg
	return pkg, nil
}

// check type-checks one unit of the importing package in full and returns what its identifiers resolve to, with
// the number of type errors and the first of them.
func (l *typeLoader) check(lp *listPkg, pkgPath string, names []string) (*types.Package, *types.Info, int, string) {
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	nerr, first := 0, ""
	conf := types.Config{Importer: l.importerFor(lp), FakeImportC: true, Sizes: l.sizes, Error: func(err error) {
		if te, ok := err.(types.Error); ok && te.Soft {
			return // unused variables and imports do not change what an identifier means
		}
		if nerr++; nerr == 1 {
			first = err.Error()
			if te, ok := err.(types.Error); ok {
				p := te.Fset.Position(te.Pos)
				first = fmt.Sprintf("%s:%d: %s", filepath.Base(p.Filename), p.Line, te.Msg)
			}
		}
	}}
	pkg, _ := conf.Check(pkgPath, l.fset, l.parse(lp, names), info)
	return pkg, info, nerr, first
}

// fieldOwners maps each struct field declared in pkg's package-level types and variables to the name that owns it.
func fieldOwners(pkg *types.Package) map[*types.Var]string {
	owners := map[*types.Var]string{}
	seen := map[types.Type]bool{}
	var walk func(t types.Type, owner string)
	walk = func(t types.Type, owner string) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Struct:
			for i := range t.NumFields() {
				f := t.Field(i)
				owners[f] = owner
				if _, named := types.Unalias(f.Type()).(*types.Named); !named {
					walk(f.Type(), owner)
				}
			}
		case *types.Pointer:
			walk(t.Elem(), owner)
		case *types.Slice:
			walk(t.Elem(), owner)
		case *types.Array:
			walk(t.Elem(), owner)
		case *types.Chan:
			walk(t.Elem(), owner)
		case *types.Map:
			walk(t.Key(), owner)
			walk(t.Elem(), owner)
		}
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		switch o := scope.Lookup(name).(type) {
		case *types.TypeName:
			if !o.IsAlias() {
				walk(o.Type().Underlying(), name)
			}
		case *types.Var:
			if _, named := types.Unalias(o.Type()).(*types.Named); !named {
				walk(o.Type(), name)
			}
		}
	}
	return owners
}

// describe names an object of the imported package the way a reader would write it, and says what it is. ok is
// false for an object that is not part of a package's surface.
func describe(obj types.Object, owners map[*types.Var]string) (name, kind string, ok bool) {
	switch o := obj.(type) {
	case *types.TypeName:
		return o.Name(), "type", true
	case *types.Const:
		return o.Name(), "const", true
	case *types.Func:
		o = o.Origin()
		sig, _ := o.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			return o.Name(), "func", true
		}
		t := types.Unalias(sig.Recv().Type())
		if p, isPtr := t.(*types.Pointer); isPtr {
			t = types.Unalias(p.Elem())
		}
		if n, isNamed := t.(*types.Named); isNamed {
			return n.Obj().Name() + "." + o.Name(), "method", true
		}
		return "(interface)." + o.Name(), "method", true
	case *types.Var:
		o = o.Origin()
		if o.IsField() {
			owner := owners[o]
			if owner == "" {
				owner = "(struct)"
			}
			return owner + "." + o.Name(), "field", true
		}
		if o.Parent() == o.Pkg().Scope() {
			return o.Name(), "var", true
		}
	}
	return "", "", false
}

type useKey struct{ name, kind string }

type usePos struct {
	file string
	line int
}

// Uses returns the identifiers of the imported package that the importing package's files name, each with the number
// of lines naming it, under any listed configuration. It is the surface a package seam would have to carry.
//
// Both packages are given the way a symbol's package is: a repository-relative directory, the tail of one, or a
// package name; the imported one may also be an import path, and need not be in the repository. The answer is
// type-resolved: an aliased or dot import counts, a local identifier that merely shares the package's name does not,
// and methods and fields count as well as package-level names (o.Run() names Type.Run). Test files (those ending
// _test.go, including an external test package) are counted apart.
//
// Each configuration asks the go command for the files it builds (go list, behind the repository's wrap command) and
// type-checks them from source in this process, with the standard library's go/types.
func (r *Runner) Uses(ctx context.Context, importer, imported string) (*UsesResult, error) {
	dir, err := ResolvePackageDir(r.Repo, importer)
	if err != nil {
		return nil, err
	}
	loads := make([]usesLoad, len(r.Config.Configs))
	for i, bc := range r.Config.Configs {
		t := time.Now()
		loads[i] = r.list(ctx, bc, dir)
		loads[i].run.Seconds = time.Since(t).Seconds()
	}
	res := &UsesResult{Importer: dir}
	built := false
	for _, l := range loads {
		if l.target != nil {
			built = true
			res.ImporterPath = l.target.ImportPath
		}
	}
	if built {
		if res.Imported, res.ImportedDir, err = r.resolveImported(loads, dir, imported); err != nil {
			return nil, err
		}
	}

	seen := map[useKey]map[usePos]map[string]bool{}
	for i := range loads {
		l := &loads[i]
		if l.target != nil && ctx.Err() == nil {
			t := time.Now()
			r.usesUnder(l, dir, res.Imported, seen)
			l.run.Seconds += time.Since(t).Seconds()
		}
		l.run.Seconds = float64(int(l.run.Seconds*1000)) / 1000
		if len(l.run.Err) > 300 {
			l.run.Err = l.run.Err[:300]
		}
		if l.run.Err != "" || l.run.Partial != "" {
			res.Incomplete = true
		}
		res.Runs = append(res.Runs, l.run)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !built {
		res.Incomplete = true
	}

	files := map[string]bool{}
	for k, at := range seen {
		u := Use{Name: k.name, Kind: k.kind}
		cfgs := map[string]bool{}
		for pos, by := range at {
			files[pos.file] = true
			if isTestFile(pos.file) {
				u.Test++
			} else {
				u.NonTest++
			}
			for c := range by {
				cfgs[c] = true
			}
		}
		for _, bc := range r.Config.Configs {
			if cfgs[bc.Name] {
				u.Configs = append(u.Configs, bc.Name)
			}
		}
		member := k.kind == "method" || k.kind == "field"
		switch {
		case member:
			res.Members++
			if u.NonTest > 0 {
				res.MembersNonTest++
			}
		default:
			res.PackageLevel++
			if u.NonTest > 0 {
				res.PackageLevelNonTest++
			}
		}
		res.Lines += u.NonTest + u.Test
		res.Uses = append(res.Uses, u)
	}
	sort.Slice(res.Uses, func(i, j int) bool {
		a, b := res.Uses[i], res.Uses[j]
		if a.NonTest+a.Test != b.NonTest+b.Test {
			return a.NonTest+a.Test > b.NonTest+b.Test
		}
		return a.Name < b.Name
	})
	res.Total, res.Files = len(res.Uses), len(files)
	return res, nil
}

// usesUnder type-checks the importer under one configuration and adds what it names from the imported package.
func (r *Runner) usesUnder(l *usesLoad, dir, imported string, seen map[useKey]map[usePos]map[string]bool) {
	t := l.target
	tl := &typeLoader{fset: token.NewFileSet(), pkgs: l.pkgs, done: map[string]*types.Package{}, busy: map[string]bool{},
		sizes: types.SizesFor("gc", l.bc.goarch())}
	nerr, first := 0, ""
	lines := 0
	owners := map[*types.Package]map[*types.Var]string{}
	collect := func(info *types.Info) {
		for id, obj := range info.Uses {
			if obj.Pkg() == nil || obj.Pkg().Path() != imported {
				continue
			}
			if owners[obj.Pkg()] == nil {
				owners[obj.Pkg()] = fieldOwners(obj.Pkg())
			}
			name, kind, ok := describe(obj, owners[obj.Pkg()])
			if !ok {
				continue
			}
			p := tl.fset.Position(id.Pos())
			pos := usePos{path.Join(dir, filepath.Base(p.Filename)), p.Line}
			k := useKey{name, kind}
			if seen[k] == nil {
				seen[k] = map[usePos]map[string]bool{}
			}
			if seen[k][pos] == nil {
				seen[k][pos] = map[string]bool{}
			}
			if !seen[k][pos][l.bc.Name] {
				seen[k][pos][l.bc.Name] = true
				lines++
			}
		}
	}
	// The package with its in-package test files is one unit; an external test package imports that unit.
	if in := append(append(append([]string{}, t.GoFiles...), t.CgoFiles...), t.TestGoFiles...); len(in) > 0 {
		pkg, info, n, msg := tl.check(t, t.ImportPath, in)
		if nerr += n; first == "" {
			first = msg
		}
		tl.done[t.ImportPath] = pkg
		collect(info)
	}
	if len(t.XTestGoFiles) > 0 {
		_, info, n, msg := tl.check(t, t.ImportPath+"_test", t.XTestGoFiles)
		if nerr += n; first == "" {
			first = msg
		}
		collect(info)
	}
	l.run.Hits = lines
	if nerr > 0 {
		l.run.Partial = fmt.Sprintf("%d type errors, so some uses may be missing; first: %s", nerr, first)
	}
}

// Text renders the result: a header with the totals, then one row per identifier, most-named first. A row names its
// configurations only when some listed configuration did not see it. maxRows caps the rows (0 means all).
func (u *UsesResult) Text(nConfigs, maxRows int) string {
	var b strings.Builder
	what := u.Imported
	if u.ImportedDir != "" {
		what = u.ImportedDir
	}
	if what == "" {
		what = "?"
	}
	fmt.Fprintf(&b, "uses of %s by %s: %d identifiers, %d lines in %d files\n", what, u.Importer, u.Total, u.Lines, u.Files)
	if u.ImportedDir != "" || u.ImporterPath != "" {
		fmt.Fprintf(&b, "import paths: %s <- %s\n", u.Imported, u.ImporterPath)
	}
	fmt.Fprintf(&b, "package-level: %d (%d named by non-test code) · methods and fields: %d (%d named by non-test code)\n",
		u.PackageLevel, u.PackageLevelNonTest, u.Members, u.MembersNonTest)
	fmt.Fprintf(&b, "per configuration (lines): %s\n", runsLine(u.Runs))
	asked := false
	for _, cr := range u.Runs {
		asked = asked || cr.Skipped == ""
		if cr.Err != "" {
			fmt.Fprintf(&b, "INCOMPLETE: %s: %s\n", cr.Config, firstLine(cr.Err))
		}
		if cr.Partial != "" {
			fmt.Fprintf(&b, "INCOMPLETE: %s: %s\n", cr.Config, firstLine(cr.Partial))
		}
	}
	if !asked {
		fmt.Fprintf(&b, "INCOMPLETE: no listed configuration builds %s; add one to %s\n", u.Importer, ConfigFile)
	}
	shown := u.Uses
	if maxRows > 0 && len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	w, kw, nw, tw := 0, 0, 1, 1
	for _, x := range shown {
		w, kw = max(w, len(x.Name)), max(kw, len(x.Kind))
		nw, tw = max(nw, len(fmt.Sprint(x.NonTest))), max(tw, len(fmt.Sprint(x.Test)))
	}
	for _, x := range shown {
		fmt.Fprintf(&b, "%-*s  %-*s  %*d non-test  %*d test", w, x.Name, kw, x.Kind, nw, x.NonTest, tw, x.Test)
		if len(x.Configs) < nConfigs {
			fmt.Fprintf(&b, "  [%s]", strings.Join(x.Configs, ","))
		}
		b.WriteByte('\n')
	}
	if len(shown) < len(u.Uses) {
		fmt.Fprintf(&b, "… %d more identifiers not shown\n", len(u.Uses)-len(shown))
	}
	return b.String()
}
