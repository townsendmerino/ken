// Package tstypes is the typed tier for TypeScript and JavaScript (ADR-064). It asks the project's own compiler, the
// native TypeScript 7 program in the project's node_modules, over the language-server protocol (internal/lsp). ken
// links no TypeScript code and runs no Node.
//
// What it answers today is one question: what would an edit do to the build (Runner.CheckEdit).
package tstypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/lsp"
	"github.com/townsendmerino/ken/internal/repo"
)

// Runner checks edits against one repository.
type Runner struct {
	// Repo is the repository root, an absolute path.
	Repo string
	// Tsc is the native TypeScript compiler to run. "" finds the one in the node_modules of the edited file's
	// project (FindCompiler).
	Tsc string
	// Timeout bounds one check. 0 means two minutes.
	Timeout time.Duration
	// MaxFiles is the most source files one check will ask the compiler about. 0 means 20,000. A repository with
	// more is not checked: an answer from part of it would read as complete.
	MaxFiles int
	// Parallel is how many files are asked about at once. 0 means 8.
	Parallel int
	// Spawn starts the server; nil runs the real program. Environ is its environment; nil means the process's own.
	Spawn   lsp.SpawnFunc
	Environ []string
}

// ErrNoCompiler is what errors.Is matches when the repository has no TypeScript 7 compiler to ask.
var ErrNoCompiler = errors.New("no TypeScript 7 compiler")

// sourceExts are the files the compiler checks, with LSP's name for the language of each.
var sourceExts = map[string]string{
	".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "typescriptreact",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascriptreact",
}

// IsSource reports whether name is a file the TypeScript compiler checks.
func IsSource(name string) bool { return sourceExts[strings.ToLower(filepath.Ext(name))] != "" }

// platformPackage is the npm package that holds the native compiler for this machine, as the typescript package
// names its optional dependencies.
func platformPackage() string {
	goos, arch := runtime.GOOS, runtime.GOARCH
	if goos == "windows" {
		goos = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	return "typescript-" + goos + "-" + arch
}

// FindCompiler returns the native TypeScript compiler installed for the project that holds dir: the nearest
// node_modules at or above dir, no higher than repo, that has one. The error says what was found instead (an older
// TypeScript, or none) and matches ErrNoCompiler.
func FindCompiler(repo, dir string) (string, error) {
	exe := "tsc"
	if runtime.GOOS == "windows" {
		exe = "tsc.exe"
	}
	repo = filepath.Clean(repo)
	older := ""
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		nm := filepath.Join(d, "node_modules")
		p := filepath.Join(nm, "@typescript", platformPackage(), "lib", exe)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
		if older == "" {
			if b, err := os.ReadFile(filepath.Join(nm, "typescript", "package.json")); err == nil {
				var pkg struct{ Version string }
				if json.Unmarshal(b, &pkg) == nil && pkg.Version != "" {
					older = pkg.Version
				}
			}
		}
		if d == repo || d == filepath.Dir(d) || !strings.HasPrefix(d, repo) {
			break
		}
	}
	switch {
	case older != "" && !strings.HasPrefix(older, "7.") && !strings.HasPrefix(older, "8."):
		return "", fmt.Errorf("%w: this project has TypeScript %s installed, and the check needs the native compiler that ships with 7.0 and later", ErrNoCompiler, older)
	case older != "":
		return "", fmt.Errorf("%w: TypeScript %s is installed but its native program for this machine (@typescript/%s) is not; run the project's package install", ErrNoCompiler, older, platformPackage())
	}
	return "", fmt.Errorf("%w in node_modules (looked from %s up to the repository root): install the project's dependencies, with typescript 7.0 or later", ErrNoCompiler, dir)
}

// FileEdit is one change to one file, as the caller would make it: either a piece of text replaced by another, or the
// whole contents. Several edits to one file apply in the order given.
type FileEdit struct {
	// File is the file, relative to the repository root (or absolute, inside it).
	File string `json:"file"`
	// OldString is text the file holds, and NewString what replaces it. OldString must occur exactly once unless
	// ReplaceAll is set.
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	// Content, when not nil, is the file's whole new contents; the file need not exist yet.
	Content *string `json:"content,omitempty"`
}

// EditedFile is one file an edit changes.
type EditedFile struct {
	File    string `json:"file"`
	Created bool   `json:"created,omitempty"`
}

// EditFinding is one error the edit would cause, at a line of the edited tree.
type EditFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"` // "TS2554: Expected 3 arguments, but got 2."
}

// EditCheck is what an edit would do to the build. Nothing is written.
type EditCheck struct {
	Files []EditedFile `json:"files"`
	// Errors are the compiler's errors with the edit applied that are not there without it, in the edited files and
	// in every other source file of the repository.
	Errors []EditFinding `json:"errors,omitempty"`
	// Fixed counts the errors that are there now and would be gone.
	Fixed int `json:"fixed,omitempty"`
	// Checked is how many source files the compiler was asked about, before and after.
	Checked int `json:"checked"`
	// Unasked lists the files the compiler would not answer for, with the reason for the first.
	Unasked    []string `json:"unasked,omitempty"`
	UnaskedWhy string   `json:"unasked_why,omitempty"`
	// Compiler is the version of the compiler that answered, and CompilerPath the program.
	Compiler     string  `json:"compiler"`
	CompilerPath string  `json:"compiler_path"`
	Seconds      float64 `json:"seconds"`
	// Clean is true when every file was asked about and the edit causes no new error.
	Clean bool `json:"clean"`
	// Incomplete is true when some file could not be asked about: the errors listed are then a lower bound.
	Incomplete bool `json:"incomplete"`
}

// diag is one error the compiler reports for a file.
type diag struct {
	line int // 1-based
	msg  string
}

// CheckEdit applies edits to unsaved copies of the files inside the project's own compiler and reports the errors
// that appear: see EditCheck. Nothing is written to disk, and the server is stopped before it returns.
//
// The compiler has no request for "every error in the workspace", so every source file of the repository (as the
// index walks it: .gitignore is honored) is asked about twice, once as it is on disk and once with the edit open.
func (r *Runner) CheckEdit(ctx context.Context, edits []FileEdit) (*EditCheck, error) {
	start := time.Now()
	root, err := filepath.EvalSymlinks(r.Repo)
	if err != nil {
		return nil, err
	}
	patch, created, order, err := applyEdits(root, edits)
	if err != nil {
		return nil, err
	}
	tsc := r.Tsc
	if tsc == "" {
		if tsc, err = FindCompiler(root, filepath.Dir(order[0])); err != nil {
			return nil, err
		}
	}
	files, err := r.sources(root)
	if err != nil {
		return nil, err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := r.Environ
	if env == nil {
		env = os.Environ()
	}
	s, err := lsp.Start(ctx, lsp.Options{Name: "tsc", Argv: []string{tsc, "--lsp", "--stdio"}, Dir: root, Env: env, Root: root,
		Spawn: r.Spawn, Timeout: timeout, Capabilities: map[string]any{
			"workspace":    map[string]any{"configuration": true, "workspaceFolders": true},
			"textDocument": map[string]any{"diagnostic": map[string]any{"relatedDocumentSupport": false}},
		}})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	res := &EditCheck{CompilerPath: tsc}
	_, res.Compiler = s.ServerInfo()
	for _, abs := range order {
		res.Files = append(res.Files, EditedFile{File: rel(root, abs), Created: created[abs]})
	}

	before, _, err := r.pullAll(ctx, s, files)
	if err != nil {
		return nil, err
	}
	for _, abs := range order {
		s.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": lsp.PathToURI(abs), "languageId": sourceExts[strings.ToLower(filepath.Ext(abs))], "version": 1, "text": string(patch[abs])}})
	}
	all := files
	for _, abs := range order {
		if created[abs] {
			all = append(all, abs)
		}
	}
	after, failed, err := r.pullAll(ctx, s, all)
	if err != nil {
		return nil, err
	}
	res.Checked = len(all) - len(failed)
	for i, f := range failed {
		if i == 0 {
			res.UnaskedWhy = f.why
		}
		res.Unasked = append(res.Unasked, rel(root, f.file))
	}

	edited := map[string]bool{}
	for _, abs := range order {
		edited[abs] = true
	}
	for _, abs := range all {
		was, now := before[abs], after[abs]
		// An error is the same error when it has the same message on the same line. In a file the edit rewrote, lines
		// have moved, so what is left after that is matched by message alone.
		exact := func(d diag) string { return fmt.Sprintf("%d\x00%s", d.line, d.msg) }
		had := map[string]int{}
		for _, d := range was {
			had[exact(d)]++
		}
		var rest []diag
		for _, d := range now {
			if had[exact(d)] > 0 {
				had[exact(d)]--
				continue
			}
			rest = append(rest, d)
		}
		left := 0
		byMsg := map[string]int{}
		for _, d := range was {
			if had[exact(d)] > 0 {
				had[exact(d)]--
				byMsg[d.msg]++
				left++
			}
		}
		for _, d := range rest {
			if edited[abs] && byMsg[d.msg] > 0 {
				byMsg[d.msg]--
				left--
				continue
			}
			res.Errors = append(res.Errors, EditFinding{File: rel(root, abs), Line: d.line, Message: d.msg})
		}
		res.Fixed += left
	}
	sort.SliceStable(res.Errors, func(i, j int) bool {
		a, b := res.Errors[i], res.Errors[j]
		if ea, eb := edited[filepath.Join(root, filepath.FromSlash(a.File))], edited[filepath.Join(root, filepath.FromSlash(b.File))]; ea != eb {
			return ea // the edited files first
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	res.Incomplete = len(res.Unasked) > 0
	res.Clean = len(res.Errors) == 0 && !res.Incomplete
	res.Seconds = float64(int(time.Since(start).Seconds()*100)) / 100
	return res, nil
}

// applyEdits reads the files the edits name and returns each one's new contents by absolute path, which of them do
// not exist yet, and the paths in the order first named.
func applyEdits(root string, edits []FileEdit) (patch map[string][]byte, created map[string]bool, order []string, err error) {
	if len(edits) == 0 {
		return nil, nil, nil, errors.New("no edit given")
	}
	patch, created = map[string][]byte{}, map[string]bool{}
	old := map[string][]byte{}
	for _, e := range edits {
		name := strings.TrimSpace(e.File)
		if name == "" {
			return nil, nil, nil, errors.New("an edit names no file")
		}
		abs := filepath.FromSlash(name)
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		abs = filepath.Clean(abs)
		if real, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
			abs = filepath.Join(real, filepath.Base(abs))
		}
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return nil, nil, nil, fmt.Errorf("%s is outside the repository", name)
		}
		r := rel(root, abs)
		if !IsSource(abs) {
			return nil, nil, nil, fmt.Errorf("%s: only TypeScript and JavaScript source files are checked (an edit to tsconfig.json or package.json changes what is built, not how it type-checks)", r)
		}
		cur, have := patch[abs]
		if !have {
			src, rerr := os.ReadFile(abs)
			switch {
			case rerr == nil:
				old[abs] = src
			case !os.IsNotExist(rerr):
				return nil, nil, nil, fmt.Errorf("%s: %w", r, rerr)
			case e.Content == nil:
				return nil, nil, nil, fmt.Errorf("%s does not exist: give its whole contents to create it", r)
			default:
				created[abs] = true
			}
			cur = src
			order = append(order, abs)
		}
		switch {
		case e.Content != nil:
			cur = []byte(*e.Content)
		case e.OldString == "":
			return nil, nil, nil, fmt.Errorf("%s: an edit needs old_string, or content for the whole file", r)
		default:
			n := strings.Count(string(cur), e.OldString)
			switch {
			case n == 0:
				return nil, nil, nil, fmt.Errorf("%s: old_string is not in the file", r)
			case n > 1 && !e.ReplaceAll:
				return nil, nil, nil, fmt.Errorf("%s: old_string occurs %d times: add surrounding text to pick one, or set replace_all", r, n)
			}
			cur = []byte(strings.ReplaceAll(string(cur), e.OldString, e.NewString))
		}
		patch[abs] = cur
	}
	kept := order[:0]
	for _, abs := range order {
		if !created[abs] && string(old[abs]) == string(patch[abs]) {
			delete(patch, abs)
			continue
		}
		kept = append(kept, abs)
	}
	if len(kept) == 0 {
		return nil, nil, nil, errors.New("the edit changes nothing")
	}
	return patch, created, kept, nil
}

func rel(root, abs string) string {
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// sources lists the repository's TypeScript and JavaScript files, as absolute paths.
func (r *Runner) sources(root string) ([]string, error) {
	// the size and minified-file limits of the index walk stay on: a bundle is not something an edit is checked against
	names, err := repo.Walk(repo.Options{Root: root})
	if err != nil {
		return nil, err
	}
	limit := r.MaxFiles
	if limit <= 0 {
		limit = 20000
	}
	var out []string
	for _, n := range names {
		if IsSource(n) {
			out = append(out, filepath.Join(root, filepath.FromSlash(n)))
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("the repository has %d TypeScript and JavaScript files, more than the %d one check asks about", len(out), limit)
	}
	return out, nil
}

type unasked struct{ file, why string }

// pullAll asks the server for the errors of every file. A file the server refuses is returned in failed; an error is
// returned only when the server is gone or the time is up.
func (r *Runner) pullAll(ctx context.Context, s *lsp.Session, files []string) (map[string][]diag, []unasked, error) {
	n := r.Parallel
	if n <= 0 {
		n = 8
	}
	var (
		mu     sync.Mutex
		out    = map[string][]diag{}
		failed []unasked
		fatal  error
		wg     sync.WaitGroup
		next   = make(chan string)
	)
	for range min(n, max(len(files), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range next {
				ds, err := pull(ctx, s, f)
				mu.Lock()
				switch {
				case err == nil:
					out[f] = ds
				case errors.Is(err, lsp.ErrClosed) || ctx.Err() != nil:
					if fatal == nil {
						fatal = err
					}
				default:
					failed = append(failed, unasked{f, err.Error()})
				}
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		mu.Lock()
		stop := fatal != nil
		mu.Unlock()
		if stop {
			break
		}
		next <- f
	}
	close(next)
	wg.Wait()
	if fatal != nil {
		if errors.Is(fatal, context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("the compiler did not finish in time (%d of %d files answered)", len(out), len(files))
		}
		return nil, nil, fatal
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].file < failed[j].file })
	return out, failed, nil
}

// pull asks for one file's diagnostics and keeps the errors.
func pull(ctx context.Context, s *lsp.Session, abs string) ([]diag, error) {
	raw, err := s.Call(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]string{"uri": lsp.PathToURI(abs)}})
	if err != nil {
		return nil, err
	}
	var rep struct {
		Items []struct {
			Range    lsp.Range       `json:"range"`
			Severity int             `json:"severity"`
			Code     json.RawMessage `json:"code"`
			Message  string          `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("unreadable diagnostics: %w", err)
	}
	var out []diag
	for _, it := range rep.Items {
		if it.Severity > 1 { // warnings, and the hints an editor greys out: the compiler does not fail on them
			continue
		}
		msg := strings.Join(strings.Fields(it.Message), " ")
		if code := strings.Trim(string(it.Code), `"`); code != "" && code != "null" {
			msg = "TS" + code + ": " + msg
		}
		out = append(out, diag{line: it.Range.Start.Line + 1, msg: msg})
	}
	return out, nil
}

// Text renders the check. maxFindings caps the error lines (0 means all).
func (c *EditCheck) Text(maxFindings int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "check edit: %d files edited; %d source files checked with TypeScript %s in %.2fs\n", len(c.Files), c.Checked, c.Compiler, c.Seconds)
	for _, f := range c.Files {
		fmt.Fprintf(&b, "  %s", f.File)
		if f.Created {
			b.WriteString(" (new file)")
		}
		b.WriteByte('\n')
	}
	for i, f := range c.Errors {
		if maxFindings > 0 && i == maxFindings {
			fmt.Fprintf(&b, "ERROR: … %d more\n", len(c.Errors)-maxFindings)
			break
		}
		fmt.Fprintf(&b, "ERROR: %s:%d: %s\n", f.File, f.Line, f.Message)
	}
	if c.Incomplete {
		fmt.Fprintf(&b, "INCOMPLETE: the compiler did not answer for %d files (%s: %s)\n", len(c.Unasked), c.Unasked[0], c.UnaskedWhy)
	}
	if c.Fixed > 0 {
		fmt.Fprintf(&b, "fixes %d errors that are there now\n", c.Fixed)
	}
	switch {
	case len(c.Errors) > 0:
		fmt.Fprintf(&b, "WOULD NOT COMPILE: %d new errors\n", len(c.Errors))
	case c.Clean:
		b.WriteString("checked: the edit was opened as unsaved contents in the project's compiler; no new error in any source file. Not checked: whether a name on a line the edit does not touch now refers to another declaration\n")
	default:
		b.WriteString("NO NEW ERROR FOUND, but not every file was checked\n")
	}
	return b.String()
}
