package gotypes

import (
	"bytes"
	"context"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// RenameFile is one file a rename edits.
type RenameFile struct {
	File    string   `json:"file"`
	Lines   []int    `json:"lines"`
	Configs []string `json:"configs"` // the configurations whose rename edited it
}

// Mention is a file that still spells the old name after the rename, in a comment, a string, prose or assembly:
// nothing a type checker treats as a reference.
type Mention struct {
	File  string `json:"file"`
	Lines []int  `json:"lines"`
}

// RenamePlan is what renaming a symbol would change, merged over every configuration. Nothing is written: Patch is a
// unified diff against the repository root for the caller to review and apply.
type RenamePlan struct {
	Symbol  string       `json:"symbol"`
	NewName string       `json:"new_name"`
	Decls   []Decl       `json:"declarations"`
	Files   []RenameFile `json:"files"`
	Patch   string       `json:"patch"`
	// Conflicts lists why the rename is not safe as computed: a reason it was refused under a configuration, a
	// difference the check of the applied patch found (see Verified), or a place where two configurations disagree
	// about an edit (the patch carries the first configuration's version there, and the place needs a human).
	Conflicts []string  `json:"conflicts,omitempty"`
	Mentions  []Mention `json:"mentions,omitempty"`
	// Verified is true when the patch was applied in memory and every configuration type-checked again, with no
	// new type error, every identifier referring to the declaration it referred to before, and every type assertion
	// that could succeed still able to. Only the in-process engine checks; a plan from gopls is never Verified. When
	// the check finds a difference it is listed in Conflicts and the plan carries no patch.
	Verified bool `json:"verified"`
	// Refused is true when the rename is not safe as computed (Conflicts says why): Patch is then empty, and Files
	// lists where it would have edited.
	Refused    bool        `json:"refused,omitempty"`
	Runs       []ConfigRun `json:"runs"`
	Incomplete bool        `json:"incomplete"`
	// Cached and CacheAgeSeconds: see Result.
	Cached          bool    `json:"cached,omitempty"`
	CacheAgeSeconds float64 `json:"cache_age_seconds,omitempty"`
}

// RenamePlan computes the edits that rename symbol to newName. One configuration's rename leaves stale every file
// that only another configuration compiles, so each is asked and the edits are merged per line. A declaration with
// per-platform twins is renamed at every twin.
//
// Under the in-process engine (the default) the merged patch is then applied in memory and every configuration is
// type-checked again: see RenamePlan.Verified.
func (r *Runner) RenamePlan(ctx context.Context, symbol, newName string) (*RenamePlan, error) {
	if !token.IsIdentifier(newName) || newName == "_" {
		return nil, fmt.Errorf("new name %q is not an identifier", newName)
	}
	return cached(r, "rename", []string{symbol, newName}, func() (*RenamePlan, error) {
		return r.renamePlan(ctx, symbol, newName)
	})
}

// renameMerge merges the renames of several configurations into one patch, line by line.
type renameMerge struct {
	r        *Runner
	abs      map[string]string          // repository-relative file -> absolute path
	orig     map[string][]string        // file -> original lines
	merged   map[string][]string        // file -> lines after every configuration's edits
	by       map[string]map[int]string  // file -> line index -> configuration that set it
	cfgs     map[string]map[string]bool // file -> configurations that edited it
	conflict map[string]bool
	// read returns a file's current contents; nil reads the disk. The in-process rename gives the contents its
	// edits were made on.
	read func(file string) ([]byte, error)
}

func (r *Runner) newRenameMerge() *renameMerge {
	return &renameMerge{r: r, abs: map[string]string{}, orig: map[string][]string{}, merged: map[string][]string{},
		by: map[string]map[int]string{}, cfgs: map[string]map[string]bool{}, conflict: map[string]bool{}}
}

// add merges one configuration's rename and returns how many lines it was the first to edit.
func (m *renameMerge) add(bc BuildConfig, rp reply) int {
	edits := 0
	for _, abs := range rp.files() {
		rel, inRepo := m.r.rel(abs)
		if !inRepo {
			m.conflict["the rename also edits "+abs+", outside the repository: that edit is not in the patch"] = true
			continue
		}
		if _, ok := m.orig[rel]; !ok {
			read := m.read
			if read == nil {
				read = os.ReadFile
			}
			src, err := read(abs)
			if err != nil {
				m.conflict[rel+": cannot read: "+err.Error()] = true
				continue
			}
			m.abs[rel] = abs
			m.orig[rel] = strings.Split(string(src), "\n")
			m.merged[rel] = append([]string{}, m.orig[rel]...)
			m.by[rel], m.cfgs[rel] = map[int]string{}, map[string]bool{}
		}
		next, err := rp.newLines(abs, m.orig[rel])
		if err != nil || len(next) != len(m.orig[rel]) {
			m.conflict[fmt.Sprintf("%s: %s's edit is not line-for-line; apply that configuration's rename by hand", rel, bc.Name)] = true
			continue
		}
		for i := range next {
			if next[i] == m.orig[rel][i] {
				continue
			}
			if prev, set := m.by[rel][i]; set && m.merged[rel][i] != next[i] {
				m.conflict[fmt.Sprintf("%s:%d: %s and %s edit this line differently", rel, i+1, prev, bc.Name)] = true
				continue
			}
			if _, set := m.by[rel][i]; !set {
				m.by[rel][i], m.merged[rel][i] = bc.Name, next[i]
				edits++
			}
			m.cfgs[rel][bc.Name] = true
		}
	}
	return edits
}

// files returns the files the merged rename edits, sorted.
func (m *renameMerge) files() []string {
	files := make([]string, 0, len(m.merged))
	for f := range m.merged {
		if len(m.by[f]) > 0 {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// finish fills the plan's files, patch, conflicts and leftover mentions.
func (m *renameMerge) finish(plan *RenamePlan) {
	var patch strings.Builder
	changed := map[string]map[int]bool{}
	for _, f := range m.files() {
		rf := RenameFile{File: f}
		changed[f] = map[int]bool{}
		for i := range m.by[f] {
			rf.Lines = append(rf.Lines, i+1)
			changed[f][i+1] = true
		}
		sort.Ints(rf.Lines)
		for _, bc := range m.r.Config.Configs {
			if m.cfgs[f][bc.Name] {
				rf.Configs = append(rf.Configs, bc.Name)
			}
		}
		plan.Files = append(plan.Files, rf)
		writeUnified(&patch, f, m.orig[f], m.merged[f], rf.Lines)
	}
	plan.Patch = patch.String()
	for c := range m.conflict {
		plan.Conflicts = append(plan.Conflicts, c)
	}
	sort.Strings(plan.Conflicts)
	if len(plan.Conflicts) > 0 {
		plan.Incomplete = true
	}
	decls := plan.Decls
	if k := decls[0].Kind; k == "type" || k == "func" || k == "const" || k == "var" {
		plan.Mentions = m.r.mentions(decls[0].QName[strings.LastIndex(decls[0].QName, ".")+1:], changed)
	}
}

func (r *Runner) renamePlan(ctx context.Context, symbol, newName string) (*RenamePlan, error) {
	decls, err := r.resolve(symbol)
	if err != nil {
		return nil, err
	}
	plan := &RenamePlan{Symbol: symbol, NewName: newName, Decls: decls}
	m := r.newRenameMerge()
	if r.useTypes(askRename) {
		r.renameTyped(ctx, plan, m, newName)
		return plan, nil
	}
	plan.Runs, plan.Incomplete = r.each(ctx, decls, askRename, newName,
		func(bc BuildConfig, _ Decl, rp reply) int { return m.add(bc, rp) })
	m.finish(plan)
	return plan, nil
}

// splitDiff splits gopls's unified diff output into the hunk lines of each file, keyed by the +++ path.
func splitDiff(out string) map[string][]string {
	res := map[string][]string{}
	var cur string
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "--- "):
			cur = ""
		case strings.HasPrefix(ln, "+++ "):
			cur = strings.TrimSpace(strings.SplitN(ln[4:], "\t", 2)[0])
			res[cur] = nil
		case cur != "":
			res[cur] = append(res[cur], ln)
		}
	}
	return res
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// applyHunks applies unified-diff hunk lines to orig and returns the result.
func applyHunks(orig, hunks []string) ([]string, error) {
	var out []string
	at := 0 // next unread index in orig
	for i := 0; i < len(hunks); i++ {
		m := hunkRE.FindStringSubmatch(hunks[i])
		if m == nil {
			if strings.TrimSpace(hunks[i]) == "" {
				continue
			}
			return nil, fmt.Errorf("unexpected diff line %q", hunks[i])
		}
		start, _ := strconv.Atoi(m[1])
		if m[2] == "0" {
			start++ // a pure insertion names the line before it
		}
		if start-1 < at || start-1 > len(orig) {
			return nil, fmt.Errorf("hunk at line %d is out of order", start)
		}
		out = append(out, orig[at:start-1]...)
		at = start - 1
		for i+1 < len(hunks) && !strings.HasPrefix(hunks[i+1], "@@") {
			i++
			ln := hunks[i]
			switch {
			case strings.HasPrefix(ln, "+"):
				out = append(out, ln[1:])
			case strings.HasPrefix(ln, "-"):
				if at >= len(orig) || orig[at] != ln[1:] {
					return nil, fmt.Errorf("hunk does not match the file at line %d", at+1)
				}
				at++
			case strings.HasPrefix(ln, " "):
				if at >= len(orig) {
					return nil, fmt.Errorf("hunk runs past the file")
				}
				out = append(out, orig[at])
				at++
			case strings.HasPrefix(ln, `\`), ln == "":
				// "\ No newline at end of file", or the blank after the last hunk
			default:
				return nil, fmt.Errorf("unexpected diff line %q", ln)
			}
		}
	}
	return append(out, orig[at:]...), nil
}

// writeUnified writes a unified diff of one file whose listed lines (1-based, sorted) differ between a and b, which
// have the same length. Paths carry the a/ and b/ prefixes git apply expects.
func writeUnified(w *strings.Builder, file string, a, b []string, lines []int) {
	const ctx = 3
	fmt.Fprintf(w, "--- a/%s\n+++ b/%s\n", file, file)
	// a trailing newline in the file shows as a last empty element; it is not a line
	n := len(a)
	if n > 0 && a[n-1] == "" {
		n--
	}
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
			}
			if a[l-1] != b[l-1] {
				// group a run of changed lines: removals, then additions
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
		}
		i = j + 1
	}
}

// mentions finds lines in .go, .md and .s files that spell name as a whole word and that the rename did not edit.
// The files are read on every processor: a common name is in a thousand of them.
func (r *Runner) mentions(name string, changed map[string]map[int]bool) []Mention {
	var paths []string
	_ = filepath.WalkDir(r.Repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != r.Repo && (d.Name() == "node_modules" || d.Name() == "vendor" || (len(d.Name()) > 1 && d.Name()[0] == '.')) {
				return filepath.SkipDir
			}
			return nil
		}
		// (.s: an assembly file names a Go function it implements, and no type checker reads it)
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".s") {
			paths = append(paths, path)
		}
		return nil
	})
	word := func(b byte) bool {
		return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	}
	needle := []byte(name)
	var mu sync.Mutex
	var out []Mention
	var wg sync.WaitGroup
	next := make(chan string)
	for range min(runtime.GOMAXPROCS(0), max(len(paths), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range next {
				src, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(src, needle) {
					continue
				}
				rel, _ := filepath.Rel(r.Repo, path)
				rel = filepath.ToSlash(rel)
				m := Mention{File: rel}
				line, counted := 1, 0 // the line of offset counted
				for at := 0; ; {
					i := bytes.Index(src[at:], needle)
					if i < 0 {
						break
					}
					start, end := at+i, at+i+len(needle)
					at = end
					if start > 0 && word(src[start-1]) || end < len(src) && word(src[end]) {
						continue // part of a longer word
					}
					line += bytes.Count(src[counted:start], []byte{'\n'})
					counted = start
					if !changed[rel][line] && (len(m.Lines) == 0 || m.Lines[len(m.Lines)-1] != line) {
						m.Lines = append(m.Lines, line)
					}
				}
				if len(m.Lines) > 0 {
					mu.Lock()
					out = append(out, m)
					mu.Unlock()
				}
			}
		}()
	}
	for _, path := range paths {
		next <- path
	}
	close(next)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}
