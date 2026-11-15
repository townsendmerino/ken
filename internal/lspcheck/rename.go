package lspcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/lsp"
)

// RenamePlan is what a rename would edit, computed by the project's own language server and then checked by applying
// it as unsaved contents and asking for every file's errors again. Nothing is written.
type RenamePlan struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	NewName string `json:"new_name"`
	// Files are the files the rename edits, with the lines that change.
	Files []RenameFile `json:"files"`
	// Patch is a unified diff against the repository root, for the caller to review and apply. Empty when Refused.
	Patch string `json:"patch,omitempty"`
	// Conflicts say why the rename is not safe as computed: an error the edited project would have that it does not
	// have now, or an edit that falls outside the repository's own source files.
	Conflicts []string `json:"conflicts,omitempty"`
	// Mentions are lines that still spell the old name once the rename is applied: a comment, a string, a name looked
	// up at run time, or another symbol of that name. The server did not judge them to be this symbol.
	Mentions []RenameFile `json:"mentions,omitempty"`
	// Verified is true when the plan was applied as unsaved contents and no source file gained an error.
	Verified bool `json:"verified"`
	// Refused is true when Conflicts is not empty: the plan then carries no patch.
	Refused bool `json:"refused,omitempty"`
	// Incomplete is true when the check could not ask about everything (Check says what): the plan is given, unverified.
	Incomplete bool `json:"incomplete"`
	// Check is the edit check of the applied plan.
	Check *EditCheck `json:"check,omitempty"`
	// Tool and Compiler name the server that computed the rename.
	Tool     string  `json:"tool"`
	Compiler string  `json:"compiler"`
	Seconds  float64 `json:"seconds"`
}

// RenameFile is one file and the 1-based lines of it that matter.
type RenameFile struct {
	File  string `json:"file"`
	Lines []int  `json:"lines"`
}

func isWord(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// wordCol returns the 1-based byte column of the first place line spells name as a whole word, or 0.
func wordCol(line, name string) int {
	for from := 0; name != ""; {
		i := strings.Index(line[from:], name)
		if i < 0 {
			return 0
		}
		i += from
		end := i + len(name)
		if (i == 0 || !isWord(line[i-1])) && (end == len(line) || !isWord(line[end])) {
			return i + 1
		}
		from = i + 1
	}
	return 0
}

// Rename asks the server for the edits that rename the symbol called name on line (1-based) of file to newName,
// and puts the result to check, the tier's edit check. file is relative to repoDir or absolute.
func Rename(ctx context.Context, o Options, repoDir, file string, line int, name, newName string,
	check func(context.Context, []FileEdit) (*EditCheck, error)) (*RenamePlan, error) {
	start := time.Now()
	root, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(newName) == "" || strings.ContainsAny(newName, " \t\r\n") {
		return nil, fmt.Errorf("new name %q is not a name", newName)
	}
	if name == newName {
		return nil, errors.New("the new name is the name it has")
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, filepath.FromSlash(file))
	}
	if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = real
	}
	relFile := rel(root, abs)
	if relFile == "" || strings.HasPrefix(relFile, "../") || filepath.IsAbs(relFile) {
		return nil, fmt.Errorf("%s is outside the repository", file)
	}
	if !o.Languages.Has(abs) {
		return nil, fmt.Errorf("%s is not a source file this server answers for", relFile)
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	if line < 1 || line > len(lines) {
		return nil, fmt.Errorf("%s has %d lines: there is no line %d", relFile, len(lines), line)
	}
	col := wordCol(strings.TrimSuffix(lines[line-1], "\r"), name)
	if col == 0 {
		return nil, fmt.Errorf("%s:%d does not name %q: the line is %q", relFile, line, name, strings.TrimSpace(lines[line-1]))
	}

	files, err := Sources(root, o.Languages, o.MaxFiles)
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := o.Environ
	if env == nil {
		env = os.Environ()
	}
	s, err := lsp.Start(rctx, lsp.Options{Name: o.Server.Name, Argv: o.Server.Argv, Dir: root, Env: env, Root: root,
		Spawn: o.Spawn, Timeout: timeout, Capabilities: map[string]any{
			"workspace":    map[string]any{"configuration": true, "workspaceFolders": true, "workspaceEdit": map[string]any{"documentChanges": true}},
			"textDocument": map[string]any{"rename": map[string]any{"prepareSupport": false}},
			"window":       map[string]any{"workDoneProgress": true},
		}})
	if err != nil {
		return nil, err
	}
	_, version := s.ServerInfo()
	if version == "" {
		version = o.Server.Version
	}
	plan := &RenamePlan{File: relFile, Line: line, Name: name, NewName: newName, Tool: o.Server.Label, Compiler: version}
	lang := o.Languages[strings.ToLower(filepath.Ext(abs))]
	if o.Server.OpenAll {
		// a server that answers only for open files finds the uses in the files it has open
		for _, f := range files {
			if f == abs {
				continue // Rename opens it
			}
			text, rerr := os.ReadFile(f)
			if rerr != nil {
				s.Close()
				return nil, rerr
			}
			s.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
				"uri": lsp.PathToURI(f), "languageId": o.Languages[strings.ToLower(filepath.Ext(f))], "version": 1, "text": string(text)}})
		}
	}
	changed, err := s.Rename(rctx, abs, lang, line, col, newName)
	s.Close()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s did not answer the rename in time (%s)", o.Server.Name, timeout)
		}
		return nil, fmt.Errorf("%s would not rename %q at %s:%d: %w", o.Server.Name, name, relFile, line, err)
	}

	isSource := map[string]bool{}
	for _, f := range files {
		isSource[f] = true
	}
	var paths []string
	for p := range changed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var edits []FileEdit
	var patch strings.Builder
	after := map[string][]string{} // each edited source file's new lines
	outside := 0
	for _, p := range paths {
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			p, changed[real] = real, changed[p]
		}
		old, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, rerr
		}
		a, b := strings.Split(string(old), "\n"), changed[p]
		differ := diffLines(a, b)
		if len(differ) == 0 {
			continue
		}
		r := rel(root, p)
		if !isSource[p] {
			outside++
			if outside <= 5 {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%s: the rename would edit this file, which is not one of the repository's own source files (a dependency, a generated file, or one git ignores)", r))
			}
			continue
		}
		plan.Files = append(plan.Files, RenameFile{File: r, Lines: differ})
		writeUnified(&patch, r, a, b)
		content := strings.Join(b, "\n")
		edits = append(edits, FileEdit{File: r, Content: &content})
		after[p] = b
	}
	if outside > 5 {
		plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("… and %d more files outside the repository's own source", outside-5))
	}
	if len(plan.Files) == 0 && outside == 0 {
		return nil, fmt.Errorf("%s offers no rename for %q at %s:%d: it is not a symbol the server can rename there", o.Server.Name, name, relFile, line)
	}

	if len(edits) > 0 {
		res, cerr := check(ctx, edits)
		if cerr != nil {
			return nil, fmt.Errorf("the rename was computed, but checking it failed: %w", cerr)
		}
		plan.Check = res
		for i, e := range res.Errors {
			if i == 20 {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("… and %d more new errors", len(res.Errors)-20))
				break
			}
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%s:%d: with the rename applied: %s", e.File, e.Line, e.Message))
		}
		plan.Incomplete = res.Incomplete
		plan.Verified = res.Clean && outside == 0
	}
	plan.Refused = len(plan.Conflicts) > 0
	if !plan.Refused {
		plan.Patch = patch.String()
	}

	// what still spells the old name
	for _, f := range files {
		ls, edited := after[f]
		if !edited {
			text, rerr := os.ReadFile(f)
			if rerr != nil || !strings.Contains(string(text), name) {
				continue
			}
			ls = strings.Split(string(text), "\n")
		}
		var at []int
		for i, l := range ls {
			if wordCol(l, name) > 0 {
				at = append(at, i+1)
			}
		}
		if len(at) > 0 {
			plan.Mentions = append(plan.Mentions, RenameFile{File: rel(root, f), Lines: at})
		}
	}
	plan.Seconds = float64(int(time.Since(start).Seconds()*100)) / 100
	return plan, nil
}

// diffLines returns the 1-based lines of a that differ from b. Where the two have different lengths it is every line
// from the first difference to the last, by the lines of a.
func diffLines(a, b []string) []int {
	var out []int
	if len(a) == len(b) {
		for i := range a {
			if a[i] != b[i] {
				out = append(out, i+1)
			}
		}
		return out
	}
	lo, hi := changedSpan(a, b)
	for i := lo; i < max(hi, lo+1); i++ {
		out = append(out, i+1)
	}
	return out
}

// changedSpan trims the common head and tail of a and b and returns the span of a between them, [lo, hi).
func changedSpan(a, b []string) (lo, hi int) {
	for lo < len(a) && lo < len(b) && a[lo] == b[lo] {
		lo++
	}
	hi = len(a)
	for bj := len(b); hi > lo && bj > lo && a[hi-1] == b[bj-1]; {
		hi--
		bj--
	}
	return lo, hi
}

// writeUnified writes a unified diff of one file from its lines before (a) and after (b), each as strings.Split
// gives them. Paths carry the a/ and b/ prefixes git apply expects.
func writeUnified(w *strings.Builder, file string, a, b []string) {
	const ctx = 3
	fmt.Fprintf(w, "--- a/%s\n+++ b/%s\n", file, file)
	// a trailing newline shows as a last empty element; it is not a line
	trim := func(s []string) []string {
		if n := len(s); n > 0 && s[n-1] == "" {
			return s[:n-1]
		}
		return s
	}
	if len(a) != len(b) {
		a, b = trim(a), trim(b)
		lo, hi := changedSpan(a, b)
		bhi := len(b) - (len(a) - hi)
		from, to := max(lo-ctx, 0), min(hi+ctx, len(a))
		fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", from+1, to-from, from+1, to-from+(bhi-lo)-(hi-lo))
		for _, l := range a[from:lo] {
			w.WriteString(" " + l + "\n")
		}
		for _, l := range a[lo:hi] {
			w.WriteString("-" + l + "\n")
		}
		for _, l := range b[lo:bhi] {
			w.WriteString("+" + l + "\n")
		}
		for _, l := range a[hi:to] {
			w.WriteString(" " + l + "\n")
		}
		return
	}
	lines := diffLines(a, b)
	n := len(trim(a))
	for i := 0; i < len(lines); {
		lo := max(lines[i]-ctx, 1)
		j := i
		for j+1 < len(lines) && lines[j+1]-lines[j] <= 2*ctx {
			j++
		}
		hi := min(lines[j]+ctx, n)
		fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", lo, hi-lo+1, lo, hi-lo+1)
		for l := lo; l <= hi; l++ {
			if a[l-1] == b[l-1] {
				w.WriteString(" " + a[l-1] + "\n")
				continue
			}
			// a run of changed lines: removals, then additions
			k := l
			for k <= hi && a[k-1] != b[k-1] {
				w.WriteString("-" + a[k-1] + "\n")
				k++
			}
			for m := l; m < k; m++ {
				w.WriteString("+" + b[m-1] + "\n")
			}
			l = k - 1
		}
		i = j + 1
	}
}

// Text renders the plan. maxLines caps the conflict and mention lines (0 means all).
func (p *RenamePlan) Text(maxLines int) string {
	var b strings.Builder
	n := 0
	for _, f := range p.Files {
		n += len(f.Lines)
	}
	fmt.Fprintf(&b, "rename %s -> %s (%s:%d): %d lines in %d files, computed by %s %s in %.2fs\n", p.Name, p.NewName, p.File, p.Line, n, len(p.Files), p.Tool, p.Compiler, p.Seconds)
	for i, c := range p.Conflicts {
		if maxLines > 0 && i == maxLines {
			fmt.Fprintf(&b, "CONFLICT: … %d more\n", len(p.Conflicts)-maxLines)
			break
		}
		fmt.Fprintf(&b, "CONFLICT: %s\n", c)
	}
	if c := p.Check; c != nil && !p.Refused {
		if len(c.Unasked) > 0 {
			fmt.Fprintf(&b, "INCOMPLETE: no answer for %d files (%s: %s)\n", len(c.Unasked), c.Unasked[0], c.UnaskedWhy)
		}
		if c.Partial != "" {
			fmt.Fprintf(&b, "INCOMPLETE: %s\n", c.Partial)
		}
	}
	switch {
	case p.Refused:
		b.WriteString("NOT SAFE as computed: no patch is given")
		if len(p.Files) > 0 {
			b.WriteString(". The files below are where it would have edited")
		}
		b.WriteByte('\n')
	case p.Verified:
		fmt.Fprintf(&b, "checked: the plan was applied as unsaved contents and %s asked about every source file again; no new error. Not checked: whether a name the rename does not touch now refers to another declaration (renaming onto a name already in scope can do that with no error)\n", p.Check.Tool)
	default:
		b.WriteString("NO NEW ERROR FOUND, but not everything was checked: the patch is given unverified\n")
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "  %s (%d)\n", f.File, len(f.Lines))
	}
	if len(p.Mentions) > 0 {
		total := 0
		for _, m := range p.Mentions {
			total += len(m.Lines)
		}
		fmt.Fprintf(&b, "still spelled %q on %d lines in %d files the rename leaves as they are (a comment, a string, a name looked up at run time, or another symbol of that name):\n", p.Name, total, len(p.Mentions))
		shown := 0
		for _, m := range p.Mentions {
			for _, l := range m.Lines {
				if maxLines > 0 && shown == maxLines {
					fmt.Fprintf(&b, "  … %d more\n", total-shown)
					return b.String()
				}
				fmt.Fprintf(&b, "  %s:%d\n", m.File, l)
				shown++
			}
		}
	}
	return b.String()
}
