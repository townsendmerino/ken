package engine_test

// Ported from the pre-engine cmd/ken-mcp/snapshot_test.go (deleted as part
// of moving ken-mcp's ADR-039 snapshot wiring onto this package — the
// coverage these tests give now guards the shared implementation instead
// of a ken-mcp-local copy). Adapted to engine's black-box surface: instead
// of calling the private tryLoadSnapshot directly, each test re-Opens the
// same root and asserts on the diagnostic line Open's LogWriter emits
// (snapshot.go's w.logf calls are the only observable signal of which path
// — clean load / drift rebuild / reconcile / config mismatch — was taken
// from outside the package).

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// openSnapshot opens dir with Snapshot on and the given chunker, returning
// the Workspace and the captured log text.
func openSnapshot(t *testing.T, ctx context.Context, dir, chunker string) (*engine.Workspace, string) {
	t.Helper()
	var logBuf bytes.Buffer
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Chunker: chunker, Watch: true, Snapshot: true, LogWriter: &logBuf})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ws, logBuf.String()
}

func TestSnapshot_WriteThenCleanLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"),
		[]byte("package auth\nfunc Login(u string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws1, _ := openSnapshot(t, ctx, dir, "line")
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ken", "snapshot.bin")); err != nil {
		t.Fatalf("snapshot.bin not written: %v", err)
	}

	ws2, log := openSnapshot(t, ctx, dir, "line")
	defer func() { _ = ws2.Close() }()
	if !strings.Contains(log, "no drift") {
		t.Fatalf("expected a clean (no-drift) snapshot load, log was: %q", log)
	}
	hits, err := ws2.Search(ctx, "Login", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Error("seeded index should find 'Login'")
	}
}

func TestSnapshot_DriftTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(f, []byte("package auth\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws1, _ := openSnapshot(t, ctx, dir, "line")
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Edit the sole file — 100% drift, over the reconcile threshold.
	if err := os.WriteFile(f, []byte("package auth\nfunc Login() {}\nfunc Logout() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws2, log := openSnapshot(t, ctx, dir, "line")
	defer func() { _ = ws2.Close() }()
	if !strings.Contains(log, "drifted heavily") {
		t.Fatalf("expected a heavy-drift full rebuild, log was: %q", log)
	}
}

func TestSnapshot_NewFileTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws1, _ := openSnapshot(t, ctx, dir, "line")
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Adding a file to a 1-file repo is >50% drift.
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws2, log := openSnapshot(t, ctx, dir, "line")
	defer func() { _ = ws2.Close() }()
	if !strings.Contains(log, "drifted heavily") {
		t.Fatalf("expected a heavy-drift full rebuild, log was: %q", log)
	}
}

func TestSnapshot_ConfigMismatchTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws1, _ := openSnapshot(t, ctx, dir, "line") // stored with chunker=line
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen requesting chunker=regex → config-key mismatch.
	ws2, log := openSnapshot(t, ctx, dir, "regex")
	defer func() { _ = ws2.Close() }()
	if !strings.Contains(log, "config changed") {
		t.Fatalf("expected a config-mismatch rebuild, log was: %q", log)
	}
}

func TestSnapshot_MissingAndCorruptDegradeToRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// No .ken/ at all → silent fall-through to a live build (never a crash
	// on untrusted/missing snapshot input).
	ws1, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Chunker: "line", Watch: true, Snapshot: true})
	if err != nil {
		t.Fatalf("Open with no snapshot present: %v", err)
	}
	if ws1.Status().ChunkCount == 0 {
		t.Error("expected a live build to still produce chunks with no snapshot present")
	}
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Corrupt the manifest → DecodeManifest fails → rebuild, logged.
	if err := os.WriteFile(filepath.Join(dir, ".ken", "snapshot.manifest"), []byte("not a real KMAN blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws2, log := openSnapshot(t, ctx, dir, "line")
	if !strings.Contains(log, "unreadable") {
		t.Fatalf("expected an unreadable-manifest rebuild, log was: %q", log)
	}
	if err := ws2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Manifest present but bin missing → rebuild, logged.
	if err := os.Remove(filepath.Join(dir, ".ken", "snapshot.bin")); err != nil {
		t.Fatal(err)
	}
	ws3, log3 := openSnapshot(t, ctx, dir, "line")
	defer func() { _ = ws3.Close() }()
	if !strings.Contains(log3, "missing") {
		t.Fatalf("expected a missing-bin rebuild, log was: %q", log3)
	}
}

func TestSnapshot_SmallDriftReconciles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		if err := os.WriteFile(filepath.Join(dir, n+".go"),
			[]byte("package p\nfunc "+n+"Sym() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws1, _ := openSnapshot(t, ctx, dir, "line")
	if err := ws1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Edit ONE of six files → 1/6 drift, well under the 50% reconcile
	// threshold.
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\nfunc aSymRenamed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws2, log := openSnapshot(t, ctx, dir, "line")
	if !strings.Contains(log, "reconciled snapshot") {
		t.Fatalf("expected an incremental reconcile, log was: %q", log)
	}
	if hits, err := ws2.Search(ctx, "aSymRenamed", 10); err != nil || len(hits) == 0 {
		t.Errorf("reconciled index should reflect the edit (aSymRenamed not found): err=%v hits=%v", err, hits)
	}
	if hits, err := ws2.Search(ctx, "fSym", 10); err != nil || len(hits) == 0 {
		t.Errorf("unchanged files should be kept from the snapshot (fSym not found): err=%v hits=%v", err, hits)
	}
	if err := ws2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The reconcile re-persisted the snapshot: a fresh open now sees no drift.
	ws3, log3 := openSnapshot(t, ctx, dir, "line")
	defer func() { _ = ws3.Close() }()
	if !strings.Contains(log3, "no drift") {
		t.Fatalf("expected a clean load after reconcile re-persisted, log was: %q", log3)
	}
}
