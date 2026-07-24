package structural

import (
	"path/filepath"

	"github.com/odvcencio/gotreesitter"
)

// maxEnrichBytes is the per-file size ceiling for Arm B enrichment. Files
// larger than this skip gotreesitter parsing (see ExtractFile) to avoid
// the GLR stack overflow on pathological inputs. 64 KiB.
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
	ext := filepath.Ext(rel)
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
// Guard 1 — size ceiling. gotreesitter's GLR parser recurses on parse-stack
// depth; for pathological inputs (huge table-driven test files — cobra's
// 117 KB completions_test.go, 80 KB command_test.go) that recursion
// overflows the goroutine stack. A stack overflow is a FATAL runtime error,
// not an error return, so the err guard on Parse below cannot catch it — it
// crashes the whole process. 64 KiB clears every crasher observed on the
// semble corpus while preserving normal source (cobra's 61 KB command.go
// parses fine). Heuristic, not a formal depth bound — gotreesitter exposes
// no node/depth cap (only a wall-clock timeout, which a synchronous stack
// overflow outruns).
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
	tree, err := lc.pool.Parse(data)
	if err != nil || tree == nil {
		return nil
	}
	if r := tree.ParseStopReason(); r != gotreesitter.ParseStopAccepted {
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
