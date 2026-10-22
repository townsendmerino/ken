package structural

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/gotreesitter"

	"github.com/townsendmerino/ken/internal/bytesize"
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

// DefaultMaxStructuralFileBytes is the structural index's per-file size
// ceiling when neither BuildOptions.MaxFileBytes nor
// KEN_STRUCTURAL_MAX_FILE_BYTES sets one: 1 MiB. It is separate from
// maxEnrichBytes (64 KiB), which bounds only the Arm B enrichment parse and
// stays fixed so the label can't change; before the two were split, a
// 96 KB Go file's functions and types were missing from Definition,
// Outline and SymbolAt entirely. The walker's own KEN_MAX_FILE_BYTES
// (default 2 MiB) still applies first.
const DefaultMaxStructuralFileBytes int64 = 1 << 20

// ResolveMaxStructuralFileBytes returns the structural index's size cap:
// opt if it is > 0, else KEN_STRUCTURAL_MAX_FILE_BYTES (a byte count, with
// an optional KiB/MiB/KB/MB suffix; invalid or non-positive values are
// ignored, as with KEN_MAX_FILE_BYTES), else DefaultMaxStructuralFileBytes.
func ResolveMaxStructuralFileBytes(opt int64) int64 {
	if opt > 0 {
		return opt
	}
	if raw := os.Getenv("KEN_STRUCTURAL_MAX_FILE_BYTES"); raw != "" {
		if n, ok := bytesize.Parse(raw); ok && n > 0 {
			return n
		}
	}
	return DefaultMaxStructuralFileBytes
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
// the ceiling is no longer the crash safeguard, just a cheap bound on parse
// cost. Not a formal depth bound — gotreesitter still exposes no node/depth
// cap (only a wall-clock timeout). The ceiling differs by caller: enrichment
// (extractGuarded, ExtractFile) uses the fixed 64 KiB maxEnrichBytes, so the
// Arm B label never changes; the structural index (Build, Update) passes its
// own, 1 MiB by default (ResolveMaxStructuralFileBytes), through
// extractGuardedCap. The 24 files over 64 KiB in goinfer (66–223 KB, Go and
// Python) parsed in 3–250 ms each when the split was made.
//
// Guard 2 — parse acceptance. gotreesitter returns the partially-built tree
// on every non-accept stop reason (timeout, cancellation, iteration cap,
// node cap) with err=nil. Walking that partial tree as if complete flakes
// the determinism contract; reject any tree whose parse didn't run to clean
// acceptance.
func extractGuarded(gram, rel string, data []byte) *FileStruct {
	return extractGuardedCap(gram, rel, data, maxEnrichBytes)
}

// extractGuardedCap is extractGuarded with an explicit size ceiling: the
// enrichment path passes maxEnrichBytes, the structural index its own
// (ResolveMaxStructuralFileBytes). Everything else — the parse budget, the
// acceptance check — is shared.
func extractGuardedCap(gram, rel string, data []byte, maxBytes int64) *FileStruct {
	if int64(len(data)) > maxBytes {
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
	if extractCheck != nil {
		extractCheck(gram, data, fs)
	}
	return fs
}

// extractCheck, when set, sees every FileStruct extractGuarded produces,
// with the source it came from. Test-only (spans_test.go's TestMain uses
// it to check every span every extractor test produces); nil in
// production.
var extractCheck func(gram string, data []byte, fs *FileStruct)
