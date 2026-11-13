package gotypes

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Group is one row of a grouped result: how many of the result's lines fall in the group, split by whether the file
// is a test file (its name ends _test.go).
type Group struct {
	Name    string `json:"group"`
	NonTest int    `json:"non_test"`
	Test    int    `json:"test"`
	// Direct counts the group's lines that name the declaration itself, when the result is Labelled; the rest name
	// a corresponding method.
	Direct int `json:"direct,omitempty"`
}

// GroupKeys are the values GroupBy takes.
var GroupKeys = []string{"package", "dir", "file"}

// GroupBy replaces the result's hits with counts, one Group per package, directory or file, largest first. It is for
// a question about coupling, where the counts are the answer and the lines are noise. Total, Files, Runs and
// Incomplete are unchanged.
//
// "dir" is the file's directory. "package" is the Go package: the directory, with an external test package
// (package x_test) counted with the package it tests, and with the package name added when the directory name is not
// the package's (cmd/ken (main)), so a directory that holds two packages is two rows.
func (r *Result) GroupBy(by string) error {
	key, err := groupKey(by)
	if err != nil {
		return err
	}
	at := map[string]int{}
	var groups []Group
	for _, h := range r.Hits {
		k := key(h)
		i, ok := at[k]
		if !ok {
			i = len(groups)
			at[k] = i
			groups = append(groups, Group{Name: k})
		}
		if isTestFile(h.File) {
			groups[i].Test++
		} else {
			groups[i].NonTest++
		}
		if r.Labelled && h.Through == "" {
			groups[i].Direct++
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if a, b := groups[i].NonTest+groups[i].Test, groups[j].NonTest+groups[j].Test; a != b {
			return a > b
		}
		return groups[i].Name < groups[j].Name
	})
	r.GroupedBy, r.Groups, r.Hits = by, groups, nil
	return nil
}

func groupKey(by string) (func(Hit) string, error) {
	switch by {
	case "file":
		return func(h Hit) string { return h.File }, nil
	case "dir":
		return func(h Hit) string { return path.Dir(h.File) }, nil
	case "package":
		return func(h Hit) string {
			dir := path.Dir(h.File)
			if name := strings.TrimSuffix(h.pkg, "_test"); name != "" && name != path.Base(dir) {
				return dir + " (" + name + ")"
			}
			return dir
		}, nil
	}
	return nil, fmt.Errorf("group_by %q: want one of %s", by, strings.Join(GroupKeys, ", "))
}

func isTestFile(file string) bool { return strings.HasSuffix(file, "_test.go") }

// groupLines writes one aligned row per group; maxRows caps them (0 means all).
func groupLines(b *strings.Builder, groups []Group, maxRows int, labelled bool) {
	shown := groups
	if maxRows > 0 && len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	w, nw, tw := 0, 1, 1
	for _, g := range shown {
		w = max(w, len(g.Name))
		nw = max(nw, len(fmt.Sprint(g.NonTest)))
		tw = max(tw, len(fmt.Sprint(g.Test)))
	}
	for _, g := range shown {
		fmt.Fprintf(b, "%-*s  %*d non-test  %*d test", w, g.Name, nw, g.NonTest, tw, g.Test)
		if labelled {
			fmt.Fprintf(b, "  (%d direct)", g.Direct)
		}
		b.WriteByte('\n')
	}
	if len(shown) < len(groups) {
		fmt.Fprintf(b, "… %d more groups not shown\n", len(groups)-len(shown))
	}
}

// DirectOnly drops the hits that do not name the declaration itself (Hit.Through), and brings Total and Files down to
// what is left. It applies to a Labelled result before GroupBy; on any other it reports an error and changes nothing.
func (r *Result) DirectOnly() error {
	if !r.Labelled {
		return fmt.Errorf("direct uses cannot be told from uses through an interface in this answer: only references answered by the in-process engine are labelled")
	}
	kept := r.Hits[:0:0]
	files := map[string]bool{}
	for _, h := range r.Hits {
		if h.Through == "" {
			kept = append(kept, h)
			files[h.File] = true
		}
	}
	r.Hits, r.Total, r.Files, r.DirectOnlyApplied = kept, len(kept), len(files), true
	return nil
}
