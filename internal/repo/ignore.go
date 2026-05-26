package repo

// Minimal gitignore matcher — the common-subset rule engine that
// powers both the WalkFS scope stack and the Matcher.ShouldIndex
// snapshot (ADR-015). Scope and pathspec-parity caveats live in
// walk.go's package doc.

import (
	"io/fs"
	gopath "path"
	"regexp"
	"strings"
)

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

type gitignore struct{ rules []rule }

// scopedGitignore is a *gitignore plus the directory (slash-separated,
// relative to the FS root; "" for the root .gitignore) whose patterns
// it owns. WalkFS evaluates rules from outer scopes first, inner
// scopes last, last-match-wins across the union of rules. See ADR-015.
type scopedGitignore struct {
	dir string
	gi  *gitignore
}

// loadGitignoreFS reads the .gitignore at `name` (FS-relative path,
// typically "<dir>/.gitignore" or just ".gitignore"). A missing file
// returns an empty *gitignore — that's the common case in any
// directory without ignore rules.
func loadGitignoreFS(fsys fs.FS, name string) *gitignore {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return &gitignore{}
	}
	return parseGitignore(data)
}

// parseGitignore compiles the rules in a .gitignore body.
func parseGitignore(data []byte) *gitignore {
	gi := &gitignore{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if r, ok := compileRule(line); ok {
			gi.rules = append(gi.rules, r)
		}
	}
	return gi
}

// pruneScopes trims `scopes` to those whose dir is an ancestor of (or
// equal to) `path`. Scopes are pushed in DFS order, so the first
// non-applicable scope means every later scope is also non-applicable;
// returning scopes[:i] is sufficient.
func pruneScopes(scopes []scopedGitignore, path string) []scopedGitignore {
	for i, s := range scopes {
		if s.dir == "" || s.dir == path || strings.HasPrefix(path, s.dir+"/") {
			continue
		}
		return scopes[:i]
	}
	return scopes
}

// matchScopes evaluates the rules of every scope against `path`,
// outer-first, inner-last, last-match-wins across the union. Each
// scope's patterns are evaluated relative to its scope.dir. Returns
// true when the path should be ignored.
//
// We deliberately inline the per-rule loop here rather than calling
// (*gitignore).match per scope: that helper resets its `ignored` state
// at every call, so calling it per scope would lose the union
// semantics — an outer "ignore *.log" would be silently forgotten by
// an inner scope that has no matching rule.
func matchScopes(scopes []scopedGitignore, path string, isDir bool) bool {
	ignored := false
	for _, scope := range scopes {
		rel := relToScope(path, scope.dir)
		if rel == "" {
			continue
		}
		for _, r := range scope.gi.rules {
			if r.dirOnly && !isDir {
				continue
			}
			if r.re.MatchString(rel) {
				ignored = !r.negate
			}
		}
	}
	return ignored
}

// relToScope returns path relative to scopeDir (slash-separated), or
// "" if path is not strictly under scopeDir. scopeDir == "" means
// root (every path is "under" root, so path is returned as-is).
func relToScope(path, scopeDir string) string {
	if scopeDir == "" {
		return path
	}
	if path == scopeDir {
		return ""
	}
	if !strings.HasPrefix(path, scopeDir+"/") {
		return ""
	}
	return strings.TrimPrefix(path, scopeDir+"/")
}

// collectGitignores walks fsys once and returns every applicable
// .gitignore in DFS order (outer-first, inner-last). Directories
// pruned by an outer scope or named .git are not descended into, so
// .gitignores buried inside ignored subtrees (e.g. inside a
// gitignored node_modules/) are correctly excluded.
//
// Used by NewMatcher to take a one-shot snapshot of the tree's ignore
// state. WalkFS does the same scope-stack management inline during its
// own walk, so this helper is not used there.
func collectGitignores(fsys fs.FS) []scopedGitignore {
	var collected []scopedGitignore
	var active []scopedGitignore // pruning state during DFS
	if gi := loadGitignoreFS(fsys, ".gitignore"); len(gi.rules) > 0 {
		s := scopedGitignore{dir: "", gi: gi}
		collected = append(collected, s)
		active = append(active, s)
	}
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied on a single subtree shouldn't fail the
			// whole Matcher; skip and continue. Matches the watch path's
			// existing fail-soft posture.
			return nil
		}
		if path == "." {
			return nil
		}
		active = pruneScopes(active, path)
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == ".ken" || matchScopes(active, path, true) {
			return fs.SkipDir
		}
		if gi := loadGitignoreFS(fsys, gopath.Join(path, ".gitignore")); len(gi.rules) > 0 {
			s := scopedGitignore{dir: path, gi: gi}
			collected = append(collected, s)
			active = append(active, s)
		}
		return nil
	})
	return collected
}

func compileRule(pat string) (rule, bool) {
	var r rule
	if strings.HasPrefix(pat, "!") {
		r.negate = true
		pat = pat[1:]
	}
	if strings.HasSuffix(pat, "/") {
		r.dirOnly = true
		pat = strings.TrimSuffix(pat, "/")
	}
	if pat == "" {
		return r, false
	}
	// A slash anywhere (besides the trailing one already stripped) anchors
	// the pattern to the .gitignore location; otherwise it matches a
	// basename at any depth.
	anchored := strings.Contains(pat, "/")
	pat = strings.TrimPrefix(pat, "/")

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < len(pat) && pat[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '\\', '[', ']':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	// Matching a path also ignores everything beneath it (dir contents).
	b.WriteString("(?:/.*)?$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return r, false
	}
	r.re = re
	return r, true
}
