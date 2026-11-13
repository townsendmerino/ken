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
	Run     RunFunc       // nil runs the subprocess
	Environ []string      // base environment for gopls and go; nil means the process's
	Timeout time.Duration // per gopls or go run; 0 means 5 minutes
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

// each runs one gopls subcommand at every declaration under every configuration that compiles it.
func (r *Runner) each(ctx context.Context, decls []Decl, sub []string, extra []string,
	got func(bc BuildConfig, d Decl, stdout []byte) int) (runs []ConfigRun, incomplete bool) {
	for _, d := range decls {
		where := d.File + ":" + strconv.Itoa(d.Line)
		asked := false
		for _, bc := range r.Config.Configs {
			if !bc.Compiles(filepath.Base(d.File), d.Constraint) {
				runs = append(runs, ConfigRun{Config: bc.Name, Decl: where, Skipped: "does not compile this file"})
				continue
			}
			asked = true
			pos := fmt.Sprintf("%s:%d:%d", filepath.Join(r.Repo, filepath.FromSlash(d.File)), d.Line, d.Col)
			args := append(append(append([]string{}, sub...), pos), extra...)
			t := time.Now()
			stdout, stderr, err := r.run(ctx, bc, args...)
			cr := ConfigRun{Config: bc.Name, Decl: where, Seconds: float64(time.Since(t).Milliseconds()) / 1000}
			if err != nil {
				msg := strings.TrimSpace(string(stderr))
				if msg == "" {
					msg = err.Error()
				}
				if len(msg) > 300 {
					msg = msg[:300]
				}
				cr.Err, incomplete = msg, true
			} else {
				cr.Hits = got(bc, d, stdout)
			}
			runs = append(runs, cr)
		}
		if !asked {
			incomplete = true
		}
	}
	return runs, incomplete
}

func (r *Runner) locations(ctx context.Context, symbol, query string, sub []string) (*Result, error) {
	decls, err := r.resolve(symbol)
	if err != nil {
		return nil, err
	}
	type key struct {
		file string
		line int
	}
	seen := map[key]map[string]bool{}
	runs, incomplete := r.each(ctx, decls, sub, nil, func(bc BuildConfig, _ Decl, stdout []byte) int {
		n := 0
		for _, ln := range strings.Split(string(stdout), "\n") {
			m := locRE.FindStringSubmatch(strings.TrimSpace(ln))
			if m == nil {
				continue
			}
			rel, err := filepath.Rel(r.Repo, m[1])
			if err != nil || strings.HasPrefix(rel, "..") {
				continue // a dependency outside the repository
			}
			line, _ := strconv.Atoi(m[2])
			k := key{filepath.ToSlash(rel), line}
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
	return r.locations(ctx, symbol, "references", []string{"references"})
}

// Implementations returns, for an interface or interface method, the types or methods that implement it; for a
// concrete type or method, the interfaces it satisfies.
func (r *Runner) Implementations(ctx context.Context, symbol string) (*Result, error) {
	return r.locations(ctx, symbol, "implementations", []string{"implementation"})
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
