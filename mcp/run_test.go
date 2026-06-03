package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/townsendmerino/ken/internal/search"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// embeddedCorpus is a small fstest.MapFS with one Go file, one Python
// file, and a README — enough to exercise routing through chunkers and
// confirm that BM25 finds known terms. The content is deliberately
// distinctive so the assertions below can be precise.
func embeddedCorpus() fstest.MapFS {
	return fstest.MapFS{
		"main.go": {Data: []byte(`package main

import "fmt"

func ValidateUser(name string) bool {
	return len(name) > 0
}

func main() {
	fmt.Println(ValidateUser("alice"))
}
`)},
		"auth.py": {Data: []byte(`def validate_user(name):
    """Returns True iff the user name is non-empty."""
    return bool(name)


def hash_password(password):
    return password[::-1]
`)},
		"README.md": {Data: []byte(`# Demo corpus

This embedded corpus is used by mcp/run_test.go to exercise
mcp.Run end-to-end. It is intentionally tiny.
`)},
	}
}

// runRunOnInMemoryTransport spins up mcp.Run-equivalent over a single
// in-memory transport pair and returns a connected client session.
// Cleanup stops the server and the client.
func runRunOnInMemoryTransport(t *testing.T, opts Options) (context.Context, *sdk.ClientSession, *bytes.Buffer, func()) {
	t.Helper()
	logBuf := &bytes.Buffer{}
	opts.LogWriter = logBuf
	if opts.LogLevel == "" {
		opts.LogLevel = "debug" // capture everything in tests
	}

	// v0.8.0 Part 3 addendum: buildEmbeddedServer was split into
	// buildEmbeddedIndex (returns the Index + Logger) and runOnTransport's
	// inline atomic-pointer wiring. We replicate the wiring shape here
	// so the same in-memory transport pattern works.
	ix, logger, err := buildEmbeddedIndex(embeddedCorpus(), opts)
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v", err)
	}
	var ixPtr atomic.Pointer[search.Index]
	ixPtr.Store(ix)
	srv := newServerForIndex(&ixPtr, logger, nil /* no DB integration in these tests */)

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx, serverT) }()

	cli := sdk.NewClient(&sdk.Implementation{Name: "run-test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v\n--log--\n%s", err, logBuf.String())
	}

	cleanup := func() {
		_ = sess.Close()
		cancel()
		<-srvDone
	}
	return ctx, sess, logBuf, cleanup
}

// TestRun_BM25_FindsKnownTerm is the happy path with no model. The corpus
// contains "ValidateUser" in main.go; a BM25 search for it must return
// main.go in the top results.
func TestRun_BM25_FindsKnownTerm(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "ValidateUser",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== search result ===\n%s", txt)
	if !strings.Contains(txt, "main.go") {
		t.Errorf("search result missing main.go:\n%s", txt)
	}
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search result missing semble-format header:\n%s", txt)
	}
}

// TestRun_SearchModeArg_PerCallOverride pins the H4 fix: args.mode is
// honored as a per-call override AND the response header reports the
// effective post-downgrade mode. Index is built BM25 (no model wired);
// the agent requests hybrid; runSearch routes through SearchMode,
// downgrades to BM25 silently, and the header says mode=bm25.
func TestRun_SearchModeArg_PerCallOverride(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "ValidateUser",
			"mode":  "hybrid",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	// The capability downgrade must be reflected in the header — agent
	// asked for hybrid, BM25-only index can't serve hybrid, header
	// says bm25.
	if !strings.Contains(txt, "mode=bm25") {
		t.Errorf("expected post-downgrade 'mode=bm25' in header, got:\n%s", txt)
	}
	// Sanity: results still come back.
	if !strings.Contains(txt, "main.go") {
		t.Errorf("expected main.go in results:\n%s", txt)
	}
}

// TestRun_SearchModeArg_EmptyUsesIndexMode confirms that an absent
// args.mode uses the index's build-time mode rather than the old
// hard-coded "hybrid" schema-default header. Calibration-honest: the
// header reflects what the index actually ran.
func TestRun_SearchModeArg_EmptyUsesIndexMode(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "ValidateUser",
			"top_k": 3,
			// no mode arg
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "mode=bm25") {
		t.Errorf("expected 'mode=bm25' (index built bm25), got:\n%s", txt)
	}
}

// TestRun_RepoArgIgnored confirms the embedded-corpus contract: the
// agent's `repo` argument is accepted (for wire compat) but ignored —
// passing a bogus repo must produce the same hit as passing none.
func TestRun_RepoArgIgnored(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	call := func(repo string) string {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "search",
			Arguments: map[string]any{
				"query": "ValidateUser",
				"mode":  "bm25",
				"top_k": 3,
				"repo":  repo,
			},
		})
		if err != nil {
			t.Fatalf("CallTool(search): %v", err)
		}
		return res.Content[0].(*sdk.TextContent).Text
	}
	with := call("/totally/bogus/never-existed-path")
	without := call("")
	if with != without {
		t.Errorf("repo arg changed results:\n--with bogus repo--\n%s\n\n--without--\n%s", with, without)
	}
	// The debug log should mention the ignored repo. We set LogLevel=debug
	// in the helper so this is captured.
	if !strings.Contains(logBuf.String(), "ignored") {
		t.Errorf("expected debug log about ignored repo arg, got:\n%s", logBuf.String())
	}
}

// TestRun_TypoedModeFallsBackWithWarn confirms Options validation: a
// case-mismatch mode ("Hybrid" not "hybrid") falls back to "hybrid" with
// a stderr warning, NOT a hard error — matches cmd/ken-mcp's ADR-009
// behavior.
func TestRun_TypoedModeFallsBackWithWarn(t *testing.T) {
	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{
		Mode:        "Hybrid", // case-mismatch
		ChunkerName: "regex",
		LogLevel:    "warn",
		LogWriter:   logBuf,
	})
	// The build should succeed (falls back to hybrid, then downgrades to
	// bm25 since no model is configured) — never an error.
	if err != nil {
		t.Fatalf("buildEmbeddedIndex with typoed Mode: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil {
		t.Fatalf("index is nil despite no error")
	}
	if !strings.Contains(logBuf.String(), "invalid Options.Mode") {
		t.Errorf("expected warn about invalid Options.Mode, got:\n%s", logBuf.String())
	}
}

// TestRun_HybridDowngradesToBM25WhenNoModel confirms the model-resolve
// downgrade path: requesting hybrid with no ModelFS or ModelDir warns
// once and serves a bm25-only index instead of failing. This is the
// first-launch usability promise the prompt's "auto-downgrade" calls out.
func TestRun_HybridDowngradesToBM25WhenNoModel(t *testing.T) {
	logBuf := &bytes.Buffer{}
	ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{
		Mode:        "hybrid",
		ChunkerName: "regex",
		LogLevel:    "warn",
		LogWriter:   logBuf,
	})
	if err != nil {
		t.Fatalf("buildEmbeddedIndex: %v\n--log--\n%s", err, logBuf.String())
	}
	if ix == nil {
		t.Fatalf("index is nil despite no error")
	}
	low := strings.ToLower(logBuf.String())
	for _, want := range []string{"downgrading to bm25", "neither options.modelfs nor options.modeldir"} {
		if !strings.Contains(low, want) {
			t.Errorf("expected warning %q in log, got:\n%s", want, logBuf.String())
		}
	}
}

// TestRun_HybridWithModelDir exercises the "model loaded from a path"
// path. Gated on testdata/model presence (per-machine; see testdata/README.md).
func TestRun_HybridWithModelDir(t *testing.T) {
	modelDir := filepath.Join("..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "hybrid",
		ChunkerName: "regex",
		ModelDir:    modelDir,
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate user",
			"top_k": 5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== hybrid search result ===\n%s", txt)
	// Either main.go (ValidateUser) or auth.py (validate_user) should be top.
	// Hybrid should find at least one of them.
	if !strings.Contains(txt, "main.go") && !strings.Contains(txt, "auth.py") {
		t.Errorf("hybrid search returned no relevant hit:\n%s", txt)
	}
}

// TestRun_HybridWithModelFS is the embedded-corpus path: model lives in
// an arbitrary fs.FS (here a fstest.MapFS built from testdata/model so
// the test stays self-contained). Confirms ModelFS wins over ModelDir
// and that fs.FS-rooted model loading works the same as path-rooted.
func TestRun_HybridWithModelFS(t *testing.T) {
	modelDir := filepath.Join("..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	modelFS := os.DirFS(modelDir)

	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "hybrid",
		ChunkerName: "regex",
		ModelDir:    "/intentionally/wrong/path", // proves ModelFS wins
		ModelFS:     modelFS,
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "hash password",
			"top_k": 5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== ModelFS hybrid search result ===\n%s", txt)
	if !strings.Contains(txt, "auth.py") {
		t.Errorf("hybrid 'hash password' query missed auth.py:\n%s", txt)
	}
	// Confirm we used the ModelFS source, not the bogus ModelDir.
	if !strings.Contains(logBuf.String(), "Options.ModelFS") {
		t.Errorf("log should mention ModelFS as load source, got:\n%s", logBuf.String())
	}
}

// TestRun_ListsBothTools confirms that the embedded-corpus server
// registers the same two tools as the Cache-backed NewServer, so agents
// trained against semble/ken-mcp continue to work over the embedded build.
func TestRun_ListsBothTools(t *testing.T) {
	ctx, sess, _, cleanup := runRunOnInMemoryTransport(t, Options{Mode: "bm25"})
	defer cleanup()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"search", "find_related"} {
		if !got[want] {
			t.Errorf("missing tool %q (have %v)", want, got)
		}
	}
}

// TestRun_SearchFilters_Language pins the 1.0 language-filter behavior:
// search returns candidates from the index, then post-filters by
// file extension. The embedded corpus has main.go (Go) and auth.py
// (Python); a query that hits both narrowed to languages=["go"]
// must return only main.go.
func TestRun_SearchFilters_Language(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	// "validate user" hits both Go's ValidateUser (camelCase split)
	// and Python's validate_user. Without a filter we'd see both.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":     "validate user",
			"top_k":     10,
			"languages": []string{"go"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "main.go") {
		t.Errorf("languages=[go] filter dropped the expected .go result:\n%s", txt)
	}
	if strings.Contains(txt, "auth.py") {
		t.Errorf("languages=[go] should have filtered out auth.py:\n%s", txt)
	}
	// Header should report the candidate-vs-filter ratio.
	if !strings.Contains(txt, "passed filter") {
		t.Errorf("expected filter ratio in header, got:\n%s", txt)
	}
}

// TestRun_SearchFilters_PathContains pins the substring-path filter.
// "auth" matches auth.py; main.go has no "auth" in its path.
func TestRun_SearchFilters_PathContains(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":         "validate user",
			"top_k":         10,
			"path_contains": "auth",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "auth.py") {
		t.Errorf("path_contains=auth dropped the expected match:\n%s", txt)
	}
	if strings.Contains(txt, "main.go") {
		t.Errorf("path_contains=auth should have filtered out main.go:\n%s", txt)
	}
}

// TestRun_SearchFilters_ExcludePath pins exclude_path_contains.
// Excluding ".py" should drop auth.py and keep main.go.
func TestRun_SearchFilters_ExcludePath(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":                 "validate user",
			"top_k":                 10,
			"exclude_path_contains": ".py",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if strings.Contains(txt, "auth.py") {
		t.Errorf("exclude_path_contains=.py should have dropped auth.py:\n%s", txt)
	}
	if !strings.Contains(txt, "main.go") {
		t.Errorf("exclude_path_contains=.py dropped main.go too:\n%s", txt)
	}
}

// TestRun_SearchFilters_NoMatch confirms the honest empty-after-filter
// response: when the filter rules out every candidate, the response
// names the issue rather than silently returning "No results found"
// (which would be confusing — the search DID find candidates; the
// filter dropped them).
func TestRun_SearchFilters_NoMatch(t *testing.T) {
	ctx, sess, logBuf, cleanup := runRunOnInMemoryTransport(t, Options{
		Mode:        "bm25",
		ChunkerName: "regex",
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":     "validate user",
			"top_k":     5,
			"languages": []string{"rs"}, // no .rs files in fixture
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--log--\n%s", err, logBuf.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "match the filters") {
		t.Errorf("expected post-filter-empty message, got:\n%s", txt)
	}
}
