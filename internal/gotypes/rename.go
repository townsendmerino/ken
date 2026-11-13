package gotypes

import (
	"context"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RenameFile is one file a rename edits.
type RenameFile struct {
	File    string   `json:"file"`
	Lines   []int    `json:"lines"`
	Configs []string `json:"configs"` // the configurations whose rename edited it
}

// Mention is a file that still spells the old name after the rename, in a comment, a string or prose: nothing a type
// checker treats as a reference.
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
	// Conflicts lists places where two configurations disagree about an edit; the patch carries the first
	// configuration's version there, and the place needs a human.
	Conflicts  []string    `json:"conflicts,omitempty"`
	Mentions   []Mention   `json:"mentions,omitempty"`
	Runs       []ConfigRun `json:"runs"`
	Incomplete bool        `json:"incomplete"`
}

// RenamePlan computes the edits that rename symbol to newName. One configuration's rename leaves stale every file
// that only another configuration compiles, so each is asked and the edits are merged per line. A declaration with
// per-platform twins is renamed at every twin.
func (r *Runner) RenamePlan(ctx context.Context, symbol, newName string) (*RenamePlan, error) {
	if !token.IsIdentifier(newName) {
		return nil, fmt.Errorf("new name %q is not an identifier", newName)
	}
	decls, err := r.resolve(symbol)
	if err != nil {
		return nil, err
	}
	plan := &RenamePlan{Symbol: symbol, NewName: newName, Decls: decls}
	orig := map[string][]string{}        // file -> original lines
	merged := map[string][]string{}      // file -> lines after every configuration's edits
	by := map[string]map[int]string{}    // file -> line index -> configuration that set it
	cfgs := map[string]map[string]bool{} // file -> configurations that edited it
	conflict := map[string]bool{}
	plan.Runs, plan.Incomplete = r.each(ctx, decls, []string{"rename", "-d"}, []string{newName},
		func(bc BuildConfig, _ Decl, stdout []byte) int {
			edits := 0
			for abs, hunks := range splitDiff(string(stdout)) {
				rel, err := filepath.Rel(r.Repo, abs)
				if err != nil || strings.HasPrefix(rel, "..") {
					continue
				}
				rel = filepath.ToSlash(rel)
				if _, ok := orig[rel]; !ok {
					src, err := os.ReadFile(abs)
					if err != nil {
						conflict[rel+": cannot read: "+err.Error()] = true
						continue
					}
					orig[rel] = strings.Split(string(src), "\n")
					merged[rel] = append([]string{}, orig[rel]...)
					by[rel], cfgs[rel] = map[int]string{}, map[string]bool{}
				}
				next, err := applyHunks(orig[rel], hunks)
				if err != nil || len(next) != len(orig[rel]) {
					conflict[fmt.Sprintf("%s: %s's edit is not line-for-line; apply that configuration's rename by hand", rel, bc.Name)] = true
					continue
				}
				for i := range next {
					if next[i] == orig[rel][i] {
						continue
					}
					if prev, set := by[rel][i]; set && merged[rel][i] != next[i] {
						conflict[fmt.Sprintf("%s:%d: %s and %s edit this line differently", rel, i+1, prev, bc.Name)] = true
						continue
					}
					if _, set := by[rel][i]; !set {
						by[rel][i], merged[rel][i] = bc.Name, next[i]
						edits++
					}
					cfgs[rel][bc.Name] = true
				}
			}
			return edits
		})
	files := make([]string, 0, len(merged))
	for f := range merged {
		if len(by[f]) > 0 {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	var patch strings.Builder
	changed := map[string]map[int]bool{}
	for _, f := range files {
		rf := RenameFile{File: f}
		changed[f] = map[int]bool{}
		for i := range by[f] {
			rf.Lines = append(rf.Lines, i+1)
			changed[f][i+1] = true
		}
		sort.Ints(rf.Lines)
		for _, bc := range r.Config.Configs {
			if cfgs[f][bc.Name] {
				rf.Configs = append(rf.Configs, bc.Name)
			}
		}
		plan.Files = append(plan.Files, rf)
		writeUnified(&patch, f, orig[f], merged[f], rf.Lines)
	}
	plan.Patch = patch.String()
	for c := range conflict {
		plan.Conflicts = append(plan.Conflicts, c)
	}
	sort.Strings(plan.Conflicts)
	if len(plan.Conflicts) > 0 {
		plan.Incomplete = true
	}
	if k := decls[0].Kind; k == "type" || k == "func" || k == "const" || k == "var" {
		plan.Mentions = r.mentions(decls[0].QName[strings.LastIndex(decls[0].QName, ".")+1:], changed)
	}
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

// mentions finds lines in .go and .md files that spell name as a whole word and that the rename did not edit.
func (r *Runner) mentions(name string, changed map[string]map[int]bool) []Mention {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	var out []Mention
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
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(src), name) {
			return nil
		}
		rel, _ := filepath.Rel(r.Repo, path)
		rel = filepath.ToSlash(rel)
		m := Mention{File: rel}
		for i, ln := range strings.Split(string(src), "\n") {
			if re.MatchString(ln) && !changed[rel][i+1] {
				m.Lines = append(m.Lines, i+1)
			}
		}
		if len(m.Lines) > 0 {
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}
