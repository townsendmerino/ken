package mcp

// callers_groups.go — F4: the callers tool groups its results by Go package
// import path (and by module under a go.work that lists several), and other
// languages by directory, so same-named methods in different packages or
// backends can be told apart. Name-resolved, like callers itself: this only
// labels where a site lives, it resolves nothing.

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// goLayout answers "which module and import path is this Go file in" for one
// repo root, reading go.mod and go.work once per directory.
type goLayout struct {
	root        string
	multiModule bool // a go.work at the root lists more than one module
	modByDir    map[string]*goModule
}

type goModule struct {
	dir  string // repo-relative directory of go.mod ("." for the root)
	path string // module path; "" when go.mod has no module line
}

func newGoLayout(root string) *goLayout {
	l := &goLayout{root: root, modByDir: map[string]*goModule{}}
	l.multiModule = countGoWorkUses(filepath.Join(root, "go.work")) > 1
	return l
}

// countGoWorkUses counts the `use` directories of a go.work file, 0 if there
// is none.
func countGoWorkUses(file string) int {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	n, inBlock := 0, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			if line != "" {
				n++
			}
		case line == "use (":
			inBlock = true
		case strings.HasPrefix(line, "use ") && strings.TrimSpace(strings.TrimPrefix(line, "use")) != "(":
			n++
		}
	}
	return n
}

// moduleOf finds the nearest go.mod at or above the repo-relative directory
// dir, within the repo; nil when there is none.
func (l *goLayout) moduleOf(dir string) *goModule {
	if m, ok := l.modByDir[dir]; ok {
		return m
	}
	var m *goModule
	if data, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(dir), "go.mod")); err == nil {
		m = &goModule{dir: dir}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
				m.path = strings.Trim(strings.TrimSpace(rest), `"`)
				break
			}
		}
	} else if dir != "." && dir != "" {
		m = l.moduleOf(path.Dir(dir))
	}
	l.modByDir[dir] = m
	return m
}

// groupOf returns the group a file belongs to: its package import path
// (kind "package", plus the module path under a multi-module go.work) for a
// .go file inside a module, otherwise its directory.
func (l *goLayout) groupOf(file string) (group, kind, module string) {
	dir := path.Dir(file)
	if strings.HasSuffix(file, ".go") {
		if m := l.moduleOf(dir); m != nil && m.path != "" {
			rel := dir
			if m.dir != "." && m.dir != "" {
				rel = strings.TrimPrefix(strings.TrimPrefix(dir, m.dir), "/")
				if dir == m.dir {
					rel = "."
				}
			}
			group = m.path
			if rel != "." && rel != "" {
				group = m.path + "/" + rel
			}
			if l.multiModule {
				module = m.path
			}
			return group, "package", module
		}
	}
	if dir == "." {
		return "(repo root)", "directory", ""
	}
	return dir + "/", "directory", ""
}

// callerGroups splits sites and value uses into groups, sorted by module,
// then group name.
func callerGroups(root string, sites, uses []CallerSiteOut) []CallerGroupOut {
	l := newGoLayout(root)
	type key struct{ group, kind, module string }
	byKey := map[key]*CallerGroupOut{}
	get := func(file string) *CallerGroupOut {
		g, k, m := l.groupOf(file)
		kk := key{g, k, m}
		if byKey[kk] == nil {
			byKey[kk] = &CallerGroupOut{Group: g, Kind: k, Module: m}
		}
		return byKey[kk]
	}
	for _, s := range sites {
		g := get(s.File)
		g.Sites = append(g.Sites, s)
	}
	for _, s := range uses {
		g := get(s.File)
		g.ValueUses = append(g.ValueUses, s)
	}
	out := make([]CallerGroupOut, 0, len(byKey))
	for _, g := range byKey {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Group < out[j].Group
	})
	return out
}
