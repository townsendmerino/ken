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
	"sort"
	"strconv"
	"strings"
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
	Run     RunFunc       // nil runs the subprocess; a non-nil Run stands in for the gopls command line (see UseCLI)
	Environ []string      // base environment for gopls and go; nil means the process's
	Timeout time.Duration // per gopls or go run; 0 means 5 minutes

	// UseCLI asks gopls through its command line, one process per question, as ADR-059 did. The default asks over the
	// language-server protocol (ADR-060): the same server code, so the same answers, in a fraction of the time. A
	// Runner with Run set also takes the command-line route, since Run replaces the command-line subprocess.
	UseCLI bool
	// Spawn starts a language server process; nil runs the real one. Tests substitute it.
	Spawn SpawnFunc
	// Pool, when non-nil, keeps language servers alive between queries. Nil starts and stops one per query.
	Pool *Pool
	// Cache, when non-nil, remembers answers while the tree is unchanged. Nil remembers nothing.
	Cache *Cache
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

// useLSP reports whether questions go to a language server rather than the gopls command line.
func (r *Runner) useLSP() bool { return !r.UseCLI && r.Run == nil }

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
	text  []byte              // references, implementations: one "path:line:col" per line
	edits map[string][]string // rename over LSP: the new lines of each file the rename edits, by absolute path
	hunks map[string][]string // rename over the command line: the diff hunk lines of each file, by absolute path
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
	if r.Pool != nil {
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
		if err != nil {
			return reply{}, fmt.Errorf("%w: %v", errNoLockHolder, err)
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
	defer func() {
		for _, s := range shots {
			s.close()
		}
	}()
	for _, d := range decls {
		where := d.File + ":" + strconv.Itoa(d.Line)
		asked := false
		for _, bc := range r.Config.Configs {
			if !bc.Compiles(filepath.Base(d.File), d.Constraint) {
				runs = append(runs, ConfigRun{Config: bc.Name, Decl: where, Skipped: "does not compile this file"})
				continue
			}
			asked = true
			t := time.Now()
			rp, err := r.ask(ctx, bc, kind, d, newName, shots)
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
			cr.Seconds = float64(time.Since(t).Milliseconds()) / 1000
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
	runs, incomplete := r.each(ctx, decls, kind, "", func(bc BuildConfig, _ Decl, rp reply) int {
		stdout := rp.text
		n := 0
		for _, ln := range strings.Split(string(stdout), "\n") {
			m := locRE.FindStringSubmatch(strings.TrimSpace(ln))
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
		}
		return n
	})
	res := &Result{Symbol: symbol, Query: query, Decls: decls, Runs: runs, Incomplete: incomplete}
	files := map[string]bool{}
	for k, cfgs := range seen {
		h := Hit{File: k.file, Line: k.line}
		for _, bc := range r.Config.Configs {
			if cfgs[bc.Name] {
				h.Configs = append(h.Configs, bc.Name)
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

// annotate fills each hit's source line and enclosing function, reading every file once.
func (r *Runner) annotate(hits []Hit) {
	type span struct {
		start, end int
		name       string
	}
	var (
		cur   string
		pkg   string
		lines []string
		funcs []span
	)
	for i := range hits {
		h := &hits[i]
		if h.File != cur {
			cur, pkg, lines, funcs = h.File, "", nil, nil
			src, err := os.ReadFile(filepath.Join(r.Repo, filepath.FromSlash(h.File)))
			if err != nil {
				continue
			}
			lines = strings.Split(string(src), "\n")
			fset := token.NewFileSet()
			if f, _ := parser.ParseFile(fset, h.File, src, parser.SkipObjectResolution); f != nil {
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
		}
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
