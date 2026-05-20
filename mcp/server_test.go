package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
)

// newInMemoryServerClient wires our ken-mcp server to a test client over
// SDK in-memory transports. The fixture is BM25-only so no model is
// needed; the builder ignores its key and always returns the prebuilt
// Index — exercise of the cache is in cache_test.go.
func newInMemoryServerClient(t *testing.T) (context.Context, *sdk.ClientSession, func()) {
	t.Helper()
	ix, err := search.FromPath("../testdata/repo", search.ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	cache := NewCache(4, func(context.Context, string) (*search.Index, func(), error) {
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
	if len(res.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(res.Tools))
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

func TestServer_NoRepoNoDefault_ReturnsValidationText(t *testing.T) {
	// Build a server WITHOUT a default repo. Tools should reject a call
	// that also omits `repo` — as text, not a protocol error.
	cache := NewCache(2, func(context.Context, string) (*search.Index, func(), error) {
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
	if !strings.Contains(txt, "No repo specified") {
		t.Errorf("expected 'No repo specified' text, got: %s", txt)
	}
}
