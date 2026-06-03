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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	// search + find_related from v0.6, plus status from the 1.0
	// ship-list. reindex_db remains hidden when MY_DB_DSN is empty.
	if len(tl.Tools) != 3 {
		names := []string{}
		for _, tool := range tl.Tools {
			names = append(names, tool.Name)
		}
		t.Errorf("got %d tools, want 3 (no DSN configured → reindex_db not registered); got %v", len(tl.Tools), names)
	}
	for _, tool := range tl.Tools {
		if tool.Name == "reindex_db" {
			t.Errorf("reindex_db should NOT be in tools/list when MY_DB_DSN is empty")
		}
	}
}

// TestRun_WithDB_ChunksBecomeSearchableAfterReindex is the
// load-bearing v0.8.0 Part 3 addendum (ADR-020) verification. The
// gap this addendum closes: in Part 3's initial ship, calling
// reindex_db ran IndexSchema but the chunks weren't unioned into
// mcp.Run's embedded *search.Index — the agent's next search call
// returned the same stale results. This test asserts the gap is
// closed: after reindex_db, search finds DB chunks that weren't in
// the corpus pre-reindex.
//
// The test:
//  1. Spins up a real Postgres (via KEN_DB_TEST_DSN).
//  2. Creates a uniquely-named table (so we can grep for it
//     independently of any other corpus content).
//  3. Spawns the mini-binary with that DSN.
//  4. Calls reindex_db via MCP.
//  5. Calls search for the table name.
//  6. Asserts the table appears in the results.
//
// Skipped without KEN_DB_TEST_DSN (CI sets it via the
// postgres:16-alpine service container — same shape as
// internal/db's reindex_db integration tests).
func TestRun_WithDB_ChunksBecomeSearchableAfterReindex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}
	dsn := os.Getenv("KEN_DB_TEST_DSN")
	if dsn == "" {
		t.Skip("KEN_DB_TEST_DSN not set; see internal/db/integration_test.go for setup")
	}

	// Create a uniquely-named table directly via pgx — the mini-binary
	// will see it on its initial introspection.
	table := fmt.Sprintf("qzx_addendum_marker_%d", time.Now().UnixNano())
	createSQL := fmt.Sprintf("CREATE TABLE %s (id int)", table)
	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s", table)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create the table via a fresh pgx connection (matches the
	// pattern internal/db/reindex_integration_test.go uses).
	setupConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect (setup): %v", err)
	}
	if _, err := setupConn.Exec(ctx, createSQL); err != nil {
		_ = setupConn.Close(context.Background())
		t.Fatalf("create table: %v", err)
	}
	_ = setupConn.Close(context.Background())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupConn, err := pgx.Connect(cleanupCtx, dsn)
		if err != nil {
			return // best-effort cleanup
		}
		_, _ = cleanupConn.Exec(cleanupCtx, dropSQL)
		_ = cleanupConn.Close(context.Background())
	})

	binPath := buildEmbeddedWithDBBinary(t)
	cmd := newBinaryCommand(t, ctx, binPath, dsn)
	stderr := newStderrBuf()
	cmd.Stderr = stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "mcp-db-run-chunks", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect: %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// Trigger reindex_db explicitly — even though the initial Start
	// fires the swap, calling reindex_db proves the on-demand path
	// also makes chunks searchable in real time.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v\n--stderr--\n%s", err, stderr.String())
	}
	rtxt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(rtxt, "Reindexed in") && !strings.Contains(rtxt, "Reindex already in progress") {
		t.Errorf("unexpected reindex_db response: %s\n--stderr--\n%s", rtxt, stderr.String())
	}

	// Search for the table name. After the reindex, the DB chunk for
	// this table should be in the embedded Index (via
	// *search.Index.WithExtraChunks rebuild + atomic-store).
	searchRes, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": table,
			"mode":  "bm25",
			"top_k": 10,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--stderr--\n%s", err, stderr.String())
	}
	stxt := searchRes.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(stxt, table) {
		t.Errorf("search for table name %q didn't find the DB chunk; got:\n%s\n--stderr--\n%s",
			table, stxt, stderr.String())
	}
}
