//go:build bench

package tokens

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/ken/internal/repo"
)

// PerFileTokenCap bounds any single file's contribution to the grep
// baseline. Real agents tokenize a 50 KB Read call cleanly but a 1 MB
// minified bundle isn't going to be Read whole — it'd be Read in
// slices or skipped. Capping each file at 20k tokens reflects what an
// agent would actually consume; pathological files don't get to make
// grep look catastrophic. The cap is documented in BENCH.md.
const PerFileTokenCap = 20000

// GrepResult is one query's grep-fallback measurement: how many
// tokens would the "agent runs grep, Reads matched files whole" path
// burn, and did it cover the qrel target? `MatchedFiles` is reported
// for diagnostic context — a query that matched 14k files explains
// its huge token cost.
type GrepResult struct {
	Tokens       int  `json:"tokens"`
	Recall       bool `json:"recall"`
	MatchedFiles int  `json:"matched_files"`
}

// QueryClass classifies a query as either an identifier lookup
// ("symbol") or a natural-language question ("nl"). The classifier
// is intentionally simple — bench/tokens doesn't need parity with
// semble's resolveAlpha, just a cleanly-binarizable signal so we
// can report per-class numbers.
type QueryClass string

const (
	ClassSymbol QueryClass = "symbol"
	ClassNL     QueryClass = "nl"
)

// ClassifyQuery returns "symbol" for short single-token identifier-
// shaped strings, "nl" for everything else. Heuristic chosen to match
// what a heuristic agent router would use: one word, identifier-shaped
// (alphanumeric + underscore, no spaces, ≤ 30 chars) ⇒ symbol;
// otherwise NL.
func ClassifyQuery(q string) QueryClass {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 30 || strings.ContainsAny(q, " \t") {
		return ClassNL
	}
	for _, r := range q {
		isAlnum := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_'
		if !isAlnum {
			return ClassNL
		}
	}
	return ClassSymbol
}

// CorpusCache holds every indexable file from a corpus pre-read and
// pre-tokenized so grep-baseline measurements across N queries don't
// pay re-read + re-tokenize per query. Build once per benchmark
// corpus.
//
// Memory cost: ≈ 3× corpus byte size (raw + lowercase + small
// token-count metadata). For the CoIR-CSN-Python corpus (~83 MB of
// .py files) that's ~250 MB resident — acceptable for the bench
// harness, which is a one-shot process.
type CorpusCache struct {
	root  string
	files map[string]*cachedFile // rel-path → entry
}

type cachedFile struct {
	lower []byte // lowercased bytes for case-insensitive Contains
	tok   int    // cl100k_base token count of original text
}

// NewCorpusCache walks `root` via repo.Walk (same rules ken uses), reads
// every file, lowercases its bytes, counts cl100k_base tokens, and
// caches the result. Returns an error only on Walk failure; individual
// file read errors are skipped silently (same as MeasureKen's tolerance).
func NewCorpusCache(root string) (*CorpusCache, error) {
	files, err := repo.Walk(repo.Options{Root: root})
	if err != nil {
		return nil, err
	}
	c := &CorpusCache{root: root, files: make(map[string]*cachedFile, len(files))}
	for _, rel := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		c.files[rel] = &cachedFile{
			lower: bytes.ToLower(data),
			tok:   Count(string(data)),
		}
	}
	return c, nil
}

// Len is the number of cached files.
func (c *CorpusCache) Len() int { return len(c.files) }

// MeasureGrepTokenized simulates an agent who tokenizes the query
// identifier-aware (same as ken's BM25 layer), greps each token across
// the corpus, takes the union of matched files, and Reads each one
// whole — capped at PerFileTokenCap per file.
//
// `targets` is the qrel positive set; recall is via pathMatches
// (suffix-aware, mirrors semble's qrel matcher).
func (c *CorpusCache) MeasureGrepTokenized(query string, targets []string) GrepResult {
	terms := bm25.Tokenize(query)
	if len(terms) == 0 {
		return GrepResult{}
	}
	lowered := make([][]byte, len(terms))
	for i, t := range terms {
		lowered[i] = []byte(strings.ToLower(t))
	}
	matched := make(map[string]bool)
	for rel, cf := range c.files {
		for _, t := range lowered {
			if bytes.Contains(cf.lower, t) {
				matched[rel] = true
				break
			}
		}
	}
	return c.scoreMatchedSet(matched, targets)
}

// MeasureGrepLiteral simulates the symbol-query best case: an agent
// who greps for the exact query string verbatim and Reads matched
// files whole. Realistic for "find `validateToken`" style lookups; the
// fewest-tokens grep can plausibly do without semantic help.
func (c *CorpusCache) MeasureGrepLiteral(query string, targets []string) GrepResult {
	q := strings.TrimSpace(query)
	if q == "" {
		return GrepResult{}
	}
	needle := []byte(strings.ToLower(q))
	matched := make(map[string]bool)
	for rel, cf := range c.files {
		if bytes.Contains(cf.lower, needle) {
			matched[rel] = true
		}
	}
	return c.scoreMatchedSet(matched, targets)
}

func (c *CorpusCache) scoreMatchedSet(matched map[string]bool, targets []string) GrepResult {
	var totalTokens int
	for rel := range matched {
		cf := c.files[rel]
		t := cf.tok
		if t > PerFileTokenCap {
			t = PerFileTokenCap
		}
		totalTokens += t
	}
	recall := false
	for rel := range matched {
		if anyTargetMatches(rel, targets) {
			recall = true
			break
		}
	}
	return GrepResult{
		Tokens:       totalTokens,
		Recall:       recall,
		MatchedFiles: len(matched),
	}
}
