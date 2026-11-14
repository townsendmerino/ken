// Package lspcheck checks an edit against a language server: the edit is opened as unsaved contents, every source
// file is asked for its errors before and after, and what is new is reported (ADR-064, ADR-065). It knows no
// language. A tier for one (internal/tstypes, internal/pytypes) finds the project's own server and describes it in a
// Server.
package lspcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/lsp"
	"github.com/townsendmerino/ken/internal/repo"
)

// Server describes the language server that answers and the files it answers for.
type Server struct {
	// Name is what errors call the program ("tsc").
	Name string
	// Label is what the reply calls it, before its version ("TypeScript").
	Label string
	// Verdict is the line for an edit that causes errors, before their count ("WOULD NOT COMPILE").
	Verdict string
	// Version is used when the server does not give its own in the handshake.
	Version string
	// Where finishes "the edit was opened as unsaved contents in ..." ("the project's compiler").
	Where string
	// Argv is the command.
	Argv []string
	// OpenAll opens every source file before asking: for a server that reports only on open files.
	OpenAll bool
	// CodePrefix goes in front of a diagnostic's code ("TS" makes 2554 read TS2554).
	CodePrefix string
	// ImportsNewFile is set for a server that resolves imports on disk, and so cannot find a file the edit creates:
	// it reports whether a source line imports the created file (given relative to the repository). An error on such
	// a line is the server not seeing the file, not the edit's fault. It is set aside and counted, and the check is
	// then incomplete: what the import brings in was not checked.
	ImportsNewFile func(line, created string) bool
}

// Languages are the files a tier checks: LSP's name for the language, by lower-case file extension.
type Languages map[string]string

// Has reports whether name is a file of one of the languages.
func (l Languages) Has(name string) bool { return l[strings.ToLower(filepath.Ext(name))] != "" }

// Options is everything one check needs besides the edit.
type Options struct {
	Server    Server
	Languages Languages
	// Timeout bounds the check; 0 means two minutes. MaxFiles is the most source files asked about; 0 means 20,000.
	// Parallel is how many are asked about at once; 0 means 8.
	Timeout  time.Duration
	MaxFiles int
	Parallel int
	// Spawn starts the server; nil runs the real program. Environ is its environment; nil means the process's own.
	Spawn   lsp.SpawnFunc
	Environ []string
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
	// Delete removes the file. MoveTo moves it, with the contents it has at that point of the edits, to another
	// path, which later edits may change. A language server cannot be told that a file on disk is gone, so a file
	// that is deleted or moved away is checked as an empty file: every use of what it held is an error, though the
	// server words a missing module differently, and an import that only names the module is not reported.
	Delete bool   `json:"delete,omitempty"`
	MoveTo string `json:"move_to,omitempty"`
}

// EditedFile is one file an edit changes.
type EditedFile struct {
	File    string `json:"file"`
	Created bool   `json:"created,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// EditFinding is one error the edit would cause, at a line of the edited tree.
type EditFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"` // the code, then the text: "TS2554: Expected 3 arguments, but got 2."
}

// EditCheck is what an edit would do to the build. Nothing is written.
type EditCheck struct {
	Files []EditedFile `json:"files"`
	// Errors are the server's errors with the edit applied that are not there without it, in the edited files and in
	// every other source file of the repository.
	Errors []EditFinding `json:"errors,omitempty"`
	// Fixed counts the errors that are there now and would be gone.
	Fixed int `json:"fixed,omitempty"`
	// Checked is how many source files the server was asked about, before and after.
	Checked int `json:"checked"`
	// Unasked lists the files the server would not answer for, with the reason for the first.
	Unasked    []string `json:"unasked,omitempty"`
	UnaskedWhy string   `json:"unasked_why,omitempty"`
	// UnseenImports counts the errors set aside because they are on a line that imports a file the edit creates:
	// the server resolves imports on disk, where that file is not yet. UnseenFile is the first such file.
	UnseenImports int    `json:"unseen_imports,omitempty"`
	UnseenFile    string `json:"unseen_file,omitempty"`
	// Tool is the server's label ("TypeScript", "pyright"), Compiler its version, and CompilerPath the program.
	Tool         string  `json:"tool"`
	Compiler     string  `json:"compiler"`
	CompilerPath string  `json:"compiler_path"`
	Seconds      float64 `json:"seconds"`
	// Clean is true when every file was asked about and the edit causes no new error.
	Clean bool `json:"clean"`
	// Incomplete is true when some file could not be asked about: the errors listed are then a lower bound.
	Incomplete bool `json:"incomplete"`

	where, verdict string
}

// Patch is a set of edits applied in memory.
type Patch struct {
	Root    string            // the repository, symlinks resolved
	Order   []string          // the edited files, absolute, in the order first named
	Content map[string][]byte // each one's new contents
	Created map[string]bool   // those that do not exist yet
	Deleted map[string]bool   // those the edit deletes or moves away: their Content is empty
}

// Apply reads the files the edits name under repo and applies the edits in memory. notSource finishes the refusal of
// a file that is not one of langs ("only TypeScript and JavaScript source files are checked ...").
func Apply(repoDir string, edits []FileEdit, langs Languages, notSource string) (*Patch, error) {
	root, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, errors.New("no edit given")
	}
	p := &Patch{Root: root, Content: map[string][]byte{}, Created: map[string]bool{}, Deleted: map[string]bool{}}
	old := map[string][]byte{}
	// place resolves a name an edit gives to a file of the repository.
	place := func(name string) (abs, r string, err error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", "", errors.New("an edit names no file")
		}
		abs = filepath.FromSlash(name)
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		abs = filepath.Clean(abs)
		if real, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
			abs = filepath.Join(real, filepath.Base(abs))
		}
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return "", "", fmt.Errorf("%s is outside the repository", name)
		}
		r = rel(root, abs)
		if !langs.Has(abs) {
			return "", "", fmt.Errorf("%s: %s", r, notSource)
		}
		return abs, r, nil
	}
	// load brings a file into the patch as it is on disk; it reports whether the file exists there.
	load := func(abs, r string) (bool, error) {
		if _, have := p.Content[abs]; have {
			return !p.Created[abs], nil
		}
		src, rerr := os.ReadFile(abs)
		switch {
		case rerr == nil:
			old[abs] = src
		case !os.IsNotExist(rerr):
			return false, fmt.Errorf("%s: %w", r, rerr)
		default:
			p.Created[abs] = true
		}
		p.Content[abs] = src
		p.Order = append(p.Order, abs)
		return rerr == nil, nil
	}
	for _, e := range edits {
		abs, r, err := place(e.File)
		if err != nil {
			return nil, err
		}
		_, known := p.Content[abs]
		exists, err := load(abs, r)
		if err != nil {
			return nil, err
		}
		gone := p.Deleted[abs] || (!exists && !known)
		cur := p.Content[abs]
		switch {
		case e.Delete || e.MoveTo != "":
			if gone {
				return nil, fmt.Errorf("%s does not exist, so it cannot be deleted or moved", r)
			}
			if e.MoveTo != "" {
				to, tr, err := place(e.MoveTo)
				if err != nil {
					return nil, err
				}
				_, toKnown := p.Content[to]
				toExists, err := load(to, tr)
				if err != nil {
					return nil, err
				}
				if to == abs || (!p.Deleted[to] && (toExists || toKnown)) {
					return nil, fmt.Errorf("%s cannot be moved to %s: that file exists", r, tr)
				}
				p.Content[to], p.Deleted[to] = cur, false
			}
			cur, p.Deleted[abs] = []byte{}, true
		case e.Content != nil:
			cur, p.Deleted[abs] = []byte(*e.Content), false
		case gone:
			return nil, fmt.Errorf("%s does not exist: give its whole contents to create it", r)
		case e.OldString == "":
			return nil, fmt.Errorf("%s: an edit needs old_string, content for the whole file, delete or move_to", r)
		default:
			n := strings.Count(string(cur), e.OldString)
			switch {
			case n == 0:
				return nil, fmt.Errorf("%s: old_string is not in the file", r)
			case n > 1 && !e.ReplaceAll:
				return nil, fmt.Errorf("%s: old_string occurs %d times: add surrounding text to pick one, or set replace_all", r, n)
			}
			cur = []byte(strings.ReplaceAll(string(cur), e.OldString, e.NewString))
		}
		p.Content[abs] = cur
	}
	kept := p.Order[:0]
	for _, abs := range p.Order {
		switch {
		case p.Created[abs] && p.Deleted[abs]: // created and deleted again: never there
		case p.Deleted[abs], p.Created[abs]:
			kept = append(kept, abs)
			continue
		case string(old[abs]) != string(p.Content[abs]):
			kept = append(kept, abs)
			continue
		}
		delete(p.Content, abs)
		delete(p.Deleted, abs)
		delete(p.Created, abs)
	}
	p.Order = kept
	if len(kept) == 0 {
		return nil, errors.New("the edit changes nothing")
	}
	return p, nil
}

func rel(root, abs string) string {
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// Sources lists the files of langs under root, as absolute paths: the index's walk, so .gitignore is honored and
// bundles are left out. More than limit (0 means 20,000) is an error: an answer from part of a repository would read
// as complete.
func Sources(root string, langs Languages, limit int) ([]string, error) {
	names, err := repo.Walk(repo.Options{Root: root})
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20000
	}
	var out []string
	for _, n := range names {
		if langs.Has(n) {
			out = append(out, filepath.Join(root, filepath.FromSlash(n)))
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("the repository has %d source files of this language, more than the %d one check asks about", len(out), limit)
	}
	return out, nil
}

// diag is one error the server reports for a file.
type diag struct {
	line int // 1-based
	msg  string
}

// Run starts the server, asks every source file for its errors, opens the patch as unsaved contents, asks again, and
// stops the server. Nothing is written to disk.
func Run(ctx context.Context, o Options, p *Patch) (*EditCheck, error) {
	start := time.Now()
	root := p.Root
	files, err := Sources(root, o.Languages, o.MaxFiles)
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := o.Environ
	if env == nil {
		env = os.Environ()
	}
	s, err := lsp.Start(ctx, lsp.Options{Name: o.Server.Name, Argv: o.Server.Argv, Dir: root, Env: env, Root: root,
		Spawn: o.Spawn, Timeout: timeout, Capabilities: map[string]any{
			"workspace":    map[string]any{"configuration": true, "workspaceFolders": true},
			"textDocument": map[string]any{"diagnostic": map[string]any{"relatedDocumentSupport": false}},
			"window":       map[string]any{"workDoneProgress": true},
		}})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	res := &EditCheck{Tool: o.Server.Label, CompilerPath: o.Server.Argv[0], where: o.Server.Where, verdict: o.Server.Verdict}
	if _, res.Compiler = s.ServerInfo(); res.Compiler == "" {
		res.Compiler = o.Server.Version
	}
	for _, abs := range p.Order {
		res.Files = append(res.Files, EditedFile{File: rel(root, abs), Created: p.Created[abs], Deleted: p.Deleted[abs]})
	}
	open := func(abs string, text []byte) {
		s.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": lsp.PathToURI(abs), "languageId": o.Languages[strings.ToLower(filepath.Ext(abs))], "version": 1, "text": string(text)}})
	}
	opened := map[string]bool{}
	if o.Server.OpenAll {
		for _, f := range files {
			src, rerr := os.ReadFile(f)
			if rerr != nil {
				return nil, rerr
			}
			open(f, src)
			opened[f] = true
		}
	}

	before, _, err := pullAll(ctx, s, o, files)
	if err != nil {
		return nil, err
	}
	for _, abs := range p.Order {
		if opened[abs] {
			s.Notify("textDocument/didChange", map[string]any{
				"textDocument":   map[string]any{"uri": lsp.PathToURI(abs), "version": 2},
				"contentChanges": []any{map[string]string{"text": string(p.Content[abs])}}})
			continue
		}
		open(abs, p.Content[abs])
	}
	all := files
	for _, abs := range p.Order {
		if p.Created[abs] {
			all = append(all, abs)
		}
	}
	after, failed, err := pullAll(ctx, s, o, all)
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
	for _, abs := range p.Order {
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
	if o.Server.ImportsNewFile != nil {
		var created []string
		for _, abs := range p.Order {
			if p.Created[abs] && !p.Deleted[abs] {
				created = append(created, rel(root, abs))
			}
		}
		if len(created) > 0 {
			lines := map[string][]string{} // each file's contents with the edit applied, by line
			kept := res.Errors[:0]
			for _, e := range res.Errors {
				abs := filepath.Join(root, filepath.FromSlash(e.File))
				if _, ok := lines[e.File]; !ok {
					src, have := p.Content[abs]
					if !have {
						src, _ = os.ReadFile(abs)
					}
					lines[e.File] = strings.Split(string(src), "\n")
				}
				unseen := ""
				if l := lines[e.File]; e.Line >= 1 && e.Line <= len(l) {
					for _, c := range created {
						if o.Server.ImportsNewFile(l[e.Line-1], c) {
							unseen = c
							break
						}
					}
				}
				if unseen == "" {
					kept = append(kept, e)
					continue
				}
				res.UnseenImports++
				if res.UnseenFile == "" {
					res.UnseenFile = unseen
				}
			}
			res.Errors = kept
		}
	}
	res.Incomplete = len(res.Unasked) > 0 || res.UnseenImports > 0
	res.Clean = len(res.Errors) == 0 && !res.Incomplete
	res.Seconds = float64(int(time.Since(start).Seconds()*100)) / 100
	return res, nil
}

type unasked struct{ file, why string }

// pullAll asks the server for the errors of every file. A file the server refuses is returned in failed; an error is
// returned only when the server is gone or the time is up.
func pullAll(ctx context.Context, s *lsp.Session, o Options, files []string) (map[string][]diag, []unasked, error) {
	n := o.Parallel
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
				ds, err := pull(ctx, s, o.Server.CodePrefix, f)
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
			return nil, nil, fmt.Errorf("%s did not finish in time (%d of %d files answered)", o.Server.Name, len(out), len(files))
		}
		return nil, nil, fatal
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].file < failed[j].file })
	return out, failed, nil
}

// pull asks for one file's diagnostics and keeps the errors.
func pull(ctx context.Context, s *lsp.Session, codePrefix, abs string) ([]diag, error) {
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
		if it.Severity > 1 { // warnings, and the hints an editor greys out: the build does not fail on them
			continue
		}
		msg := strings.Join(strings.Fields(it.Message), " ")
		if code := strings.Trim(string(it.Code), `"`); code != "" && code != "null" {
			msg = codePrefix + code + ": " + msg
		}
		out = append(out, diag{line: it.Range.Start.Line + 1, msg: msg})
	}
	return out, nil
}

// Text renders the check. maxFindings caps the error lines (0 means all).
func (c *EditCheck) Text(maxFindings int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "check edit: %d files edited; %d source files checked with %s %s in %.2fs\n", len(c.Files), c.Checked, c.Tool, c.Compiler, c.Seconds)
	for _, f := range c.Files {
		fmt.Fprintf(&b, "  %s", f.File)
		if f.Created {
			b.WriteString(" (new file)")
		}
		if f.Deleted {
			b.WriteString(" (deleted: checked as an empty file)")
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
	if len(c.Unasked) > 0 {
		fmt.Fprintf(&b, "INCOMPLETE: no answer for %d files (%s: %s)\n", len(c.Unasked), c.Unasked[0], c.UnaskedWhy)
	}
	if c.UnseenImports > 0 {
		fmt.Fprintf(&b, "INCOMPLETE: %d imports of a file this edit creates (%s) could not be followed: %s resolves imports on disk, where the file is not yet. What those imports bring in was not checked\n", c.UnseenImports, c.UnseenFile, c.Tool)
	}
	if c.Fixed > 0 {
		fmt.Fprintf(&b, "fixes %d errors that are there now\n", c.Fixed)
	}
	switch {
	case len(c.Errors) > 0:
		verdict := c.verdict
		if verdict == "" {
			verdict = "WOULD NOT PASS"
		}
		fmt.Fprintf(&b, "%s: %d new errors\n", verdict, len(c.Errors))
	case c.Clean:
		where := c.where
		if where == "" {
			where = "the project's checker"
		}
		fmt.Fprintf(&b, "checked: the edit was opened as unsaved contents in %s; no new error in any source file. Not checked: whether a name on a line the edit does not touch now refers to another declaration\n", where)
	default:
		b.WriteString("NO NEW ERROR FOUND, but not everything was checked\n")
	}
	return b.String()
}

// ProjectDirs returns root and the directories below it, to the given depth, where a project of a repository that
// holds several may have its tools installed (frontend/, services/api/). Hidden directories, and those that hold
// dependencies or build output, are not entered. At most 2,000 directories are returned: this decides whether a
// tool is offered, and must stay cheap on a large tree.
func ProjectDirs(root string, depth int) []string {
	const limit = 2000
	dirs := []string{root}
	level := []string{root}
	for ; depth > 0 && len(level) > 0; depth-- {
		var next []string
		for _, d := range level {
			entries, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if !e.IsDir() || strings.HasPrefix(name, ".") || notAProject[name] {
					continue
				}
				if len(dirs) >= limit {
					return dirs
				}
				sub := filepath.Join(d, name)
				dirs = append(dirs, sub)
				next = append(next, sub)
			}
		}
		level = next
	}
	return dirs
}

// notAProject are directory names that hold dependencies or build output, never a project of their own.
var notAProject = map[string]bool{"node_modules": true, "vendor": true, "venv": true, "dist": true, "build": true,
	"target": true, "out": true, "__pycache__": true, "site-packages": true}
