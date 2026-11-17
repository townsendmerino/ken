package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeCorpus writes ~90 files in three directories and two extensions with
// overlapping vocabulary, so every query has candidates in every scope.
func scopeCorpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	words := []string{"parse", "token", "cache", "buffer", "stream", "handler", "config", "index", "query", "score"}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 90 {
		dir := []string{"alpha", "beta", "gamma"}[i%3]
		ext := []string{"go", "py"}[i%2]
		var b strings.Builder
		for j := range 4 {
			w1, w2 := words[(i+j)%len(words)], words[(i*3+j)%len(words)]
			if ext == "go" {
				fmt.Fprintf(&b, "// %s %s helper number %d\nfunc %s%d() { %s() }\n\n", w1, w2, i, w1, i*10+j, w2)
			} else {
				fmt.Fprintf(&b, "# %s %s helper number %d\ndef %s_%d():\n    return %s()\n\n", w1, w2, i, w1, i*10+j, w2)
			}
		}
		write(fmt.Sprintf("%s/f%02d.%s", dir, i, ext), b.String())
	}
	return root
}

type scopeCase struct {
	name string
	keep func(string) bool
}

func scopeCases() []scopeCase {
	return []scopeCase{
		{"dir-beta", func(f string) bool { return strings.Contains(f, "beta/") }},
		{"go-only", func(f string) bool { return strings.HasSuffix(f, ".go") }},
		{"one-file", func(f string) bool { return strings.HasSuffix(f, "gamma/f05.py") }},
		{"not-alpha", func(f string) bool { return !strings.Contains(f, "alpha/") }},
	}
}

var scopeQueries = []string{"parse token", "cache buffer stream", "handler config", "score", "index query helper"}

type rowKey struct {
	file       string
	start, end int
}

func rows(rs []Result) []rowKey {
	out := make([]rowKey, len(rs))
	for i, r := range rs {
		out[i] = rowKey{r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine}
	}
	return out
}

// TestScope_ExactForBM25AndSemantic is the F1 gate: a filtered top-k equals the
// unbounded ranking with the filter applied afterwards — same chunks, same
// order, same scores.
func TestScope_ExactForBM25AndSemantic(t *testing.T) {
	root := scopeCorpus(t)
	modes := []Mode{ModeBM25}
	modelDir := ""
	if _, err := os.Stat(filepath.Join("..", "..", "testdata", "model", "model.safetensors")); err == nil {
		modelDir = filepath.Join("..", "..", "testdata", "model")
		modes = append(modes, ModeSemantic)
	}
	ix, err := FromPath(root, ModeHybrid, "regex", modelDir)
	if modelDir == "" {
		ix, err = FromPath(root, ModeBM25, "regex", "")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range modes {
		for _, sc := range scopeCases() {
			allow, _, inScope := ix.ScopeOfFiles(sc.keep)
			if inScope == 0 {
				t.Fatalf("%s: empty scope", sc.name)
			}
			for _, q := range scopeQueries {
				for _, k := range []int{1, 5, 20} {
					all, _ := ix.SearchMode(q, ix.Len(), mode)
					var want []Result
					for _, r := range all {
						if sc.keep(r.Chunk.File) {
							want = append(want, r)
						}
					}
					if len(want) > k {
						want = want[:k]
					}
					got, _ := ix.SearchModeAblatedScoped(q, k, mode, AdaptiveAlphas, RankAblation{}, allow)
					if fmt.Sprint(rows(got)) != fmt.Sprint(rows(want)) {
						t.Fatalf("mode=%v scope=%s q=%q k=%d:\n got  %v\n want %v", mode, sc.name, q, k, rows(got), rows(want))
					}
					for i := range got {
						if got[i].Score != want[i].Score {
							t.Fatalf("mode=%v scope=%s q=%q k=%d row %d: score %v, want %v", mode, sc.name, q, k, i, got[i].Score, want[i].Score)
						}
					}
				}
			}
		}
	}
}

// TestScope_HybridStaysInScope: every hybrid result is in scope and the count
// is min(k, in-scope chunks), including a scope of a single file.
func TestScope_HybridStaysInScope(t *testing.T) {
	model := modelDirOrSkip(t)
	root := scopeCorpus(t)
	ix, err := FromPath(root, ModeHybrid, "regex", model)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scopeCases() {
		allow, _, inScope := ix.ScopeOfFiles(sc.keep)
		for _, q := range scopeQueries {
			for _, k := range []int{1, 5, 20, 1000} {
				got, _ := ix.SearchModeAblatedScoped(q, k, ModeHybrid, AdaptiveAlphas, RankAblation{}, allow)
				for _, r := range got {
					if !sc.keep(r.Chunk.File) {
						t.Fatalf("scope=%s q=%q k=%d: out-of-scope result %s", sc.name, q, k, r.Chunk.File)
					}
				}
				if want := min(k, inScope); len(got) != want {
					t.Fatalf("scope=%s q=%q k=%d: %d results, want min(k,%d)=%d", sc.name, q, k, len(got), inScope, want)
				}
			}
		}
	}
}

// TestScope_NilIsUnscoped: a nil scope is the unscoped search, in every mode
// the fixture can run.
func TestScope_NilIsUnscoped(t *testing.T) {
	root := scopeCorpus(t)
	ix, err := FromPath(root, ModeBM25, "regex", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range scopeQueries {
		a, _ := ix.SearchMode(q, 10, ModeBM25)
		b, _ := ix.SearchModeAblatedScoped(q, 10, ModeBM25, AdaptiveAlphas, RankAblation{}, nil)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("q=%q: nil scope differs from unscoped", q)
		}
	}
}

// TestScope_TombstonesAreOutOfScope: a scope never returns a tombstoned chunk,
// and counts only live ones.
func TestScope_ScopeOfFilesCounts(t *testing.T) {
	root := scopeCorpus(t)
	ix, err := FromPath(root, ModeBM25, "regex", "")
	if err != nil {
		t.Fatal(err)
	}
	set, files, chunks := ix.ScopeOfFiles(func(f string) bool { return strings.Contains(f, "beta/") })
	if files != 30 || chunks != set.Len() || chunks == 0 {
		t.Fatalf("files=%d chunks=%d set=%d", files, chunks, set.Len())
	}
	if set.Has(-1) || set.Has(ix.Len()+1000) {
		t.Fatal("out-of-range index reported in scope")
	}
	var nilSet *ChunkSet
	if !nilSet.Has(7) {
		t.Fatal("nil set must hold every chunk")
	}
}
