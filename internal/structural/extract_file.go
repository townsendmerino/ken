package structural

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/gotreesitter"
)

// maxEnrichBytes is the per-file size ceiling for Arm B enrichment. Files
// larger than this skip gotreesitter parsing (see ExtractFile). 64 KiB.
//
// Originally added as the mitigation for the gotreesitter GLR fatal stack
// overflow on large table-driven files (#110). That crash was FIXED upstream
// in gotreesitter v0.20.6 and ken pins v0.48.1 — the exact cobra crashers now
// parse cleanly (see TestParse_LargeTableDrivenGo_NoFatalOverflow and
// docs/internal/upstream-gotreesitter-overflow.md). The ceiling is retained as
// cheap defense-in-depth, not the primary safeguard; it can be raised/removed
// as a retrieval-quality call if enrichment on large files matters more.
const maxEnrichBytes = 64 << 10

// ExtractFile parses a single file's bytes via the gotreesitter
// grammar matching the path's extension and returns the per-file
// FileStruct (functions, classes, calls, imports, raises). Returns
// nil when the extension has no registered extractor or when parsing
// fails — both are silently swallowed because Arm B / production
// callers want a "best-effort per-file structural summary or
// nothing", never a fatal error.
//
// Same per-file work as the first pass of Build, exposed for callers
// that need just one file's structural data without paying for the
// full-corpus walk + cross-file reverse maps. The Stage 8 production
// indexer uses this on every file it ingests to compute the Arm B
// enrichment label without doing a separate full structural.Build
// pass.
//
// Goroutine-safe under the same conditions as Build: langCacheFor
// holds a per-grammar parser pool that owns its internal state.
func ExtractFile(rel string, data []byte) *FileStruct {
	ext := strings.ToLower(filepath.Ext(rel))
	gram, ok := kenLangToTSLang[ext]
	if !ok {
		return nil
	}
	return extractGuarded(gram, rel, data)
}

// extractGuarded is the single guarded parse+extract both structural parse
// paths — ExtractFile (per-file enrichment) and Build (the corpus indexer
// worker) — MUST route through, so the two can't drift on the safety
// guards. It parses data for grammar gram, applies both mandatory guards,
// and runs the language extractor into a fresh FileStruct{Path: rel}.
// Returns nil to skip (oversized, no cache, parse error, non-accept stop
// reason, or nil root). The parse tree stays alive across the extractor
// call because `tree` is reachable until this function returns.
//
// Guard 1 — size ceiling (defense-in-depth). Historically gotreesitter's GLR
// parser could recurse unboundedly in Go result-compatibility normalization on
// huge table-driven files (cobra's 117 KB completions_test.go, 80 KB
// command_test.go), overflowing the goroutine stack — a FATAL runtime error the
// err guard on Parse below cannot catch, crashing the whole process (#110).
// That was FIXED upstream in gotreesitter v0.20.6 (ken pins v0.48.1; the exact
// crashers now parse clean — TestParse_LargeTableDrivenGo_NoFatalOverflow), so
// this 64 KiB ceiling is no longer the crash safeguard, just a cheap bound that
// also caps enrichment cost on very large files. Not a formal depth bound —
// gotreesitter still exposes no node/depth cap (only a wall-clock timeout).
//
// Guard 2 — parse acceptance. gotreesitter returns the partially-built tree
// on every non-accept stop reason (timeout, cancellation, iteration cap,
// node cap) with err=nil. Walking that partial tree as if complete flakes
// the determinism contract; reject any tree whose parse didn't run to clean
// acceptance.
func extractGuarded(gram, rel string, data []byte) *FileStruct {
	if len(data) > maxEnrichBytes {
		return nil
	}
	lc := langCacheFor(gram)
	if lc == nil {
		return nil
	}
	start := time.Now()
	tree, err := lc.pool.Parse(data)
	if err != nil || tree == nil {
		return nil
	}
	// parseBudgetForceTimeout is a test seam (normally false): it forces the
	// budget-exhausted branch below without depending on gotreesitter's
	// timing-dependent deadline actually firing.
	forced := parseBudgetForceTimeout.Load()
	if r := tree.ParseStopReason(); r != gotreesitter.ParseStopAccepted || forced {
		// A budget exhaustion (KEN_ENRICH_FILE_BUDGET_MS) surfaces as
		// ParseStopTimeout: count + log it as a distinct, observable skip so a
		// pathological file (the 159× template cliff) is visible, not silently
		// unenriched. Other non-accept reasons stay silent (unchanged).
		if r == gotreesitter.ParseStopTimeout || forced {
			recordParseBudgetSkip(rel, time.Since(start))
		}
		return nil
	}
	root := tree.RootNode()
	if root == nil {
		return nil
	}
	fs := &FileStruct{Path: rel}
	langExtractor[gram](data, root, lc.lang, fs)
	return fs
}
