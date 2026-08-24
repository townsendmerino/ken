//go:build bench

// Package temporal measures whether retrieval quality survives the
// codebase MOVING — the regime a coding agent actually works in, where
// it edits the repo mid-session and watch mode re-indexes behind it.
//
// Why (docs/internal/rag-thread-followups.md item 3): a static
// relevance benchmark scores one frozen snapshot. It cannot see whether
// a query still finds its answer after a rename, a moved function, or a
// split file. The rename case is the pointed one — post-rename the
// lexical arm loses its exact-match anchor entirely and the semantic arm
// has to carry the query, which is a direct measurement of what the
// +0.13 semantic recall lift is FOR.
//
// This file is the mutation engine: text-level rewrites with a known
// ground-truth mapping, applied to a throwaway copy of a corpus repo.
// Deliberately not gold-plated — these are not refactoring tools and
// make no attempt to keep code compilable. They only need to be
// REPRESENTATIVE of how source moves and to report exactly what they
// changed, so the qrel remap can follow.
package temporal

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/townsendmerino/aikit/bm25"
)

// Mutation is one applied change plus the ground truth needed to
// re-score queries against the mutated tree.
type Mutation struct {
	// Kind is "rename", "move", or "split".
	Kind string `json:"kind"`

	// Symbol is the identifier renamed (rename) or the function moved
	// (move). Empty for split.
	Symbol string `json:"symbol,omitempty"`

	// NewSymbol is what Symbol became. Empty unless Kind=="rename".
	NewSymbol string `json:"new_symbol,omitempty"`

	// PathMap maps pre-mutation relative paths to where their content
	// now lives. A path absent from the map is unchanged. A split
	// records the ORIGINAL path mapping to the first fragment; Extra
	// lists every fragment, because a qrel target that used to be
	// satisfied by one file may now be satisfied by either.
	PathMap map[string]string `json:"path_map,omitempty"`

	// ExtraPaths lists additional files that now hold content from a
	// mapped path (split fragments, the move destination).
	ExtraPaths map[string][]string `json:"extra_paths,omitempty"`

	// Touched is every file whose bytes changed, for reporting.
	Touched []string `json:"touched"`
}

// Resolve returns the set of post-mutation paths that could satisfy a
// pre-mutation qrel target. Always includes the original path: most
// mutations leave most files alone, and a target the mutation never
// touched must still resolve to itself.
func (m Mutation) Resolve(target string) []string {
	// Deduped: a move records its destination in BOTH PathMap and
	// ExtraPaths (one says "the content went here", the other "this
	// path can also satisfy the target"), and a repeated path would
	// let one file match a qrel twice during scoring.
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(target)
	if mapped, ok := m.PathMap[target]; ok {
		add(mapped)
	}
	for _, p := range m.ExtraPaths[target] {
		add(p)
	}
	return out
}

// DisjointRename returns a replacement identifier that shares NO BM25
// token with symbol.
//
// This is load-bearing, and the obvious approach breaks it. Prefixing
// ("getUser" -> "RenamedgetUser") looks like a rename but ken's
// identifier-aware tokenizer splits both: [getuser get user] vs
// [renamedgetuser renamedget user]. The token `user` survives, so BM25
// keeps an exact-match anchor and the experiment silently measures
// nothing — it reported 0.96 lexical survival, which is the artifact,
// not the finding.
//
// So: derive letters from a hash of the symbol. Letters only, because
// digits are their own tokens; a single opaque run shares nothing with
// any camel/snake decomposition of the original. Deterministic, so a
// rerun mutates identically.
func DisjointRename(symbol string) string {
	sum := sha256.Sum256([]byte(symbol))
	var b strings.Builder
	b.WriteString("Zq")
	for _, c := range sum[:6] {
		b.WriteByte(byte('a' + c%26))
	}
	return b.String()
}

// SharesToken reports whether two identifiers share any BM25 token —
// the check that keeps a "rename" from leaving the lexical arm armed.
func SharesToken(a, b string) bool {
	seen := map[string]bool{}
	for _, tok := range bm25.Tokenize(a) {
		seen[tok] = true
	}
	for _, tok := range bm25.Tokenize(b) {
		if seen[tok] {
			return true
		}
	}
	return false
}

// identRe matches a whole-word identifier occurrence. Word boundaries
// only — a rename must not corrupt `getUserName` while renaming
// `getUser`, which a naive strings.ReplaceAll would do and which would
// silently manufacture "the semantic arm rescued it" results out of a
// broken corpus.
func identRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
}

// ApplyRename rewrites every whole-word occurrence of symbol across the
// tree rooted at dir. This is the mutation that strips the lexical
// arm's anchor: a query naming the old symbol has zero exact matches
// afterwards, so anything still retrieved came from the semantic side.
func ApplyRename(dir, symbol, newSymbol string, files []string) (Mutation, error) {
	if symbol == "" || newSymbol == "" || symbol == newSymbol {
		return Mutation{}, fmt.Errorf("rename: need two distinct non-empty symbols")
	}
	re := identRe(symbol)
	m := Mutation{Kind: "rename", Symbol: symbol, NewSymbol: newSymbol}
	for _, rel := range files {
		abs := filepath.Join(dir, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if !re.Match(data) {
			continue
		}
		out := re.ReplaceAll(data, []byte(newSymbol))
		if err := os.WriteFile(abs, out, 0o644); err != nil {
			return Mutation{}, fmt.Errorf("rename: write %s: %w", rel, err)
		}
		m.Touched = append(m.Touched, rel)
	}
	if len(m.Touched) == 0 {
		return Mutation{}, fmt.Errorf("rename: symbol %q not found in %d files", symbol, len(files))
	}
	sort.Strings(m.Touched)
	return m, nil
}

// ApplyMove relocates the line span [start,end] out of src and appends
// it to a sibling file. The content survives at a different path, which
// is what a qrel remap has to follow: path-based relevance judgments
// are exactly what a move invalidates.
func ApplyMove(dir, src string, start, end int, dst string) (Mutation, error) {
	if start < 1 || end < start {
		return Mutation{}, fmt.Errorf("move: bad span %d-%d", start, end)
	}
	srcAbs := filepath.Join(dir, src)
	data, err := os.ReadFile(srcAbs)
	if err != nil {
		return Mutation{}, fmt.Errorf("move: read %s: %w", src, err)
	}
	lines := strings.Split(string(data), "\n")
	if end > len(lines) {
		return Mutation{}, fmt.Errorf("move: span %d-%d past EOF (%d lines) in %s", start, end, len(lines), src)
	}
	moved := strings.Join(lines[start-1:end], "\n")
	remaining := append(append([]string{}, lines[:start-1]...), lines[end:]...)

	if err := os.WriteFile(srcAbs, []byte(strings.Join(remaining, "\n")), 0o644); err != nil {
		return Mutation{}, fmt.Errorf("move: write %s: %w", src, err)
	}
	dstAbs := filepath.Join(dir, dst)
	existing, _ := os.ReadFile(dstAbs)
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return Mutation{}, err
	}
	if err := os.WriteFile(dstAbs, []byte(body+"\n"+moved+"\n"), 0o644); err != nil {
		return Mutation{}, fmt.Errorf("move: write %s: %w", dst, err)
	}
	return Mutation{
		Kind:       "move",
		PathMap:    map[string]string{src: dst},
		ExtraPaths: map[string][]string{src: {dst}},
		Touched:    []string{src, dst},
	}, nil
}

// ApplySplit cuts a file in half at a line boundary, leaving the head
// at the original path and writing the tail to a sibling. Both halves
// can satisfy a qrel target that named the original, which is why
// ExtraPaths exists.
func ApplySplit(dir, src string, at int) (Mutation, error) {
	abs := filepath.Join(dir, src)
	data, err := os.ReadFile(abs)
	if err != nil {
		return Mutation{}, fmt.Errorf("split: read %s: %w", src, err)
	}
	lines := strings.Split(string(data), "\n")
	if at < 2 || at >= len(lines) {
		return Mutation{}, fmt.Errorf("split: line %d out of range for %s (%d lines)", at, src, len(lines))
	}
	ext := filepath.Ext(src)
	tail := strings.TrimSuffix(src, ext) + "_part2" + ext

	if err := os.WriteFile(abs, []byte(strings.Join(lines[:at], "\n")), 0o644); err != nil {
		return Mutation{}, fmt.Errorf("split: write head %s: %w", src, err)
	}
	if err := os.WriteFile(filepath.Join(dir, tail), []byte(strings.Join(lines[at:], "\n")), 0o644); err != nil {
		return Mutation{}, fmt.Errorf("split: write tail %s: %w", tail, err)
	}
	return Mutation{
		Kind:       "split",
		ExtraPaths: map[string][]string{src: {tail}},
		Touched:    []string{src, tail},
	}, nil
}
