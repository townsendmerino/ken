package structural

import (
	"sort"
	"strings"
)

// Maxima for the variable-length sections of the label line. Keep
// the prefix bounded even on chunks with hundreds of calls or
// callers; the M0d heur-only ablation showed precision collapses
// when the label tilts past ~25-30% of chunk size.
const (
	maxCallsInLabel    = 8
	maxRaisesInLabel   = 4
	maxImportsInLabel  = 8
	maxCallersInLabel  = 8
	maxSiblingsInLabel = 8
	maxParamsInLabel   = 8
)

// Enrich returns the comment-line label prefix for the file at the
// given path under the given options. Returns an empty string if
// the path is not in the index OR the file has no extractable
// structural facts (which shouldn't happen on the CSN-Python bench
// but is the graceful fallback).
//
// Format mirrors M0d Arm B's Python materializer exactly when opts
// is the zero value, so the Go extractor's output is byte-identical
// to the reference Python output the M0d memo benchmarked. Stage 8
// additive arms enable one of the bool fields below; the label
// grows by appending one extra `| section` per enabled fact-type.
//
// Always-present sections (the M0d Arm B baseline):
//
//	# func: <name>
//	  | calls: <comma-sep, max 8>
//	  | raises: <comma-sep, max 4>
//
// Additive sections (one per Stage 8 arm):
//
//	| called by: <comma-sep, max 8>     (opts.Callers)
//	| imports: <comma-sep, max 8>       (opts.Imports)
//	| params: <comma-sep, max 8> | returns: <T>   (opts.Signature)
//	| siblings: <comma-sep, max 8>      (opts.Siblings)
//
// Empty sections are omitted entirely (e.g. a function with no
// raises produces no `raises:` clause). The trailing newline is
// included so callers can prepend the result to a chunk with a
// plain string concatenation.
func (ix *Index) Enrich(filePath string, opts EnrichOptions) string {
	fs := ix.files[filePath]
	if fs == nil {
		return ""
	}
	// Delegate to the per-FileStruct path so both index-backed callers
	// (Track 2 tools, materializers) AND index-free callers (the
	// production indexer's chunk-pipeline enrichment, which doesn't
	// build a full cross-corpus Index) compute the SAME label from the
	// SAME code. opts.Callers needs the cross-file reverse-call map and
	// is plumbed via a callback so EnrichFromFileStruct stays
	// index-free.
	return enrichCore(fs, opts, func(name string) []string {
		return ix.callersOf(name, filePath)
	})
}

// EnrichFromFileStruct is the index-free per-FileStruct enrichment
// path. Same Arm B label format as (Index).Enrich. opts.Callers is a
// no-op here (the called-by reverse map needs a cross-file Index);
// callers that want the called-by section must use the (Index).Enrich
// path with a built full Index. M0e ranked called-by negatively
// anyway — Arm B baseline (Callers=false) is what's shipping.
//
// Used by the Stage 8 production indexer in walkAndChunkFSWithModel:
// per-file ExtractFile produces a FileStruct, EnrichFromFileStruct
// turns it into a label, the indexer prepends the label to each
// chunk's Text. No full structural.Build pass needed at index time —
// the structural Index for the Track 2 MCP tools is still built
// separately in ken-mcp's RepoBundle.
func EnrichFromFileStruct(fs *FileStruct, opts EnrichOptions) string {
	if fs == nil {
		return ""
	}
	return enrichCore(fs, opts, nil)
}

// enrichCore is the shared label-formatter. callersResolver may be
// nil — in which case opts.Callers is silently ignored. Single source
// of truth for the Arm B label format; both (Index).Enrich and
// EnrichFromFileStruct route through here so the production indexer
// produces byte-for-byte the same prefix as the bench materializers
// did.
func enrichCore(fs *FileStruct, opts EnrichOptions, callersResolver func(name string) []string) string {
	var parts []string

	// === Arm B baseline: func, calls, raises ===
	primaryFunc := primaryFuncName(fs)
	if primaryFunc != "" {
		parts = append(parts, "func: "+primaryFunc)
	}
	// Use CalleeNames() rather than ranging CallRefs directly: the
	// label must be byte-identical to the pre-Phase-0 output (deduped,
	// first-appearance order) so Arm B retrieval doesn't move.
	if names := fs.CalleeNames(); len(names) > 0 {
		parts = append(parts, "calls: "+trimAndJoin(names, maxCallsInLabel))
	}
	if len(fs.Raises) > 0 {
		parts = append(parts, "raises: "+trimAndJoin(fs.Raises, maxRaisesInLabel))
	}

	// === Stage 8 additive arms ===
	if opts.Callers && callersResolver != nil && primaryFunc != "" {
		if callers := callersResolver(primaryFunc); len(callers) > 0 {
			parts = append(parts, "called by: "+trimAndJoin(callers, maxCallersInLabel))
		}
	}
	if opts.Imports && len(fs.Imports) > 0 {
		parts = append(parts, "imports: "+trimAndJoin(fs.Imports, maxImportsInLabel))
	}
	if opts.Signature && primaryFunc != "" {
		fn := primaryFuncDef(fs)
		if fn != nil {
			if len(fn.Params) > 0 {
				parts = append(parts, "params: "+trimAndJoin(fn.Params, maxParamsInLabel))
			}
			if fn.ReturnType != "" {
				parts = append(parts, "returns: "+strings.TrimSpace(fn.ReturnType))
			}
		}
	}
	if opts.Siblings && primaryFunc != "" {
		if sibs := siblingMethods(fs, primaryFunc); len(sibs) > 0 {
			parts = append(parts, "siblings: "+trimAndJoin(sibs, maxSiblingsInLabel))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return "# " + strings.Join(parts, " | ") + "\n"
}

// primaryFuncName returns the file's identifying function — the
// first top-level FunctionDef in file order, or the first method
// of the first class if the file is class-only. For CSN-Python
// (one function per file) this is unambiguous; for richer corpora
// it's a heuristic that matches the M0d Python materializer.
func primaryFuncName(fs *FileStruct) string {
	for _, fn := range fs.Functions {
		if !fn.IsMethod {
			return fn.Name
		}
	}
	// Fall back to the first method (CSN-Python sometimes has a
	// single class with a single method as the corpus doc).
	if len(fs.Functions) > 0 {
		return fs.Functions[0].Name
	}
	return ""
}

// primaryFuncDef returns the FuncDef pointer for primaryFuncName.
// nil if the file has no functions.
func primaryFuncDef(fs *FileStruct) *FuncDef {
	for i := range fs.Functions {
		if !fs.Functions[i].IsMethod {
			return &fs.Functions[i]
		}
	}
	if len(fs.Functions) > 0 {
		return &fs.Functions[0]
	}
	return nil
}

// callersOf returns the set of file basenames (without extension)
// that call `name`, excluding the file the call is being labeled
// for. Sorted lexically for stable label output. The basename
// strip makes the resulting label tokens hit the BM25 tokenizer
// the same way the corpus's `<doc_id>.py` filenames do — i.e.,
// "called by: q265644, q265700" rather than "called by:
// q265644.py, q265700.py" which would BM25-split into the .py
// stopword on every entry.
func (ix *Index) callersOf(name, excludeFile string) []string {
	sites := ix.callers[name]
	if len(sites) == 0 {
		return nil
	}
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		if s.File == excludeFile {
			continue
		}
		// Strip the trailing `.py` (or any extension) to match
		// the doc_id form the bench harness uses for joins.
		base := s.File
		if dot := strings.LastIndex(base, "."); dot > 0 {
			base = base[:dot]
		}
		// Strip any leading directory components — Stage 8's
		// label is a per-file annotation, not a full path
		// reference.
		if slash := strings.LastIndex(base, "/"); slash >= 0 {
			base = base[slash+1:]
		}
		out = append(out, base)
	}
	sort.Strings(out)
	return out
}

// siblingMethods returns the names of other methods in the same
// enclosing class as `funcName`. Empty if funcName is a top-level
// function (no class to be a sibling of) or the only method of
// its class.
func siblingMethods(fs *FileStruct, funcName string) []string {
	// Find the class that contains funcName, if any.
	var enclosing string
	for _, fn := range fs.Functions {
		if fn.Name == funcName && fn.IsMethod {
			enclosing = fn.EnclosingClass
			break
		}
	}
	if enclosing == "" {
		return nil
	}
	// Collect sibling method names from that class.
	var out []string
	for _, cls := range fs.Classes {
		if cls.Name != enclosing {
			continue
		}
		for _, m := range cls.Methods {
			if m.Name != funcName {
				out = append(out, m.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}
