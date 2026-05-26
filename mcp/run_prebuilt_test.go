package mcp

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/ken/internal/search"
)

// corpusWithPrebuilt clones the embeddedCorpus() fixture and adds a
// pre-built index file at the conventional .ken/index.bin path. The
// pre-built bytes are produced from the same corpus, so the resulting
// search behavior should be identical to the no-prebuilt path.
func corpusWithPrebuilt(t *testing.T, mode search.Mode, chunker string) (fstest.MapFS, []byte) {
	t.Helper()
	base := embeddedCorpus()
	data, err := search.BuildAndSerializeIndex(base, search.BuildOptions{
		Mode:    mode,
		Chunker: chunker,
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	withPrebuilt := fstest.MapFS{}
	for k, v := range base {
		withPrebuilt[k] = v
	}
	withPrebuilt[".ken/index.bin"] = &fstest.MapFile{Data: data}
	return withPrebuilt, data
}

// TestRun_NoPrebuiltIndex_FallbackBuildsFromCorpus is the "no
// optimization" baseline: corpus contains no .ken/index.bin file,
// Options.PrebuiltIndex is nil, mcp.Run silently builds from corpus
// (the v0.6.0 behavior). The build-from-corpus path must run and
// the resulting index must be functional; no pre-built warning
// should fire.
//
// M4 (post-v0.8.3 bug review) fix: the original assertion was a
// tautology — at warn level no pre-built log fires regardless of
// what tryLoadPrebuilt did, so the test passed even if the
// pre-built code path were deleted. Now uses LogLevel=info so the
// build-from-corpus "indexed N chunks" log is captured, and
// asserts it positively — failure here means the fallback
// actually didn't run.
func TestRun_NoPrebuiltIndex_FallbackBuildsFromCorpus(t *testing.T) {
	// Sanity: confirm the fixture really has no .ken/index.bin.
	corpus := embeddedCorpus()
	if _, ok := corpus[".ken/index.bin"]; ok {
		t.Fatalf("test invariant: embeddedCorpus() must not contain .ken/index.bin")
	}

	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(corpus, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
		LogLevel:    "info",
		LogWriter:   logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil || ix.Len() == 0 {
		t.Fatalf("index is nil or empty — fallback didn't run?")
	}

	log := logBuf.String()
	// Build-from-corpus path emits "indexed N chunks" at info; the
	// pre-built success path emits "loaded pre-built index". Pin
	// which one fired.
	if !strings.Contains(log, "indexed") {
		t.Errorf("expected build-from-corpus 'indexed N chunks' info log, got:\n%s", log)
	}
	if strings.Contains(log, "loaded pre-built index") {
		t.Errorf("unexpected pre-built load — corpus had no .ken/index.bin:\n%s", log)
	}
	if strings.Contains(log, "failed to load pre-built") {
		t.Errorf("unexpected pre-built warning at info — missing-file path should be silent:\n%s", log)
	}

	// And the index must actually answer queries.
	if results := ix.Search("ValidateUser", 3); len(results) == 0 {
		t.Errorf("fallback index returned no results for ValidateUser")
	}
}

// TestRun_ValidPrebuiltIndex_AutoLoaded embeds a valid pre-built
// index at the conventional path and confirms mcp.Run loads it via
// the auto-discovery path (NOT the explicit Options.PrebuiltIndex
// override). The success info-log includes the src tag so the
// "auto-discovered" path is distinguishable from "Options.PrebuiltIndex"
// — M5 (post-v0.8.3 bug review) regression guard.
func TestRun_ValidPrebuiltIndex_AutoLoaded(t *testing.T) {
	fsys, _ := corpusWithPrebuilt(t, search.ModeBM25, "regex")
	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(fsys, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
		LogLevel:    "info",
		LogWriter:   logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil || ix.Len() == 0 {
		t.Fatalf("index is nil or empty")
	}
	log := logBuf.String()
	if !strings.Contains(log, "loaded pre-built index from") {
		t.Errorf("expected pre-built success log, got:\n%s", log)
	}
	if !strings.Contains(log, "auto-discovered") {
		t.Errorf("expected auto-discovered tag in src, got:\n%s", log)
	}
	if strings.Contains(log, "Options.PrebuiltIndex") {
		t.Errorf("explicit-override tag should NOT appear (no Options.PrebuiltIndex was set), got:\n%s", log)
	}
	// Sanity-check the loaded index is functional.
	results := ix.Search("ValidateUser", 3)
	if len(results) == 0 || !strings.Contains(results[0].Chunk.File, "main.go") {
		t.Errorf("loaded index returned unexpected results: %v", results)
	}
}

// TestRun_ValidExplicitPrebuiltIndex sets Options.PrebuiltIndex
// directly (non-conventional layout) and confirms mcp.Run uses it
// AND that the success log names the explicit-override source (so a
// corpus FS with a conflicting .ken/index.bin would NOT have been
// consulted). M5 (post-v0.8.3 bug review) fix: previously the log
// emitted an identical "loaded pre-built index" string regardless
// of which source fired; now the src tag distinguishes the paths.
func TestRun_ValidExplicitPrebuiltIndex(t *testing.T) {
	// Build once to get the bytes; pass them via Options.PrebuiltIndex.
	data, err := search.BuildAndSerializeIndex(embeddedCorpus(), search.BuildOptions{
		Mode:    search.ModeBM25,
		Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	// IMPORTANT: deliberately corrupt the auto-discovered path's
	// bytes so we can prove explicit-override was the path that
	// fired. If the auto-discovery path had fired instead, the
	// corrupt bytes would have produced a warn-level "failed to
	// load" log; the explicit-override path would succeed cleanly.
	corpus := fstest.MapFS{}
	for k, v := range embeddedCorpus() {
		corpus[k] = v
	}
	corpus[".ken/index.bin"] = &fstest.MapFile{Data: []byte("not a valid index")}

	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(corpus, Options{
		Mode:          "bm25",
		ChunkerName:   "regex",
		PrebuiltIndex: data,
		LogLevel:      "info",
		LogWriter:     logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil || ix.Len() == 0 {
		t.Fatalf("index is nil or empty")
	}
	log := logBuf.String()
	if !strings.Contains(log, "loaded pre-built index from Options.PrebuiltIndex") {
		t.Errorf("expected explicit-override source in log, got:\n%s", log)
	}
	if strings.Contains(log, "auto-discovered") {
		t.Errorf("auto-discovered path should NOT have fired (explicit override was set), got:\n%s", log)
	}
	// And no fallback warning — explicit bytes are valid.
	if strings.Contains(log, "failed to load pre-built") {
		t.Errorf("explicit path should have succeeded; got fallback warning:\n%s", log)
	}
}

// TestRun_CorruptPrebuiltIndex_FallsBackWithWarning embeds a corrupt
// pre-built index and confirms mcp.Run logs a warn-level fallback
// message and still builds the index from corpus.
func TestRun_CorruptPrebuiltIndex_FallsBackWithWarning(t *testing.T) {
	fsys := fstest.MapFS{}
	for k, v := range embeddedCorpus() {
		fsys[k] = v
	}
	// Garbage bytes — fails the magic check.
	fsys[".ken/index.bin"] = &fstest.MapFile{Data: []byte("this is not a valid ken index")}

	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(fsys, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
		LogLevel:    "warn",
		LogWriter:   logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil || ix.Len() == 0 {
		t.Fatalf("index is nil or empty — fallback didn't run?")
	}
	low := strings.ToLower(logBuf.String())
	for _, want := range []string{"failed to load pre-built", "falling back", "build-index"} {
		if !strings.Contains(low, want) {
			t.Errorf("expected %q in warn log, got:\n%s", want, logBuf.String())
		}
	}
	// Confirm the index actually works post-fallback.
	results := ix.Search("ValidateUser", 3)
	if len(results) == 0 {
		t.Errorf("fallback index has no results for ValidateUser")
	}
}

// TestRun_ModeDowngrade_RealignsWithPrebuilt covers the interaction
// between mcp.Run's "downgrade hybrid → bm25 when no model is loaded"
// path (the v0.6.0 first-launch usability promise) and the v0.8.3
// pre-built-index load. The setup:
//
//   - Options.Mode = "hybrid" (the SDK author's intent).
//   - No model configured (no ModelFS, no ModelDir, no testdata/model
//     gating). mcp.Run downgrades hybrid → bm25 with a warn log.
//   - Options.PrebuiltIndex = a bm25 pre-built blob.
//
// The downgraded mode (bm25) re-aligns with the pre-built blob's
// mode (bm25), so ExpectedMode matches and the load succeeds — no
// mismatch warning. This pins the desirable interaction: an SDK
// author who shipped a bm25 pre-built and asks for hybrid on a
// model-less deployment doesn't get a spurious "pre-built mismatch"
// warning on top of the (legitimate) downgrade warning.
//
// The actual "mismatch fires a fallback warning" path is exercised
// by TestRun_ChunkerMismatch_FallsBackWithWarning below — chunker
// mismatch is the easier-to-stage variant because it doesn't
// interact with the model-resolution downgrade.
func TestRun_ModeDowngrade_RealignsWithPrebuilt(t *testing.T) {
	bm25Data, err := search.BuildAndSerializeIndex(embeddedCorpus(), search.BuildOptions{
		Mode:    search.ModeBM25,
		Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{
		Mode:          "hybrid", // requests hybrid; mcp.Run downgrades to bm25 (no model)
		ChunkerName:   "regex",
		PrebuiltIndex: bm25Data, // these ARE bm25 bytes
		LogLevel:      "warn",
		LogWriter:     logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil {
		t.Fatalf("index is nil")
	}
	if strings.Contains(logBuf.String(), "failed to load pre-built") {
		t.Errorf("downgrade path should re-align with pre-built mode; "+
			"got unexpected fallback warning:\n%s", logBuf.String())
	}
	// The downgrade warning IS expected (model-resolution path); the
	// pre-built-mismatch warning is NOT (because bm25 == bm25 after
	// downgrade).
	if !strings.Contains(logBuf.String(), "downgrading to bm25") {
		t.Errorf("expected the model-downgrade warning to fire, got:\n%s", logBuf.String())
	}
}

// TestRun_ChunkerMismatch_FallsBackWithWarning builds the pre-built
// with chunker=regex but runs mcp.Run with ChunkerName="line" (the
// other chunker the mcp package's import set has registered). Both
// are registered, so ValidateEnum lets the value through, and the
// chunker-mismatch error from the load path fires.
func TestRun_ChunkerMismatch_FallsBackWithWarning(t *testing.T) {
	// Build pre-built bytes with chunker=regex.
	data, err := search.BuildAndSerializeIndex(embeddedCorpus(), search.BuildOptions{
		Mode:    search.ModeBM25,
		Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{
		Mode:          "bm25",
		ChunkerName:   "line", // mismatch vs regex pre-built
		PrebuiltIndex: data,
		LogLevel:      "warn",
		LogWriter:     logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil || ix.Len() == 0 {
		t.Fatalf("index is nil or empty — fallback didn't run?")
	}
	if !strings.Contains(logBuf.String(), "failed to load pre-built") {
		t.Errorf("expected fallback warning, got:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "chunker") {
		t.Errorf("warning should mention chunker mismatch, got:\n%s", logBuf.String())
	}
}
