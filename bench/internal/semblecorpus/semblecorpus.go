//go:build bench

// Package semblecorpus loads the semble benchmark's repo list and
// per-repo query annotations.
//
// Extracted from bench/tokens when bench/temporal became the third
// consumer. The shapes here mirror semble's own benchmarks/repos.json
// and benchmarks/annotations/<repo>.json exactly; nothing is derived or
// reinterpreted, because the whole value of scoring against semble is
// that its inputs are the reference.
//
// Bootstrap (docs/BENCH.md): clone semble, then run its
// benchmarks/sync_repos.py to populate ~/.cache/semble-bench.
package semblecorpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Repo is one row of semble's benchmarks/repos.json.
type Repo struct {
	Name          string `json:"name"`
	Language      string `json:"language"`
	URL           string `json:"url"`
	Revision      string `json:"revision"`
	BenchmarkRoot string `json:"benchmark_root"`
}

// Target is one relevance judgment. semble writes these in TWO shapes —
// a bare path string, or an object with an optional line span (its
// benchmarks/data.py Target dataclass) — and both appear in the same
// annotations directory. A loader that assumes strings unmarshals
// nothing and silently drops the whole repo, which reads as "that repo
// had no queries" rather than as a bug.
type Target struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// UnmarshalJSON accepts either form.
func (t *Target) UnmarshalJSON(data []byte) error {
	var asPath string
	if err := json.Unmarshal(data, &asPath); err == nil {
		t.Path = asPath
		return nil
	}
	// Alias to avoid recursing back into this method.
	type raw Target
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("target is neither a path string nor an object: %w", err)
	}
	*t = Target(r)
	return nil
}

// Task is one annotated query: the query text plus the targets that
// count as correct answers.
type Task struct {
	Query     string   `json:"query"`
	Relevant  []Target `json:"relevant"`
	Secondary []Target `json:"secondary"`
	Category  string   `json:"category"`
}

// AllRelevant is Relevant plus Secondary — semble's own notion of "any
// of these satisfies the query", which its metric scores against.
// Returns paths; the line spans are carried on Target for callers that
// want them.
func (t Task) AllRelevant() []string {
	out := make([]string, 0, len(t.Relevant)+len(t.Secondary))
	for _, g := range append(append([]Target{}, t.Relevant...), t.Secondary...) {
		if g.Path != "" {
			out = append(out, g.Path)
		}
	}
	return out
}

// Paths resolves the semble checkout and the synced corpus root from
// the environment, with the same defaults every other harness uses.
func Paths() (semblePath, corpusRoot string) {
	semblePath = os.Getenv("SEMBLE_CHECKOUT")
	if semblePath == "" {
		semblePath = "/tmp/semble"
	}
	corpusRoot = os.Getenv("KEN_SEMBLE_CORPUS_ROOT")
	if corpusRoot == "" {
		corpusRoot = filepath.Join(os.Getenv("HOME"), ".cache", "semble-bench")
	}
	return semblePath, corpusRoot
}

// LoadRepos reads benchmarks/repos.json from a semble checkout.
func LoadRepos(semblePath string) ([]Repo, error) {
	path := filepath.Join(semblePath, "benchmarks", "repos.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Repo
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// LoadTasks reads one repo's annotation file. A repo with no
// annotations returns an error rather than an empty slice — silently
// scoring zero queries would look like a passing run.
func LoadTasks(semblePath, repoName string) ([]Task, error) {
	path := filepath.Join(semblePath, "benchmarks", "annotations", repoName+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Task
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no tasks", path)
	}
	return out, nil
}

// Dir returns the directory a repo's queries are scored against:
// the checkout root, or its BenchmarkRoot subdirectory when set.
func (r Repo) Dir(corpusRoot string) string {
	dir := filepath.Join(corpusRoot, r.Name)
	if r.BenchmarkRoot != "" {
		dir = filepath.Join(dir, filepath.FromSlash(r.BenchmarkRoot))
	}
	return dir
}

// PathMatches mirrors semble's benchmarks/data.py path_matches: either
// path is a suffix of the other. Handles qrel targets being repo-rooted
// (`aiohttp/client.py`) while ken's chunk.File is benchmark-root-
// relative (`client.py`) — without it every target with a directory
// prefix fails to match.
func PathMatches(filePath, target string) bool {
	f, t := normalizeSlashes(filePath), normalizeSlashes(target)
	return f == t || hasSuffix(f, "/"+t) || hasSuffix(t, "/"+f)
}

func normalizeSlashes(s string) string {
	out := make([]byte, len(s))
	for i := range len(s) {
		if s[i] == '\\' {
			out[i] = '/'
			continue
		}
		out[i] = s[i]
	}
	return string(out)
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
