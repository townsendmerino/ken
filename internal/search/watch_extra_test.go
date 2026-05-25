package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/chunk"
)

// TestSetExtraChunks_UnionsFSAndDBChunks confirms the v0.7.0 snapshot-
// swap composition (ADR-017): WatchedIndex.SetExtraChunks publishes a
// new snapshot whose chunks slice is FS-chunks ∪ extras, and search
// over the new snapshot finds content from both sources.
func TestSetExtraChunks_UnionsFSAndDBChunks(t *testing.T) {
	// Build a tiny WatchedIndex over an empty real-FS dir (watch=false
	// so no fsnotify goroutine starts; we won't trigger any FS-driven
	// flushes — we just exercise the SetExtraChunks path).
	dir := t.TempDir()
	if err := writeTextFile(filepath.Join(dir, "main.go"), `package main
func validateEmail(s string) bool { return len(s) > 0 }
`); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	wix, err := NewWatchedIndex(dir, ModeBM25, "regex", "", false)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	defer wix.Close()

	// Sanity: pre-injection, the index has only the FS chunk.
	pre := wix.Load()
	if pre == nil || pre.Len() == 0 {
		t.Fatalf("pre-injection index is empty; FS chunk missing")
	}
	preCount := pre.Len()

	// Inject "DB" chunks shaped like internal/db would emit.
	dbChunks := []chunk.Chunk{
		{
			File:      "db://postgres@dev/public.users",
			StartLine: 1,
			EndLine:   5,
			Text:      "-- indexed at 2026-08-15T14:23Z from postgres@dev\nTABLE users\n  id  bigint  PK\n  email  varchar(255)  NOT NULL UNIQUE\n",
		},
		{
			File:      "db://postgres@dev/public.sessions",
			StartLine: 1,
			EndLine:   4,
			Text:      "-- indexed at 2026-08-15T14:23Z from postgres@dev\nTABLE sessions\n  id  bigint  PK\n  user_id  bigint  NOT NULL → users(id)\n",
		},
	}
	wix.SetExtraChunks(dbChunks)

	post := wix.Load()
	if post == nil {
		t.Fatalf("post-injection index is nil")
	}
	if post.Len() != preCount+len(dbChunks) {
		t.Errorf("post-injection chunk count = %d, want %d (FS %d + DB %d)",
			post.Len(), preCount+len(dbChunks), preCount, len(dbChunks))
	}

	// Search hits BOTH sources — code via the FS chunk and the DB
	// table via the structural chunk.
	codeHits := wix.Search("validateEmail", 5)
	if len(codeHits) == 0 || !strings.Contains(codeHits[0].Chunk.Text, "validateEmail") {
		t.Errorf("expected validateEmail hit from FS chunk; got %d results", len(codeHits))
	}
	dbHits := wix.Search("users email VARCHAR", 5)
	foundDB := false
	for _, r := range dbHits {
		if strings.HasPrefix(r.Chunk.File, "db://") {
			foundDB = true
			break
		}
	}
	if !foundDB {
		t.Errorf("expected DB chunk hit for 'users email VARCHAR'; got %d results, none from db://", len(dbHits))
	}

	// Replacing extras with nil clears the DB chunks. The FS chunk
	// stays — that's the "DB unreachable, keep serving FS" path.
	wix.SetExtraChunks(nil)
	cleared := wix.Load()
	if cleared.Len() != preCount {
		t.Errorf("after SetExtraChunks(nil), expected %d chunks (FS only), got %d", preCount, cleared.Len())
	}
}

// TestSetExtraChunks_NilThenEmptyIsNoOp documents the "no-op" semantic:
// calling SetExtraChunks repeatedly with nil/empty doesn't fail.
func TestSetExtraChunks_NilThenEmptyIsNoOp(t *testing.T) {
	dir := t.TempDir()
	wix, err := NewWatchedIndex(dir, ModeBM25, "regex", "", false)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	defer wix.Close()
	wix.SetExtraChunks(nil)
	wix.SetExtraChunks([]chunk.Chunk{})
	wix.SetExtraChunks(nil)
	// Just verifying no panic and the index is still usable.
	_ = wix.Load()
}

func writeTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
