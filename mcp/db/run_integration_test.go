package mcpdb_test

// External-test-package (mcpdb_test) integration tests for the
// SDK-author path: mcp.Run + mcp/db.Setup wired together, driven
// through the SDK's in-memory transport. Lives in mcp/db because it
// imports both mcp and mcp/db; placing it in mcp/ would create a
// cycle (mcp/db imports mcp; mcp can't import mcp/db).
//
// The integration test asserts the v0.8.0 Part 3 end-to-end contract:
//
//   1. SDK author writes the canonical Setup call.
//   2. mcp.Run, given the returned ReindexFunc as opts.Reindex,
//      registers `reindex_db` alongside `search` + `find_related`
//      in tools/list — total 3 tools.
//   3. Calling reindex_db returns the "Reindexed in Nms." response
//      (or "already in progress" — both prove the tool round-tripped
//      through the full Refresher path).
//   4. The opt-OUT case (Setup with empty DSN → nil ReindexFunc →
//      reindex_db NOT in tools/list) — back to 2 tools, matching the
//      v0.6.0 docs-only behavior.
//
// Uses an empty SQLite temp file so no service container is needed;
// the integration story against live Postgres is covered by
// internal/db/reindex_integration_test.go (Part 2). This test is
// specifically about the SDK-author Setup-→-mcp.Run wiring.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func emptySQLiteDSN(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run-with-db.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp sqlite: %v", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		t.Fatalf("ping temp sqlite: %v", err)
	}
	_ = conn.Close()
	return "sqlite://" + path
}

// TestRun_WithMCPDBReindex_Binary is the end-to-end test for the SDK
// author path. The in-process approach (build via the mcp package's
// public surface, drive over sdk.NewInMemoryTransports) isn't
// available because mcp.Run uses sdk.StdioTransport internally and
// runOnTransport is unexported. The binary-spawn path is also the
// most realistic SDK-author audit — it builds an actual binary that
// imports mcp + mcp/db, spawns it, and drives the protocol.
//
// This test also doubles as the seventh stdout-cleanliness variant
// for v0.8.0 Part 3: if anything in the mcp.Run + mcp/db.Setup code
// path writes to stdout, sdk.CommandTransport can't parse the JSON-RPC
// response and the test fails loudly — the same failure SDK author
// agents would see. Builds a tiny mini-binary (testdata/embedded-with-db/)
// that wires mcp.Run + mcp/db.Setup with an empty SQLite DSN, drives
// it through sdk.CommandTransport, and asserts:
//   - tools/list contains all three tools (search, find_related,
//     reindex_db)
//   - reindex_db responds with the documented success shape
//
// This is the SDK-author analogue of cmd/ken-mcp's
// TestBinary_StdoutIsCleanJSONRPC_WithReindexDB (Part 2) — different
// binary, same protocol round-trip.
func TestRun_WithMCPDBReindex_Binary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}

	binPath := buildEmbeddedWithDBBinary(t)
	dsn := emptySQLiteDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := newBinaryCommand(t, ctx, binPath, dsn)
	stderr := newStderrBuf()
	cmd.Stderr = stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "mcp-db-run-test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect: %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// tools/list must include all three tools.
	tl, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v\n--stderr--\n%s", err, stderr.String())
	}
	have := map[string]bool{}
	for _, tool := range tl.Tools {
		have[tool.Name] = true
	}
	for _, want := range []string{"search", "find_related", "reindex_db"} {
		if !have[want] {
			t.Errorf("missing tool %q (have %v)\n--stderr--\n%s", want, have, stderr.String())
		}
	}

	// reindex_db round-trip must return the documented success shape.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Reindexed in") && !strings.Contains(txt, "Reindex already in progress") {
		t.Errorf("unexpected reindex_db response from mcp.Run + mcp/db.Setup binary: %s\n--stderr--\n%s", txt, stderr.String())
	}
}

// TestRun_WithoutMCPDB_ToolListExcludesReindexDB confirms the opt-out
// side of the contract: the same mini-binary, when given an empty DSN
// (Setup returns nil-nil-nil → opts.Reindex stays nil → reindex_db
// not registered), serves exactly two tools.
//
// This is the v0.6.0 contract verification at the protocol level —
// SDK authors who don't wire DB support see the v0.6.0 tool surface.
func TestRun_WithoutMCPDB_ToolListExcludesReindexDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}

	binPath := buildEmbeddedWithDBBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Empty DSN → Setup returns nil → opts.Reindex stays nil →
	// reindex_db NOT registered. We pass "" via the env-var the
	// mini-binary reads.
	cmd := newBinaryCommand(t, ctx, binPath, "" /* empty DSN */)
	stderr := newStderrBuf()
	cmd.Stderr = stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "mcp-db-run-test-nodb", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect: %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	tl, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v\n--stderr--\n%s", err, stderr.String())
	}
	if len(tl.Tools) != 2 {
		names := []string{}
		for _, tool := range tl.Tools {
			names = append(names, tool.Name)
		}
		t.Errorf("got %d tools, want 2 (no DSN configured → reindex_db not registered); got %v", len(tl.Tools), names)
	}
	for _, tool := range tl.Tools {
		if tool.Name == "reindex_db" {
			t.Errorf("reindex_db should NOT be in tools/list when MY_DB_DSN is empty")
		}
	}
}
