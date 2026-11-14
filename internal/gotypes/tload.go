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
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// This file loads one build configuration of a workspace into this process: the go command lists every package and
// its test variants, and the standard library's go/types checks them from source. It is the ground the in-process
// references and implementations answers (trefs.go, timpl.go) stand on, and it follows how gopls sees a workspace,
// because those answers are held to gopls's (ADR-061):
//
//   - a package is one record of `go list -deps -test -compiled`, variants included: p, "p [p.test]" (p with its
//     in-package tests), "p_test [p.test]" (the external test package), and the intermediate test variants
//     "q [p.test]" that exist because q depends on p's test variant;
//   - a workspace package is one whose directory is in a workspace module (the go.work modules, or the one module),
//     and it is checked in full; every other package is checked for its declarations only;
//   - an intermediate test variant is never searched, only imported.
//
// Each file is parsed once and its syntax shared by every variant that compiles it, so an object's declaring
// position (token.Pos) names the same declaration in every variant.

// tPackage is one go list record and, after check, its types.
type tPackage struct {
	ID        string // go list's ImportPath, with any " [x.test]" suffix
	PkgPath   string // the types.Package path: ID without the suffix
	Name      string
	Dir       string
	ForTest   string
	Files     []string          // compiled Go files, absolute
	Imports   map[string]string // import path as written -> ID
	deps      []string          // IDs of direct imports
	GoVersion string
	Standard  bool
	Workspace bool // in a workspace module
	listErr   string

	importedBy []*tPackage

	once                       sync.Once
	types                      *types.Package
	nerr                       int
	parseSeconds, checkSeconds float64 // this package's own share, for profiling a load

	// Filled by check for a package that is searched, in place of its syntax and types.Info, which are dropped once
	// these are built (they are most of a program's memory):
	bodies    bool                       // checked with function bodies: uses holds its identifiers
	defs      map[token.Pos]types.Object // each declaring identifier's object, by the identifier's position
	useAtDecl map[token.Pos]types.Object // the object an identifier uses, for the few that also declare (struct{T})
	uses      map[string][]useRec        // every identifier that uses an object, by the identifier's name
	typeSpecs []typeSpecRec              // every type declaration, function-local ones included
	syntax    []*ast.File                // kept only when the program is loaded with keepSyntax
	errs      []typeErr                  // its hard type errors
	// soft holds the errors the checker calls soft and errs leaves out (an unused variable or import, an unused
	// label). The compiler rejects them all the same, which is what the check of an edit has to say (tcheckedit.go).
	soft []typeErr

	// The methods tied together by this package's assignments of types to interfaces, which a method's rename has to
	// know (tties.go). They need more of a type-check than the tables keep, so they are found on first use.
	tiesMu sync.Mutex
	ties   atomic.Pointer[pkgTies]
	// defNames: the names its identifiers declare, listed on first use (see spells).
	defNames atomic.Pointer[map[string]bool]
}

// typeErr is one hard type error.
type typeErr struct {
	pos token.Pos
	msg string
}

// useRec is one identifier that uses obj.
type useRec struct {
	pos token.Pos
	obj types.Object
}

// typeSpecRec is one type declaration: the position of its name and the name's object.
type typeSpecRec struct {
	pos token.Pos
	def *types.TypeName
}

// itv reports whether the package is an intermediate test variant: recompiled for another package's test.
func (tp *tPackage) itv() bool {
	return tp.ForTest != "" && tp.ForTest != tp.PkgPath && tp.ForTest+"_test" != tp.PkgPath
}

// full reports whether the package is one that is searched: a workspace package, not an intermediate test variant.
func (tp *tPackage) full() bool { return tp.Workspace && !tp.itv() }

// program is one build configuration of a workspace, type-checked.
type program struct {
	bc     BuildConfig
	fset   *token.FileSet
	pkgs   map[string]*tPackage
	order  []*tPackage            // go list order (dependencies first)
	byFile map[string][]*tPackage // absolute file -> the packages that compile it
	sizes  types.Sizes

	parseMu sync.Mutex
	parsed  map[string]*parsedFile
	tfiles  map[string]*token.File // by file name, for turning line:col into a position

	keepSyntax bool // the differential test walks declarations
	// overlay holds file contents that stand in for the files on disk: a patch being checked (tverify.go).
	overlay map[string][]byte
	// forceBodies names packages (by ID) that are checked with function bodies whatever needsBodies says: the ones
	// that were, in the program a patch is checked against.
	forceBodies map[string]bool
	// bodyNames, when not nil, makes the program check only the function bodies that spell one of these names:
	// see stripBodies. Only a program made to check a patch has it.
	bodyNames [][]byte
	// leanNames, when not nil, makes the program keep, of each package it checks, only what a patch's check reads:
	// the uses of these names, the type declarations and the errors.
	leanNames map[string]bool
	// read, when not nil, reads a file that overlay does not hold: the configurations of one rename plan read
	// mostly the same files, once between them.
	read func(file string) ([]byte, error)
	// added lists the files a program made to check a patch parsed afresh into the file set. It shares that file
	// set with the program it was refreshed from, and takes them out again when it is done (discard).
	added []*token.File
	// rechecked names (by ID) the packages a refresh type-checked again; nil in a program loaded afresh, where
	// every package was.
	rechecked map[string]bool

	refreshBroken atomic.Bool // a refresh found a file it took for unchanged to be different: its result is discarded

	// derived, built on first use
	xrefsOnce sync.Once
	xrefs     map[xrefKey][]xref
	msetsMu   sync.Mutex
	msets     map[*tPackage]*msetIndex
	reachMu   sync.Mutex
	reach     map[*types.Package]map[types.Object]bool
	rdepsMu   sync.Mutex
	rdepsMemo map[rdepsKey]map[*tPackage]bool
	byPosMu   sync.Mutex
	byPos     map[*tPackage]map[token.Pos]types.Object // see objAt

	mods                      []string
	listSeconds, checkSeconds float64
	refreshed                 int // packages checked again, when this program was refreshed from another
}

type parsedFile struct {
	once  sync.Once
	file  *ast.File
	tfile *token.File
	users atomic.Int32 // packages that compile it and have not been checked yet

	// What is kept about the file after its syntax is dropped. metaOK says these are known: set by a parse, or
	// carried over from the program this one was refreshed from. carried says the latter: the file is unchanged, so
	// they are final from the start and no parse writes them again, which is what lets needsBodies read them while
	// another goroutine parses the file.
	carried   bool
	metaOK    bool
	parsedOK  bool
	hasMethod bool   // declares a method (see needsBodies)
	universe  bool   // declares, at package level, a name the universe scope also has (see needsBodies)
	header    uint64 // headerSig of its source: what the go command's package list depends on
	// bodies holds where each function body of the file is: the offsets of its braces, in pairs. A program made to
	// check a patch blanks the ones it will not check before it parses (see blankBodies).
	bodies []int32
	// base, when not 0, is where an unchanged file sat in the file set of the program this one was refreshed from:
	// it is parsed again at the same place, so that every position in it is the position it had.
	base int
}

type rdepsKey struct {
	tp         *tPackage
	transitive bool
}

type listRecord struct {
	ImportPath      string
	Name            string
	Dir             string
	ForTest         string
	GoFiles         []string
	CgoFiles        []string
	CompiledGoFiles []string
	Imports         []string
	ImportMap       map[string]string
	Standard        bool
	Module          *struct {
		Dir       string
		GoVersion string
		Main      bool
	}
	Error *struct{ Err string }
}

const tListFields = "ImportPath,Name,Dir,ForTest,GoFiles,CgoFiles,CompiledGoFiles,Imports,ImportMap,Standard,Module,Error"

// workspaceOf returns the directories of the modules that make up the workspace containing dir under bc: the go.work
// modules when there is a workspace file, else the one module dir is in. That is the set gopls loads.
func (r *Runner) workspaceOf(ctx context.Context, bc BuildConfig, dir string) ([]string, error) {
	out, stderr, err := r.exec(ctx, bc, r.goPath(), "-C", dir, "env", "GOWORK", "GOMOD")
	if err != nil {
		return nil, fmt.Errorf("go env: %s", errText(stderr, err))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for len(lines) < 2 {
		lines = append(lines, "")
	}
	gowork, gomod := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if gowork != "" && gowork != "off" {
		wout, werr, err := r.exec(ctx, bc, r.goPath(), "-C", filepath.Dir(gowork), "work", "edit", "-json")
		if err != nil {
			return nil, fmt.Errorf("go work edit: %s", errText(werr, err))
		}
		var w struct{ Use []struct{ DiskPath string } }
		if err := json.Unmarshal(wout, &w); err != nil {
			return nil, fmt.Errorf("go work edit: %w", err)
		}
		var dirs []string
		for _, u := range w.Use {
			d := u.DiskPath
			if !filepath.IsAbs(d) {
				d = filepath.Join(filepath.Dir(gowork), d)
			}
			dirs = append(dirs, filepath.Clean(d))
		}
		if len(dirs) > 0 {
			return dirs, nil
		}
	}
	if gomod == "" || gomod == os.DevNull {
		return nil, errors.New("not in a Go module: no go.mod or go.work above " + dir)
	}
	return []string{filepath.Dir(gomod)}, nil
}

func inDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// listing is the go command's account of one configuration of a workspace: what load type-checks.
type listing struct {
	bc      BuildConfig
	mods    []string // the workspace's module directories
	stdout  []byte   // go list -json records
	seconds float64
}

// listWorkspace asks the go command for every package of the workspace containing dir under bc, with dependencies and test
// variants. It runs go behind the repository's wrap command, and is the only part of a load that leaves this process.
func (r *Runner) listWorkspace(ctx context.Context, bc BuildConfig, dir string) (*listing, error) {
	return r.listWorkspaceWith(ctx, bc, dir, nil)
}

// listWorkspaceWith is listWorkspace with overlay standing in for the files on disk (a file overlay names need
// not exist): the go command reads the package clause, constraints and imports of each from the overlay, so the
// list is the one the tree would have with those contents. The contents go to a temporary directory for the
// length of the run, which is how the go command takes an overlay.
func (r *Runner) listWorkspaceWith(ctx context.Context, bc BuildConfig, dir string, overlay map[string][]byte) (*listing, error) {
	t0 := nowSeconds()
	mods, err := r.workspaceOf(ctx, bc, dir)
	if err != nil {
		return nil, err
	}
	args := []string{"-C", mods[0], "list", "-e", "-deps", "-test", "-compiled", "-json=" + tListFields}
	if len(overlay) > 0 {
		tmp, err := os.MkdirTemp("", "ken-overlay-")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		files := make([]string, 0, len(overlay))
		for f := range overlay {
			files = append(files, f)
		}
		sort.Strings(files)
		replace := map[string]string{}
		for i, f := range files {
			to := filepath.Join(tmp, fmt.Sprintf("%d.go", i))
			if err := os.WriteFile(to, overlay[f], 0o600); err != nil {
				return nil, err
			}
			replace[f] = to
		}
		doc, _ := json.Marshal(map[string]any{"Replace": replace})
		js := filepath.Join(tmp, "overlay.json")
		if err := os.WriteFile(js, doc, 0o600); err != nil {
			return nil, err
		}
		args = append(args, "-overlay", js)
	}
	for _, m := range mods {
		args = append(args, m+string(filepath.Separator)+"...")
	}
	stdout, stderr, err := r.exec(ctx, bc, r.goPath(), args...)
	if err != nil && len(stdout) == 0 {
		return nil, fmt.Errorf("go list: %s", errText(stderr, err))
	}
	return &listing{bc: bc, mods: mods, stdout: stdout, seconds: nowSeconds() - t0}, nil
}

// load lists and type-checks the workspace containing dir under bc.
func (r *Runner) load(ctx context.Context, bc BuildConfig, dir string) (*program, error) {
	return r.loadWith(ctx, bc, dir, false)
}

func (r *Runner) loadWith(ctx context.Context, bc BuildConfig, dir string, keepSyntax bool) (*program, error) {
	l, err := r.listWorkspace(ctx, bc, dir)
	if err != nil {
		return nil, err
	}
	return l.build(keepSyntax)
}

// build type-checks a listing.
func (l *listing) build(keepSyntax bool) (*program, error) {
	return l.buildWith(buildOpts{keepSyntax: keepSyntax})
}

// buildOpts are the ways a listing can be type-checked.
type buildOpts struct {
	keepSyntax bool
	// overlay gives file contents that replace the files on disk.
	overlay map[string][]byte
	// forceBodies: see program.forceBodies.
	forceBodies map[string]bool
	// bodyNames, leanNames, read: see program.
	bodyNames [][]byte
	leanNames map[string]bool
	read      func(file string) ([]byte, error)
}

func (l *listing) buildWith(opts buildOpts) (*program, error) {
	bc, mods, stdout := l.bc, l.mods, l.stdout
	keepSyntax := opts.keepSyntax
	p := &program{bc: bc, fset: token.NewFileSet(), pkgs: map[string]*tPackage{}, byFile: map[string][]*tPackage{},
		sizes: types.SizesFor("gc", bc.goarch()), parsed: map[string]*parsedFile{}, tfiles: map[string]*token.File{},
		keepSyntax: keepSyntax}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var rec listRecord
		if err := dec.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: unreadable output: %w", err)
		}
		if strings.HasSuffix(rec.ImportPath, ".test") && rec.Name == "main" && rec.ForTest == "" {
			continue // the generated test binary's main package
		}
		tp := &tPackage{ID: rec.ImportPath, PkgPath: plainPath(rec.ImportPath), Name: rec.Name, Dir: rec.Dir,
			ForTest: rec.ForTest, Standard: rec.Standard, Imports: map[string]string{}}
		if rec.Error != nil {
			tp.listErr = rec.Error.Err
		}
		if rec.Module != nil {
			tp.GoVersion = rec.Module.GoVersion
		}
		names := rec.CompiledGoFiles
		if len(names) == 0 { // not compiled (a list error): fall back to the source lists
			names = append(append([]string{}, rec.GoFiles...), rec.CgoFiles...)
		}
		for _, n := range names {
			if !strings.HasSuffix(n, ".go") {
				continue
			}
			if !filepath.IsAbs(n) {
				n = filepath.Join(rec.Dir, n)
			}
			tp.Files = append(tp.Files, n)
		}
		for _, imp := range rec.Imports {
			tp.deps = append(tp.deps, imp)
			tp.Imports[plainPath(imp)] = imp
		}
		for from, to := range rec.ImportMap {
			tp.Imports[from] = to
		}
		if !rec.Standard && rec.Dir != "" {
			for _, m := range mods {
				if inDir(m, rec.Dir) {
					tp.Workspace = true
					break
				}
			}
		}
		p.pkgs[tp.ID] = tp
		p.order = append(p.order, tp)
	}
	for _, tp := range p.order {
		for _, d := range tp.deps {
			if dp := p.pkgs[d]; dp != nil {
				dp.importedBy = append(dp.importedBy, tp)
			}
		}
		for _, f := range tp.Files {
			p.byFile[f] = append(p.byFile[f], tp)
			pf := p.parsed[f]
			if pf == nil {
				pf = &parsedFile{}
				p.parsed[f] = pf
			}
			pf.users.Add(1)
		}
	}
	if cycle := importCycle(p); cycle != "" {
		// Each package is checked once and waits for what it imports: a cycle would wait for itself.
		return nil, &cycleError{cycle}
	}
	p.listSeconds = l.seconds
	p.mods = mods
	p.overlay, p.forceBodies, p.bodyNames, p.leanNames, p.read = opts.overlay, opts.forceBodies, opts.bodyNames, opts.leanNames, opts.read
	t1 := nowSeconds()
	p.checkAll()
	p.checkSeconds = nowSeconds() - t1
	return p, nil
}

// cycleError is a listing whose packages import each other in a circle: nothing can be type-checked.
type cycleError struct{ cycle string }

func (e *cycleError) Error() string { return "import cycle not allowed: " + e.cycle }

// importCycle returns one import cycle among the program's packages ("a -> b -> a"), or "".
func importCycle(p *program) string {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var stack []string
	var visit func(id string) string
	visit = func(id string) string {
		switch state[id] {
		case done:
			return ""
		case visiting:
			for i, s := range stack {
				if s == id {
					return strings.Join(append(append([]string{}, stack[i:]...), id), " -> ")
				}
			}
			return id + " -> " + id
		}
		state[id] = visiting
		stack = append(stack, id)
		if tp := p.pkgs[id]; tp != nil {
			for _, d := range tp.deps {
				if c := visit(d); c != "" {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		return ""
	}
	for _, tp := range p.order {
		if c := visit(tp.ID); c != "" {
			return c
		}
	}
	return ""
}

// parse returns the syntax of file, parsed once however many packages compile it.
func (p *program) parse(file string) *ast.File {
	p.parseMu.Lock()
	pf := p.parsed[file]
	if pf == nil {
		pf = &parsedFile{}
		p.parsed[file] = pf
	}
	p.parseMu.Unlock()
	pf.once.Do(func() {
		src, ok := p.overlay[file]
		if !ok {
			read := p.read
			if read == nil {
				read = os.ReadFile
			}
			var err error
			if src, err = read(file); err != nil {
				if !pf.carried {
					pf.metaOK = true
				}
				return
			}
		}
		mode := parser.SkipObjectResolution
		// A program that checks only the bodies spelling certain names does not parse the others, where it knows
		// from the program it was refreshed from where they are.
		var blanked map[int]bool
		if p.bodyNames != nil && pf.bodies != nil {
			src, blanked = blankBodies(src, pf.bodies, p.bodyNames)
		}
		fset := p.fset
		if pf.base > 0 {
			// An unchanged file of a refreshed program: parse it in a scratch file set at its old base. The
			// positions in the syntax are then the old ones, which the program's file set still resolves.
			if pf.tfile == nil || pf.tfile.Size() != len(src) {
				p.refreshBroken.Store(true) // not the file it was: the refresh is abandoned
				if !pf.carried {
					pf.metaOK = true
				}
				return
			}
			fset = token.NewFileSet()
			if pf.base > 1 {
				fset.AddFile("", 1, pf.base-2)
			}
		}
		// A file with syntax errors still yields what parsed before them.
		pf.file, _ = parser.ParseFile(fset, file, src, mode)
		if !pf.carried {
			pf.parsedOK, pf.hasMethod, pf.universe, pf.header = false, false, false, headerSig(src, pf.file)
		}
		if pf.file != nil {
			if !pf.carried {
				pf.parsedOK = true
				pf.hasMethod, pf.universe = declares(pf.file)
			}
			switch {
			case blanked != nil:
				base := int(pf.file.FileStart)
				for _, d := range pf.file.Decls {
					if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && blanked[int(fd.Body.Lbrace)-base] {
						fd.Body = nil
					}
				}
			case p.bodyNames != nil:
				stripBodies(pf.file, src, p.bodyNames)
			case !pf.carried || pf.bodies == nil:
				pf.bodies = bodyExtents(pf.file)
			}
			if pf.base == 0 {
				pf.tfile = p.fset.File(pf.file.Pos())
				p.parseMu.Lock()
				p.tfiles[file] = pf.tfile
				if p.overlay != nil {
					p.added = append(p.added, pf.tfile)
				}
				p.parseMu.Unlock()
			} else if fset.File(pf.file.Pos()).Base() != pf.base {
				p.refreshBroken.Store(true)
			}
		}
		if !pf.carried {
			pf.metaOK = true
		}
	})
	return pf.file
}

// declares reports whether a file declares a method, and whether it declares at package level a name the universe
// scope also has.
func declares(f *ast.File) (method, universe bool) {
	inUniverse := func(id *ast.Ident) bool { return id != nil && types.Universe.Lookup(id.Name) != nil }
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				method = true
			} else if inUniverse(d.Name) {
				universe = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					universe = universe || inUniverse(spec.Name)
				case *ast.ValueSpec:
					for _, n := range spec.Names {
						universe = universe || inUniverse(n)
					}
				}
			}
		}
	}
	return method, universe
}

// stripBodies removes from f, parsed from src, the body of every function that does not spell one of names.
//
// It is how a patch that renames a type, a function, a variable or a constant is checked without type-checking
// everything again (tverify.go). Such a rename changes what an identifier means only where one is spelled with the
// old or the new name: scopes hold names, and a selector or a composite literal names its member. A body that
// spells neither means after the patch what it meant before, so it is not checked again, and neither side of the
// comparison reads it. (A field or a method is another matter: struct types are compared and converted by their
// fields' names, and interfaces satisfied by methods' names, in code that names neither. Their patches are checked
// with every body.) A name is looked for as text, so a body that only mentions one in a comment or as part of a
// longer word is kept too: more than needed, never less.
func stripBodies(f *ast.File, src []byte, names [][]byte) {
	hits := nameHits(src, names)
	base := int(f.FileStart)
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		lo, hi := int(fd.Body.Pos())-base, int(fd.Body.End())-base
		if i := sort.SearchInts(hits, lo); i == len(hits) || hits[i] >= hi {
			fd.Body = nil
		}
	}
}

// nameHits returns, sorted, every offset of src at which one of names starts.
func nameHits(src []byte, names [][]byte) []int {
	var hits []int
	for _, name := range names {
		for at := 0; len(name) > 0; {
			i := bytes.Index(src[at:], name)
			if i < 0 {
				break
			}
			hits = append(hits, at+i)
			at += i + len(name)
		}
	}
	sort.Ints(hits)
	return hits
}

// bodyExtents returns where each function body of f is: the offsets of its braces, in pairs.
func bodyExtents(f *ast.File) []int32 {
	base := int(f.FileStart)
	out := []int32{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && fd.Body.Rbrace.IsValid() {
			out = append(out, int32(int(fd.Body.Lbrace)-base), int32(int(fd.Body.Rbrace)-base))
		}
	}
	return out
}

// blankBodies returns a copy of src in which every function body that does not spell one of names is emptied (its
// text turned to spaces, the line breaks kept, so that every offset and line outside it stands), and the set of the
// opening-brace offsets of the bodies it emptied. It is stripBodies done before parsing, where the bodies' places
// are known: the parser then spends nothing on them. extents (see parsedFile.bodies) must be of this very source;
// if a brace is not where they say, nothing is blanked.
func blankBodies(src []byte, extents []int32, names [][]byte) ([]byte, map[int]bool) {
	hits := nameHits(src, names)
	blanked := map[int]bool{}
	var out []byte
	for i := 0; i+1 < len(extents); i += 2 {
		lo, hi := int(extents[i]), int(extents[i+1])
		if lo < 0 || hi >= len(src) || lo >= hi || src[lo] != '{' || src[hi] != '}' {
			return src, nil
		}
		if j := sort.SearchInts(hits, lo); j < len(hits) && hits[j] < hi {
			continue // it spells one of the names: checked
		}
		if out == nil {
			out = bytes.Clone(src)
		}
		for k := lo + 1; k < hi; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
		blanked[lo] = true
	}
	if out == nil {
		return src, blanked
	}
	return out, blanked
}

// reparse parses src, the unchanged source of a file the program's file set already holds as tf, so that every
// position in the syntax is the position it has in the program. It returns nil when src is not the size the file
// was, or does not parse.
func reparse(tf *token.File, file string, src []byte, mode parser.Mode) *ast.File {
	if tf == nil || tf.Size() != len(src) {
		return nil
	}
	fset := token.NewFileSet()
	if base := tf.Base(); base > 1 {
		fset.AddFile("", 1, base-2)
	}
	f, _ := parser.ParseFile(fset, file, src, mode)
	if f == nil || fset.File(f.Pos()).Base() != tf.Base() {
		return nil
	}
	return f
}

// parseAll parses files, several at a time when there are many, and returns the ones that parsed, in order.
func (p *program) parseAll(names []string) []*ast.File {
	parsed := make([]*ast.File, len(names))
	if len(names) < 8 {
		for i, f := range names {
			parsed[i] = p.parse(f)
		}
	} else {
		var wg sync.WaitGroup
		sem := make(chan struct{}, runtime.GOMAXPROCS(0))
		for i, f := range names {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				parsed[i] = p.parse(f)
				<-sem
			}()
		}
		wg.Wait()
	}
	out := parsed[:0]
	for _, sf := range parsed {
		if sf != nil {
			out = append(out, sf)
		}
	}
	return out
}

// release says one package that compiles file has been checked; the syntax is dropped when none is left.
func (p *program) release(file string) {
	if p.keepSyntax {
		return
	}
	p.parseMu.Lock()
	pf := p.parsed[file]
	p.parseMu.Unlock()
	if pf != nil && pf.users.Add(-1) <= 0 {
		pf.file = nil
	}
}

type tImporter struct {
	p    *program
	from *tPackage
}

func (im tImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	id, ok := im.from.Imports[path]
	if !ok {
		id = path
	}
	dep := im.p.pkgs[id]
	if dep == nil {
		return nil, fmt.Errorf("package %s is not among the listed dependencies of %s", path, im.from.ID)
	}
	im.p.check(dep)
	if dep.types == nil {
		return nil, fmt.Errorf("package %s did not type-check", id)
	}
	return dep.types, nil
}

// testVariant returns the "p [p.test]" variant of plain package tp, or nil.
func (p *program) testVariant(tp *tPackage) *tPackage {
	if tp.ForTest != "" {
		return nil
	}
	return p.pkgs[tp.ID+" ["+tp.ID+".test]"]
}

// needsBodies reports whether tp is checked with function bodies.
//
// A searched package is, with one saving. A plain package p that has a test variant "p [p.test]" is searched by gopls
// twice, once as each, and the answers are united. The variant holds every file of p and imports what p imports, so
// each identifier of p's files means in the variant what it means in p, and everything the search computes from p's
// types (which types have which methods) only grows in the variant. The union is then the variant's answer alone, and
// p is checked for its declarations only. The exception is a test file that declares a method: a method added to a
// type can change what a selector in a non-test file resolves to (it can shadow a promoted method or field), so the
// two answers can differ and p is searched too. And a test file that declares, at package level, a name the universe
// scope has (a function called min): an identifier of a non-test file then means the builtin in p and that
// declaration in the variant. No search asks about a builtin, but a rename of that declaration edits the non-test
// file, and only p's own check shows what that does to p (ADR-062).
func (p *program) needsBodies(tp *tPackage, files []*ast.File) bool {
	if !tp.full() {
		return false
	}
	if p.forceBodies[tp.ID] {
		return true
	}
	tv := p.testVariant(tp)
	if tv == nil {
		return true
	}
	own := map[string]bool{}
	for _, f := range tp.Files {
		own[f] = true
	}
	for _, f := range tv.Files {
		if own[f] {
			continue
		}
		p.parseMu.Lock()
		pf := p.parsed[f]
		p.parseMu.Unlock()
		if pf == nil || !pf.carried {
			// (parse runs once, and returning from it is what makes its findings safe to read here)
			p.parse(f)
			p.parseMu.Lock()
			pf = p.parsed[f]
			p.parseMu.Unlock()
		}
		if pf == nil || !pf.parsedOK || pf.hasMethod || pf.universe {
			return true // a method or a universe name in a test file, or a file that did not parse: search both
		}
	}
	return false
}

// check type-checks tp once: with function bodies when it is searched, declarations only otherwise.
func (p *program) check(tp *tPackage) {
	tp.once.Do(func() {
		t0 := nowSeconds()
		files := p.parseAll(tp.Files)
		tp.parseSeconds = nowSeconds() - t0
		tp.bodies = p.needsBodies(tp, files)
		conf := types.Config{
			Importer:         tImporter{p, tp},
			Sizes:            p.sizes,
			IgnoreFuncBodies: !tp.bodies,
			FakeImportC:      true,
			Error: func(err error) {
				te, ok := err.(types.Error)
				if ok && te.Soft && !strings.Contains(te.Msg, "satisfy") {
					// An error the checker calls soft leaves the types it computed standing: an unused variable or
					// import, a label nobody jumps to. Those are left out, as before. One kind is not: a type
					// argument that does not satisfy its type parameter's constraint ("T does not satisfy C") is
					// soft too, and is exactly what a method's rename can newly cause, in a place that names
					// neither the method nor the interface.
					tp.soft = append(tp.soft, typeErr{te.Pos, te.Msg})
					return
				}
				tp.nerr++
				if ok {
					tp.errs = append(tp.errs, typeErr{te.Pos, te.Msg})
				}
			},
		}
		if tp.GoVersion != "" {
			conf.GoVersion = "go" + tp.GoVersion
		}
		var info *types.Info
		if tp.full() {
			info = &types.Info{Defs: map[*ast.Ident]types.Object{}}
			if tp.bodies {
				info.Uses = map[*ast.Ident]types.Object{}
			}
		}
		t1 := nowSeconds()
		tp.types, _ = conf.Check(tp.PkgPath, p.fset, files, info)
		if info != nil {
			tp.tabulate(info, files, p.leanNames)
		}
		tp.checkSeconds = nowSeconds() - t1
		if p.keepSyntax {
			tp.syntax = files
		}
		for _, f := range tp.Files {
			p.release(f)
		}
	})
}

// tabulate keeps what the searches read from a checked package, so its syntax and types.Info can go.
// lean, when not nil, keeps only what the check of a patch reads: the uses of those names, and the type declarations.
func (tp *tPackage) tabulate(info *types.Info, files []*ast.File, lean map[string]bool) {
	if lean == nil {
		tp.defs = make(map[token.Pos]types.Object, len(info.Defs))
		for id, obj := range info.Defs {
			if obj != nil {
				tp.defs[id.Pos()] = obj
			}
		}
	}
	if info.Uses == nil {
		return
	}
	tp.uses = map[string][]useRec{}
	for id, obj := range info.Uses {
		if lean != nil {
			if lean[id.Name] {
				tp.uses[id.Name] = append(tp.uses[id.Name], useRec{id.Pos(), obj})
			}
			continue
		}
		tp.uses[id.Name] = append(tp.uses[id.Name], useRec{id.Pos(), obj})
		if _, declares := tp.defs[id.Pos()]; declares {
			if tp.useAtDecl == nil {
				tp.useAtDecl = map[token.Pos]types.Object{}
			}
			tp.useAtDecl[id.Pos()] = obj
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if spec, ok := n.(*ast.TypeSpec); ok {
				if def, ok := info.Defs[spec.Name].(*types.TypeName); ok {
					tp.typeSpecs = append(tp.typeSpecs, typeSpecRec{spec.Name.Pos(), def})
				}
			}
			return true
		})
	}
}

// checkAll type-checks every package, on as many goroutines as there are processors. Each package is checked once;
// a package that needs a dependency another goroutine is checking waits for it.
func (p *program) checkAll() {
	n := runtime.GOMAXPROCS(0)
	ch := make(chan *tPackage)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tp := range ch {
				p.check(tp)
			}
		}()
	}
	for _, tp := range p.order {
		ch <- tp
	}
	close(ch)
	wg.Wait()
}

// variants returns the packages that compile file, narrowest first (fewest files: the plain package before its test
// variant), an intermediate test variant after the package it copies.
func (p *program) variants(file string, withITV bool) []*tPackage {
	var out []*tPackage
	for _, tp := range p.byFile[file] {
		if withITV || !tp.itv() {
			out = append(out, tp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Files) != len(out[j].Files) {
			return len(out[i].Files) < len(out[j].Files)
		}
		if withITV && out[i].itv() != out[j].itv() {
			return !out[i].itv()
		}
		return false
	})
	return out
}

// posAt returns the position of line:col (1-based, bytes) of file, or token.NoPos.
func (p *program) posAt(file string, line, col int) token.Pos {
	p.parseMu.Lock()
	tf := p.tfiles[file]
	p.parseMu.Unlock()
	if tf == nil || line < 1 || line > tf.LineCount() || col < 1 {
		return token.NoPos
	}
	off := tf.Offset(tf.LineStart(line)) + col - 1
	if off > tf.Size() {
		return token.NoPos
	}
	return tf.Pos(off)
}

// workspacePackages returns the packages that are searched: workspace packages that are not intermediate test
// variants.
func (p *program) workspacePackages() []*tPackage {
	var out []*tPackage
	for _, tp := range p.order {
		if tp.full() {
			out = append(out, tp)
		}
	}
	return out
}

// rdeps returns the packages that import tp, directly or (transitive) through any chain, tp itself excluded. The
// result is shared: callers do not modify it.
func (p *program) rdeps(tp *tPackage, transitive bool) map[*tPackage]bool {
	k := rdepsKey{tp, transitive}
	p.rdepsMu.Lock()
	defer p.rdepsMu.Unlock()
	if m, ok := p.rdepsMemo[k]; ok {
		return m
	}
	out := map[*tPackage]bool{}
	if !transitive {
		for _, q := range tp.importedBy {
			out[q] = true
		}
	} else {
		var walk func(q *tPackage)
		walk = func(q *tPackage) {
			for _, r := range q.importedBy {
				if !out[r] {
					out[r] = true
					walk(r)
				}
			}
		}
		walk(tp)
		delete(out, tp)
	}
	if p.rdepsMemo == nil {
		p.rdepsMemo = map[rdepsKey]map[*tPackage]bool{}
	}
	p.rdepsMemo[k] = out
	return out
}

// headerSig summarizes what the go command's list of a file's package depends on in the file itself: its package
// clause, its build constraints, and what it imports. Two versions of a file with the same signature are listed the
// same way, so an edit that keeps it leaves `go list` unchanged.
func headerSig(src []byte, f *ast.File) uint64 {
	h := fnv.New64a()
	put := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	// constraint lines sit before the package clause
	rest := src
	for len(rest) > 0 {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			rest = nil
		}
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("//go:build")) || bytes.HasPrefix(line, []byte("// +build")) || bytes.HasPrefix(line, []byte("//+build")) {
			put(string(line))
		}
		if bytes.HasPrefix(line, []byte("package ")) {
			break
		}
	}
	if f == nil {
		put("unparsed")
		return h.Sum64()
	}
	if f.Name != nil {
		put("package " + f.Name.Name)
	}
	var imps []string
	for _, im := range f.Imports {
		if im.Path != nil {
			imps = append(imps, im.Path.Value)
		}
	}
	sort.Strings(imps)
	for _, im := range imps {
		put(im)
	}
	return h.Sum64()
}
