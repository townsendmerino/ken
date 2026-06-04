package structural

import (
	"path/filepath"

	"github.com/odvcencio/gotreesitter"
)

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
	lc := langCacheFor(gram)
	if lc == nil {
		return nil
	}
	tree, err := lc.pool.Parse(data)
	if err != nil || tree == nil {
		return nil
	}
	// Defense in depth (matches the parseTimeoutMicros=0 rationale in
	// index.go): gotreesitter returns the partially-built tree on every
	// non-accept stop reason — timeout, cancellation, iteration cap,
	// node cap, etc. — with err=nil. Walking that partial tree as if
	// complete is what flakes the determinism contract; reject any
	// tree whose parse didn't run to clean acceptance.
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
