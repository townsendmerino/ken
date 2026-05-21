//go:build bench

package tokens

import (
	"strings"

	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// pathMatches mirrors semble's benchmarks/data.py path_matches: either
// path is a suffix of the other. Handles the common case where qrel
// targets are repo-rooted (`aiohttp/client.py`) but ken's chunk.File
// is benchmark-root-relative (`client.py`) — without this, every
// semble qrel with a directory prefix would fail to match.
//
// Backslashes are normalized to forward slashes so the test works on
// Windows-style paths even though the bench is Unix-only in practice.
func pathMatches(filePath, target string) bool {
	f := strings.ReplaceAll(filePath, "\\", "/")
	t := strings.ReplaceAll(target, "\\", "/")
	return f == t || strings.HasSuffix(f, "/"+t) || strings.HasSuffix(t, "/"+f)
}

// anyTargetMatches reports whether filePath matches any of `targets`
// under pathMatches semantics.
func anyTargetMatches(filePath string, targets []string) bool {
	for _, t := range targets {
		if pathMatches(filePath, t) {
			return true
		}
	}
	return false
}

// Ks are the cutoffs the bench measures per query. Picked to bracket
// the realistic agent budget: K=1 is "did ken nail it instantly?",
// K=10 is "would an agent still find it scrolling through a typical
// top-N result list?" Intermediate values let us see the
// tokens-per-extra-result slope.
var Ks = []int{1, 3, 5, 10}

// KenAtK is one row of the per-query measurement. Tokens counts the
// cl100k_base BPE size of the formatted-output string an agent would
// see over MCP for ken.Search(query, K). Recall is boolean: did the
// top-K contain a chunk whose File matches the qrel target?
type KenAtK struct {
	K      int  `json:"k"`
	Tokens int  `json:"tokens"`
	Recall bool `json:"recall"`
}

// MeasureKen runs ken.Search at every K in Ks for a single query,
// formats each result list via the exact wire format ken-mcp emits
// (mcp.FormatResults — the markdown header + numbered fenced blocks
// agents are trained against in semble's wire protocol), counts
// cl100k_base tokens on the formatted string, and checks recall
// against `targets`.
//
// `targets` is the list of qrel file paths that count as a "hit" for
// the query. Recall@K is true iff any chunk in the top-K has a File
// that matches any target under pathMatches (suffix-aware, mirrors
// semble's qrel matcher).
//
// `header` mirrors what ken-mcp uses ("Search results for: ..."),
// because the agent-input-cost we're measuring includes that header.
func MeasureKen(ix *search.Index, query, header string, targets []string) []KenAtK {
	// Search at max(Ks) once, then slice — Search(q, k) cost grows
	// with k but rebuilding for each K wastes the over-fetch the
	// rerank pipeline already does internally for hybrid mode.
	// Note: this means K=1 tokens DO count just the first result's
	// fenced block, because FormatResults is called on results[:K]
	// below.
	maxK := 0
	for _, k := range Ks {
		if k > maxK {
			maxK = k
		}
	}
	all := ix.Search(query, maxK)

	out := make([]KenAtK, len(Ks))
	for i, k := range Ks {
		if k > len(all) {
			k = len(all)
		}
		topK := all[:k]
		formatted := kenmcp.FormatResults(header, topK)
		out[i] = KenAtK{
			K:      Ks[i],
			Tokens: Count(formatted),
			Recall: anyMatch(topK, targets),
		}
	}
	return out
}

// anyMatch reports whether any chunk in `results` has a File matching
// any path in `targets` (suffix-aware via pathMatches). Stops at the
// first hit.
func anyMatch(results []search.Result, targets []string) bool {
	for _, r := range results {
		if anyTargetMatches(r.Chunk.File, targets) {
			return true
		}
	}
	return false
}
