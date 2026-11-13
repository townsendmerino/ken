package gotypes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RunFunc runs argv in dir with env and returns its output. A test substitutes it for the real subprocess.
type RunFunc func(ctx context.Context, dir string, env, argv []string) (stdout, stderr []byte, err error)

// Runner answers queries about one repository.
type Runner struct {
	Repo    string        // absolute path of the repository root
	Config  Config        // from LoadConfig
	Gopls   string        // the gopls binary; "" means "gopls" on PATH
	Go      string        // the go binary, which Uses runs; "" means "go" on PATH
	Run     RunFunc       // nil runs the subprocess; a non-nil Run stands in for the gopls command line (see Engine)
	Environ []string      // base environment for gopls and go; nil means the process's
	Timeout time.Duration // per gopls or go run; 0 means 5 minutes

	// Engine chooses what answers references and implementations: EngineTypes (the default, also "") answers in this
	// process with go/types, following gopls's own search rules (ADR-061); EngineLSP asks gopls over the
	// language-server protocol (ADR-060); EngineCLI runs the gopls command line, one process per question, as ADR-059
	// did. A rename plan is always gopls's: over LSP, or the command line under EngineCLI. A Runner with Run set
	// takes the command-line route, since Run replaces the command-line subprocess.
	Engine string
	// GoplsExact makes EngineTypes return exactly the lines gopls returns. By default it returns those and the uses
	// gopls misses that are plainly uses of the same declaration: a field of a generic type reached through an
	// instantiation in another package (gopls's own rename edits those lines; its references omit them). ADR-061.
	GoplsExact bool
	// Spawn starts a language server process; nil runs the real one. Tests substitute it.
	Spawn SpawnFunc
	// Pool, when non-nil, keeps language servers alive between queries. Nil starts and stops one per query.
	Pool *Pool
	// Cache, when non-nil, remembers answers while the tree is unchanged. Nil remembers nothing.
	Cache *Cache
	// Programs, when non-nil, keeps the programs EngineTypes type-checks between queries. Nil loads one per
	// configuration per query and lets it go.
	Programs *ProgramCache
	// Parallel is how many configurations EngineTypes type-checks at once. 0 or 1 is one at a time, which keeps one
	// program in memory; more is faster and holds that many.
	Parallel int
}

// realRepo is the repository root with symlinks resolved, which is how gopls names the files it reports.
func (r *Runner) realRepo() string {
	if real, err := filepath.EvalSymlinks(r.Repo); err == nil {
		return real
	}
	return r.Repo
}

// rel returns abs relative to the repository, slash-separated, and false for a path outside it. abs may be spelled
// through the repository path as given or through its symlink-resolved form.
func (r *Runner) rel(abs string) (string, bool) {
	for _, root := range []string{r.Repo, r.realRepo()} {
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// The values of Runner.Engine.
const (
	EngineTypes = "types"
	EngineLSP   = "lsp"
	EngineCLI   = "cli"
)

// Engines lists the values Runner.Engine takes, the default first.
var Engines = []string{EngineTypes, EngineLSP, EngineCLI}

// useLSP reports whether gopls is asked over LSP rather than through its command line.
func (r *Runner) useLSP() bool { return r.Engine != EngineCLI && r.Run == nil }

// useTypes reports whether kind is answered in this process.
func (r *Runner) useTypes(kind reqKind) bool {
	return (r.Engine == EngineTypes || r.Engine == "") && r.Run == nil && kind != askRename
}

// ConfigRun records one gopls run, or why a configuration was not asked.
type ConfigRun struct {
	Config  string  `json:"config"`
	Decl    string  `json:"decl"` // file:line of the declaration asked about
	Hits    int     `json:"hits"`
	Seconds float64 `json:"seconds,omitempty"`
	Skipped string  `json:"skipped,omitempty"` // set when the configuration does not compile the declaration's file
	Err     string  `json:"error,omitempty"`   // gopls's complaint, when it failed
	// Partial is set by Uses when the configuration's files did not type-check cleanly: Hits counts what resolved.
	Partial string `json:"partial,omitempty"`
}

// Hit is one source line a query found.
type Hit struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	In      string   `json:"in,omitempty"` // enclosing function or method
	Text    string   `json:"text,omitempty"`
	Configs []string `json:"configs"` // every configuration that reported this line
	// Through is set on a references hit that does not name the queried declaration itself: it names the method
	// given here (pkg.Type.Method), which corresponds to the queried one through an interface. For a concrete method
	// that is a call through an interface it satisfies; for an interface method, a call on a type that implements it
	// or through an interface around it. Several names are joined with ", ". Empty on a direct use, and on every hit
	// of a result that is not Labelled.
	Through string `json:"through,omitempty"`

	pkg string // the file's package clause, for GroupBy
}

// Result is the merged answer of a references or implementations query.
type Result struct {
	Symbol string      `json:"symbol"`
	Query  string      `json:"query"`
	Decls  []Decl      `json:"declarations"`
	Hits   []Hit       `json:"hits"`
	Total  int         `json:"total"` // lines found; Hits may be cut shorter by a caller's cap
	Files  int         `json:"files"`
	Runs   []ConfigRun `json:"runs"`
	// Incomplete is true when a declaration compiles under no listed configuration or a gopls run failed: the hits
	// are then a lower bound, and Runs says which part is missing.
	Incomplete bool `json:"incomplete"`
	// Labelled is true when the references hits tell direct uses from uses through a corresponding method
	// (Hit.Through); Direct then counts the lines that name the declaration itself, and Total-Direct name another
	// method. The in-process engine labels; gopls's answers (the lsp and cli engines) cannot be, and neither can
	// implementations.
	Labelled bool `json:"labelled,omitempty"`
	Direct   int  `json:"direct,omitempty"`
	// DirectOnlyApplied is set by DirectOnly: the hits through other methods were dropped, and Total and Files count
	// what is left.
	DirectOnlyApplied bool `json:"direct_only,omitempty"`
	// GroupedBy and Groups are set by GroupBy, which replaces Hits with counts.
	GroupedBy string  `json:"group_by,omitempty"`
	Groups    []Group `json:"groups,omitempty"`
	// Cached is true when the answer was remembered from an earlier identical query on a tree that has not changed
	// since; CacheAgeSeconds says how long ago that query ran. Runs then describes the original run.
	Cached          bool    `json:"cached,omitempty"`
	CacheAgeSeconds float64 `json:"cache_age_seconds,omitempty"`
}

// ErrAmbiguous is returned when a symbol names declarations of more than one thing.
var ErrAmbiguous = errors.New("ambiguous symbol")

// ErrNotFound is returned when no declaration matches a symbol.
var ErrNotFound = errors.New("no declaration found")

func (r *Runner) run(ctx context.Context, bc BuildConfig, args ...string) ([]byte, []byte, error) {
	gopls := r.Gopls
	if gopls == "" {
		gopls = "gopls"
	}
	return r.exec(ctx, bc, gopls, args...)
}

// exec runs bin under one configuration's environment, from the repository root, behind the repository's wrap command.
func (r *Runner) exec(ctx context.Context, bc BuildConfig, bin string, args ...string) ([]byte, []byte, error) {
	argv := append(append([]string{}, r.Config.Wrap...), bin)
	argv = append(argv, args...)
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if r.Run != nil {
		return r.Run(ctx, r.Repo, bc.environ(base), argv)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = r.Repo, bc.environ(base)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s timed out after %s", filepath.Base(bin), timeout)
	}
	return out.Bytes(), errb.Bytes(), err
}

// resolve finds symbol's declarations and refuses a name that means more than one thing.
func (r *Runner) resolve(symbol string) ([]Decl, error) {
	decls, err := FindDecls(r.Repo, symbol)
	if err != nil {
		return nil, err
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("%w for %q (a method or field needs its type: Type.Name)", ErrNotFound, symbol)
	}
	if names := distinctQNames(decls); len(names) > 1 {
		return nil, fmt.Errorf("%w %q: it names %d things; qualify it with a package or directory: %s",
			ErrAmbiguous, symbol, len(names), strings.Join(names, "; "))
	}
	return decls, nil
}

var locRE = regexp.MustCompile(`^(.+\.go):(\d+):(\d+)`)

// reqKind is the question put to gopls.
type reqKind int

const (
	askReferences reqKind = iota
	askImplementations
	askRename
)

// cliArgs is the gopls command-line spelling of a question, minus its position.
func (k reqKind) cliArgs() []string {
	switch k {
	case askImplementations:
		return []string{"implementation"}
	case askRename:
		return []string{"rename", "-d"}
	}
	return []string{"references"}
}

func (k reqKind) lspMethod() string {
	switch k {
	case askImplementations:
		return "textDocument/implementation"
	case askRename:
		return "textDocument/rename"
	}
	return "textDocument/references"
}

// reply is gopls's answer to one question under one configuration.
type reply struct {
	text []byte // references, implementations: one "path:line:col" per line
	// labelled says the lines of text tell direct uses from uses of a corresponding method: a line that names
	// another method carries that method's name after a tab. Only the in-process engine can say.
	labelled bool
	edits    map[string][]string // rename over LSP: the new lines of each file the rename edits, by absolute path
	hunks    map[string][]string // rename over the command line: the diff hunk lines of each file, by absolute path
}

// files returns the absolute paths of the files a rename edits.
func (rp reply) files() []string {
	var out []string
	for p := range rp.edits {
		out = append(out, p)
	}
	for p := range rp.hunks {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// newLines returns file abs, whose current lines are orig, after the rename.
func (rp reply) newLines(abs string, orig []string) ([]string, error) {
	if h, ok := rp.hunks[abs]; ok {
		return applyHunks(orig, h)
	}
	return rp.edits[abs], nil
}

func errText(stderr []byte, err error) string {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// ask puts one question about the declaration at abs:line:col to gopls under bc.
func (r *Runner) ask(ctx context.Context, bc BuildConfig, kind reqKind, d Decl, newName string, shots map[string]*lspSession) (reply, error) {
	abs := filepath.Join(r.Repo, filepath.FromSlash(d.File))
	if r.useLSP() {
		abs = filepath.Join(r.realRepo(), filepath.FromSlash(d.File))
	}
	if !r.useLSP() {
		args := append(append([]string{}, kind.cliArgs()...), fmt.Sprintf("%s:%d:%d", abs, d.Line, d.Col))
		if kind == askRename {
			args = append(args, newName)
		}
		stdout, stderr, err := r.run(ctx, bc, args...)
		if err != nil {
			return reply{}, errors.New(errText(stderr, err))
		}
		if kind != askRename {
			return reply{text: stdout}, nil
		}
		return reply{hunks: splitDiff(string(stdout))}, nil
	}
	do := func(s *lspSession) (reply, error) {
		ctx, cancel := context.WithTimeout(ctx, r.timeout())
		defer cancel()
		if kind == askRename {
			edits, err := s.rename(ctx, abs, d.Line, d.Col, newName)
			return reply{edits: edits}, err
		}
		text, err := s.ask(ctx, kind.lspMethod(), abs, d.Line, d.Col)
		return reply{text: text}, err
	}
	// Servers are kept resident only for the engine that answers from them: a rename plan under the in-process
	// engine starts its gopls and stops it, and leaves nothing behind.
	if r.Pool != nil && r.Engine == EngineLSP {
		rp, err := r.askPooled(ctx, bc, do)
		if !errors.Is(err, errPoolFull) && !errors.Is(err, errNoLockHolder) {
			return rp, err
		}
		// every pooled server is busy, or the wrap lock cannot be held per request: run on a server of our own
	}
	s := shots[bc.Name]
	if s == nil {
		var err error
		if s, err = r.startSession(ctx, bc, true); err != nil {
			return reply{}, err
		}
		shots[bc.Name] = s
	}
	return do(s)
}

var errNoLockHolder = errors.New("gotypes: cannot hold the wrap lock per request")

// askPooled runs do on the pooled server for bc, started on first use. The wrap lock is held for the length of the
// request, including a start, and not while the server sits idle.
func (r *Runner) askPooled(ctx context.Context, bc BuildConfig, do func(*lspSession) (reply, error)) (reply, error) {
	for attempt := 0; ; attempt++ {
		release, err := r.holdLock(ctx)
		if errors.Is(err, errNoShell) {
			return reply{}, fmt.Errorf("%w: %v", errNoLockHolder, err)
		}
		if err != nil {
			return reply{}, err
		}
		rp, err := func() (reply, error) {
			defer release()
			pc, err := r.Pool.acquire(ctx, r, bc, func() (*lspSession, error) {
				before, _ := scanTree(r.realRepo()) // before the server reads anything, so no edit can fall between
				at := time.Now()
				s, err := r.startSession(ctx, bc, false)
				if err == nil {
					s.tree = &treeWatch{stats: before, at: at}
				}
				return s, err
			})
			if err != nil {
				return reply{}, err
			}
			defer r.Pool.release(pc)
			pc.s.reqMu.Lock()
			defer pc.s.reqMu.Unlock()
			if err := pc.s.syncTree(); err != nil {
				return reply{}, err
			}
			rp, err := do(pc.s)
			if errors.Is(err, errSessionClosed) {
				r.Pool.discard(pc)
			}
			return rp, err
		}()
		if errors.Is(err, errSessionClosed) && attempt == 0 {
			continue // the server died under us; one retry on a fresh one
		}
		return rp, err
	}
}

// each asks one question at every declaration under every configuration that compiles it.
func (r *Runner) each(ctx context.Context, decls []Decl, kind reqKind, newName string,
	got func(bc BuildConfig, d Decl, rp reply) int) (runs []ConfigRun, incomplete bool) {
	shots := map[string]*lspSession{}
	var typed [][]typedCell // [declaration][configuration], when answered in this process
	if r.useTypes(kind) {
		typed = r.askAllTyped(ctx, decls, kind)
	}
	defer func() {
		for _, s := range shots {
			s.close()
		}
	}()
	for di, d := range decls {
		where := d.File + ":" + strconv.Itoa(d.Line)
		asked := false
		for ci, bc := range r.Config.Configs {
			if !bc.Compiles(filepath.Base(d.File), d.Constraint) {
				runs = append(runs, ConfigRun{Config: bc.Name, Decl: where, Skipped: "does not compile this file"})
				continue
			}
			asked = true
			t := time.Now()
			var rp reply
			var err error
			var spent float64
			if typed != nil {
				rp, err, spent = typed[di][ci].rp, typed[di][ci].err, typed[di][ci].seconds
			} else {
				rp, err = r.ask(ctx, bc, kind, d, newName, shots)
			}
			cr := ConfigRun{Config: bc.Name, Decl: where}
			if err != nil {
				msg := err.Error()
				if len(msg) > 300 {
					msg = msg[:300]
				}
				cr.Err, incomplete = msg, true
			} else {
				cr.Hits = got(bc, d, rp)
			}
			cr.Seconds = float64(time.Since(t).Milliseconds())/1000 + float64(int(spent*1000))/1000
			runs = append(runs, cr)
		}
		if !asked {
			incomplete = true
		}
	}
	return runs, incomplete
}

func (r *Runner) locations(ctx context.Context, symbol, query string, kind reqKind) (*Result, error) {
	decls, err := r.resolve(symbol)
	if err != nil {
		return nil, err
	}
	type key struct {
		file string
		line int
	}
	seen := map[key]map[string]bool{}
	direct := map[key]bool{}             // the line names the declaration itself, under some configuration
	through := map[key]map[string]bool{} // the other methods the line names
	labelled, unlabelled := false, false
	runs, incomplete := r.each(ctx, decls, kind, "", func(bc BuildConfig, _ Decl, rp reply) int {
		if rp.labelled {
			labelled = true
		} else {
			unlabelled = true
		}
		n := 0
		for _, ln := range strings.Split(string(rp.text), "\n") {
			loc, via, _ := strings.Cut(ln, "\t")
			m := locRE.FindStringSubmatch(strings.TrimSpace(loc))
			if m == nil {
				continue
			}
			rel, ok := r.rel(m[1])
			if !ok {
				continue // a dependency outside the repository
			}
			line, _ := strconv.Atoi(m[2])
			k := key{rel, line}
			if seen[k] == nil {
				seen[k] = map[string]bool{}
			}
			if !seen[k][bc.Name] {
				seen[k][bc.Name] = true
				n++
			}
			if via == "" {
				direct[k] = true
			} else {
				if through[k] == nil {
					through[k] = map[string]bool{}
				}
				through[k][via] = true
			}
		}
		return n
	})
	res := &Result{Symbol: symbol, Query: query, Decls: decls, Runs: runs, Incomplete: incomplete}
	// every answer that came back was labelled: the result is
	res.Labelled = labelled && !unlabelled
	files := map[string]bool{}
	for k, cfgs := range seen {
		h := Hit{File: k.file, Line: k.line}
		for _, bc := range r.Config.Configs {
			if cfgs[bc.Name] {
				h.Configs = append(h.Configs, bc.Name)
			}
		}
		if res.Labelled {
			if direct[k] {
				res.Direct++
			} else {
				names := make([]string, 0, len(through[k]))
				for name := range through[k] {
					names = append(names, name)
				}
				sort.Strings(names)
				h.Through = strings.Join(names, ", ")
			}
		}
		res.Hits = append(res.Hits, h)
		files[k.file] = true
	}
	res.Files = len(files)
	sort.Slice(res.Hits, func(i, j int) bool {
		if res.Hits[i].File != res.Hits[j].File {
			return res.Hits[i].File < res.Hits[j].File
		}
		return res.Hits[i].Line < res.Hits[j].Line
	})
	res.Total = len(res.Hits)
	r.annotate(res.Hits)
	return res, nil
}

// References returns every line that refers to symbol under any configuration. For a method, gopls includes the
// calls made through an interface the method satisfies and the calls on each type that implements it.
func (r *Runner) References(ctx context.Context, symbol string) (*Result, error) {
	return cached(r, "references", []string{symbol}, func() (*Result, error) {
		return r.locations(ctx, symbol, "references", askReferences)
	})
}

// Implementations returns, for an interface or interface method, the types or methods that implement it; for a
// concrete type or method, the interfaces it satisfies.
func (r *Runner) Implementations(ctx context.Context, symbol string) (*Result, error) {
	return cached(r, "implementations", []string{symbol}, func() (*Result, error) {
		return r.locations(ctx, symbol, "implementations", askImplementations)
	})
}

// annotate fills each hit's source line and enclosing function. Hits are sorted by file; each file is read and
// parsed once, and the files are done on every processor (a wide answer touches a thousand of them).
func (r *Runner) annotate(hits []Hit) {
	type run struct{ lo, hi int } // hits[lo:hi] share a file
	var runs []run
	for i := 0; i < len(hits); {
		j := i
		for j < len(hits) && hits[j].File == hits[i].File {
			j++
		}
		runs = append(runs, run{i, j})
		i = j
	}
	var wg sync.WaitGroup
	next := make(chan run)
	for range min(runtime.GOMAXPROCS(0), max(len(runs), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rn := range next {
				r.annotateFile(hits[rn.lo:rn.hi])
			}
		}()
	}
	for _, rn := range runs {
		next <- rn
	}
	close(next)
	wg.Wait()
}

// annotateFile fills the hits of one file.
func (r *Runner) annotateFile(hits []Hit) {
	type span struct {
		start, end int
		name       string
	}
	file := hits[0].File
	src, err := os.ReadFile(filepath.Join(r.Repo, filepath.FromSlash(file)))
	if err != nil {
		return
	}
	lines := strings.Split(string(src), "\n")
	pkg := ""
	var funcs []span
	fset := token.NewFileSet()
	if f, _ := parser.ParseFile(fset, file, src, parser.SkipObjectResolution); f != nil {
		if f.Name != nil {
			pkg = f.Name.Name
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				name = recvName(fd.Recv.List[0].Type) + "." + name
			}
			funcs = append(funcs, span{fset.Position(fd.Pos()).Line, fset.Position(fd.End()).Line, name})
		}
	}
	for i := range hits {
		h := &hits[i]
		h.pkg = pkg
		if h.Line >= 1 && h.Line <= len(lines) {
			t := strings.TrimSpace(lines[h.Line-1])
			if len(t) > 140 {
				t = t[:140] + "…"
			}
			h.Text = t
		}
		for _, s := range funcs {
			if h.Line >= s.start && h.Line <= s.end {
				h.In = s.name
				break
			}
		}
	}
}
