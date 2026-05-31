package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"
)

// newInMemoryServerClient wires our ken-mcp server to a test client over
// SDK in-memory transports. The fixture is BM25-only so no model is
// needed; the builder ignores its key and always returns the prebuilt
// Index — exercise of the cache is in cache_test.go.
func newInMemoryServerClient(t *testing.T) (context.Context, *sdk.ClientSession, func()) {
	t.Helper()
	ix, err := search.NewWatchedIndex("../testdata/repo", search.ModeBM25, "regex", "", false)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	cache := NewCache(4, func(context.Context, string) (*search.WatchedIndex, func(), error) {
		return ix, nil, nil
	})
	srv := NewServer(Config{Cache: cache, DefaultRepo: "../testdata/repo"})

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx, serverT) }()

	cli := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}

	cleanup := func() {
		_ = sess.Close()
		cancel()
		<-srvDone
		cache.Close()
	}
	return ctx, sess, cleanup
}

func TestServer_ListsBothTools(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// Default fixture wires Reindex=nil → reindex_db is NOT registered,
	// so the agent sees only the two v0.6.0 tools. v0.8.0 Part 2 keeps
	// tools/list honest by hiding tools that would return "no DB" 100%
	// of the time.
	if len(res.Tools) != 2 {
		t.Fatalf("got %d tools, want 2 (Reindex unset → reindex_db not registered)", len(res.Tools))
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
	if got["reindex_db"] {
		t.Errorf("reindex_db should NOT be listed when Config.Reindex is nil")
	}
}

func TestServer_SearchReturnsSemblyFormattedString(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("search returned empty content")
	}
	txt, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *TextContent", res.Content[0])
	}
	// semble-format markers: header line, then a fenced ## 1. <path>:line-line [score=...] block.
	for _, want := range []string{"Search results for:", "auth.py", "[score="} {
		if !strings.Contains(txt.Text, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, txt.Text)
		}
	}
}

func TestServer_FindRelated_NeedsSemanticMode(t *testing.T) {
	// BM25-only index → find_related must surface the "requires semantic
	// or hybrid mode" error as TEXT (not as an MCP protocol error, which
	// would disconnect some agents).
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "find_related",
		Arguments: map[string]any{
			"file_path": "auth.py",
			"line":      5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(find_related): %v", err)
	}
	txt, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *TextContent", res.Content[0])
	}
	if !strings.Contains(txt.Text, "semantic or hybrid") {
		t.Errorf("expected BM25-mode error text, got:\n%s", txt.Text)
	}
}

// mockDB is a test double that satisfies mcp.DBIntegration. Each test
// supplies a TryRefreshFunc to control the response the reindex_db
// handler observes (success / in-progress / error). Start is a no-op
// in tests — they don't exercise the chunk-integration loop; the
// chunks-become-searchable end-to-end is covered by
// mcp/db/run_integration_test.go.
type mockDB struct {
	TryRefreshFunc func(ctx context.Context) ReindexResult
}

func (m *mockDB) Start(_ context.Context, _ func([]chunk.Chunk)) (func(), error) {
	return func() {}, nil
}

func (m *mockDB) TryRefresh(ctx context.Context) ReindexResult {
	if m.TryRefreshFunc == nil {
		return ReindexResult{}
	}
	return m.TryRefreshFunc(ctx)
}

// newReindexServerClient is the server fixture for v0.8.0 Part 2 +
// Part-3-addendum reindex_db tests. Same shape as
// newInMemoryServerClient but with a caller-supplied DBIntegration
// (via mockDB) so each test can control the result the tool's handler
// observes (success / in-progress / error).
func newReindexServerClient(t *testing.T, dbi DBIntegration) (context.Context, *sdk.ClientSession, func()) {
	t.Helper()
	ix, err := search.NewWatchedIndex("../testdata/repo", search.ModeBM25, "regex", "", false)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	cache := NewCache(4, func(context.Context, string) (*search.WatchedIndex, func(), error) {
		return ix, nil, nil
	})
	srv := NewServer(Config{Cache: cache, DefaultRepo: "../testdata/repo", DB: dbi})

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(ctx, serverT) }()

	cli := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}
	cleanup := func() {
		_ = sess.Close()
		cancel()
		<-srvDone
		cache.Close()
	}
	return ctx, sess, cleanup
}

// TestReindexDBTool_Registered confirms that when Config.DB is
// non-nil, the tool appears in tools/list alongside search +
// find_related.
func TestReindexDBTool_Registered(t *testing.T) {
	ctx, sess, cleanup := newReindexServerClient(t, &mockDB{
		TryRefreshFunc: func(context.Context) ReindexResult {
			return ReindexResult{Elapsed: 5 * time.Millisecond}
		},
	})
	defer cleanup()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"search", "find_related", "reindex_db"} {
		if !got[want] {
			t.Errorf("missing tool %q (have %v)", want, got)
		}
	}
}

// TestReindexDBTool_NoDB confirms that with Config.Reindex unset the
// tool is NOT registered (we don't want a tool that returns "no DB"
// 100% of the time). Sibling assertion to the updated
// TestServer_ListsBothTools above; kept separate so its name surfaces
// the no-DB behavior explicitly in -v output.
func TestReindexDBTool_NoDB(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t) // Reindex=nil
	defer cleanup()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "reindex_db" {
			t.Fatalf("reindex_db must not be registered when Config.Reindex is nil")
		}
	}
	// Calling the tool by name should also error at the protocol layer
	// (tool not found), but the SDK's behavior for an unknown tool name
	// is not part of ken's wire contract — the ListTools check above is
	// the load-bearing assertion.
}

// TestReindexDBTool_Success verifies the success path renders the
// "Reindexed in Nms" text response and passes elapsed-ms through from
// the callback.
func TestReindexDBTool_Success(t *testing.T) {
	const elapsed = 142 * time.Millisecond
	ctx, sess, cleanup := newReindexServerClient(t, &mockDB{
		TryRefreshFunc: func(context.Context) ReindexResult {
			return ReindexResult{Elapsed: elapsed}
		},
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v", err)
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Reindexed in 142ms") {
		t.Errorf("expected 'Reindexed in 142ms', got: %s", txt)
	}
}

// TestReindexDBTool_InProgress: when the callback signals InProgress,
// the agent-facing response is the documented "already in progress"
// text — no timing data leaked, no error markers.
func TestReindexDBTool_InProgress(t *testing.T) {
	ctx, sess, cleanup := newReindexServerClient(t, &mockDB{
		TryRefreshFunc: func(context.Context) ReindexResult {
			return ReindexResult{InProgress: true}
		},
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v", err)
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Reindex already in progress") {
		t.Errorf("expected 'Reindex already in progress' text, got: %s", txt)
	}
}

// TestReindexDBTool_Error: when the callback returns Err, the agent-
// facing response includes the error text verbatim. We don't try to
// classify the error (transient vs fatal) — that's the agent's call.
func TestReindexDBTool_Error(t *testing.T) {
	const errText = "introspection query failed: connection refused"
	ctx, sess, cleanup := newReindexServerClient(t, &mockDB{
		TryRefreshFunc: func(context.Context) ReindexResult {
			return ReindexResult{Err: &mockError{errText}}
		},
	})
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v", err)
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Reindex failed:") {
		t.Errorf("expected 'Reindex failed:' prefix, got: %s", txt)
	}
	if !strings.Contains(txt, errText) {
		t.Errorf("expected verbatim error text, got: %s", txt)
	}
}

type mockError struct{ msg string }

func (e *mockError) Error() string { return e.msg }

func TestDefaultInstructions_ContainsRecallGuidance(t *testing.T) {
	// v0.8.1 Part A: the default instructions tell agent planners that
	// ken is relevance-optimized and they should fall back to grep for
	// exhaustive operations. Sanity check the surfaced text contains
	// the load-bearing tokens of that framing — not the exact phrasing
	// (which can drift), but the planner-visible cues.
	cache := NewCache(2, func(context.Context, string) (*search.WatchedIndex, func(), error) {
		t.Fatal("builder should not be invoked just to read instructions")
		return nil, nil, nil
	})
	srv := NewServer(Config{Cache: cache})

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Run(ctx, serverT)
	cli := sdk.NewClient(&sdk.Implementation{Name: "t"}, nil)
	sess, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	init := sess.InitializeResult()
	if init == nil {
		t.Fatal("InitializeResult was nil")
	}
	instr := init.Instructions

	wantSubstrings := []string{
		"ken",
		"grep",
		"100% recall",
		"isn't designed for exhaustive enumeration",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(instr, want) {
			t.Errorf("default instructions missing %q\ngot: %q", want, instr)
		}
	}

	// The hardcoded "82-91% recall at K=10" framing from v0.8.0 was
	// deliberately removed in v0.8.1 — hardcoded numbers date the
	// instructions when ken's pipeline improves. If it sneaks back
	// in, the deletion was undone.
	for _, banned := range []string{"82", "91%", "K=10"} {
		if strings.Contains(instr, banned) {
			t.Errorf("default instructions still contain hardcoded recall number %q (should be qualitative framing)\ngot: %q", banned, instr)
		}
	}
}

func TestServer_NoRepoNoDefault_ReturnsValidationText(t *testing.T) {
	// Build a server WITHOUT a default repo. Tools should reject a call
	// that also omits `repo` — as text, not a protocol error.
	cache := NewCache(2, func(context.Context, string) (*search.WatchedIndex, func(), error) {
		t.Fatal("builder should not be invoked when repo is missing")
		return nil, nil, nil
	})
	srv := NewServer(Config{Cache: cache})

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Run(ctx, serverT)
	cli := sdk.NewClient(&sdk.Implementation{Name: "t"}, nil)
	sess, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "anything"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "no repo specified") {
		t.Errorf("expected 'no repo specified' text, got: %s", txt)
	}
}
