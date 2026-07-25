package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotConfigKey_StableAndSensitive(t *testing.T) {
	base := SnapshotConfigKey(ModeHybrid, "regex", "dim=256,vocab=30000,bytes=999", true)
	// Same inputs → identical key.
	if again := SnapshotConfigKey(ModeHybrid, "regex", "dim=256,vocab=30000,bytes=999", true); again != base {
		t.Fatalf("config key not stable:\n %q\n %q", base, again)
	}
	// Each knob must change the key.
	cases := map[string]string{
		"mode":    SnapshotConfigKey(ModeBM25, "regex", "dim=256,vocab=30000,bytes=999", true),
		"chunker": SnapshotConfigKey(ModeHybrid, "treesitter", "dim=256,vocab=30000,bytes=999", true),
		"model":   SnapshotConfigKey(ModeHybrid, "regex", "dim=512,vocab=30000,bytes=999", true),
		"enrich":  SnapshotConfigKey(ModeHybrid, "regex", "dim=256,vocab=30000,bytes=999", false),
	}
	for knob, key := range cases {
		if key == base {
			t.Errorf("changing %s did not change the config key", knob)
		}
	}
}

func TestSnapshotManifest_EncodeDecodeRoundtrip(t *testing.T) {
	m := SnapshotManifest{
		ConfigKey: "v1|mode=2|chunker=regex",
		Files: []FileStamp{
			{File: "a.go", MTimeNano: 111, Size: 10},
			{File: "b/c.go", MTimeNano: 222, Size: 20},
		},
	}
	got, err := DecodeManifest(m.Encode())
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if got.ConfigKey != m.ConfigKey {
		t.Errorf("config key = %q, want %q", got.ConfigKey, m.ConfigKey)
	}
	if !got.FilesEqual(m) {
		t.Errorf("files roundtrip mismatch:\n got %+v\n want %+v", got.Files, m.Files)
	}
}

func TestSnapshotManifest_EmptyRoundtrip(t *testing.T) {
	m := SnapshotManifest{ConfigKey: "k"}
	got, err := DecodeManifest(m.Encode())
	if err != nil {
		t.Fatalf("DecodeManifest empty: %v", err)
	}
	if got.ConfigKey != "k" || len(got.Files) != 0 {
		t.Errorf("empty manifest roundtrip = %+v", got)
	}
}

func TestDecodeManifest_RejectsCorrupt(t *testing.T) {
	good := SnapshotManifest{ConfigKey: "k", Files: []FileStamp{{File: "a", MTimeNano: 1, Size: 2}}}.Encode()

	t.Run("bad magic", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[0] = 'X'
		if _, err := DecodeManifest(bad); err == nil {
			t.Fatal("expected error for bad magic")
		}
	})
	t.Run("crc mismatch", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[len(bad)/2] ^= 0xff // flip a body byte, CRC no longer matches
		if _, err := DecodeManifest(bad); err == nil {
			t.Fatal("expected error for corrupted body")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, err := DecodeManifest(good[:6]); err == nil {
			t.Fatal("expected error for truncated buffer")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := DecodeManifest(nil); err == nil {
			t.Fatal("expected error for nil buffer")
		}
	})
}

func TestSnapshotManifest_FilesEqual(t *testing.T) {
	a := SnapshotManifest{Files: []FileStamp{{"a", 1, 10}, {"b", 2, 20}}}
	if !a.FilesEqual(SnapshotManifest{Files: []FileStamp{{"a", 1, 10}, {"b", 2, 20}}}) {
		t.Error("identical manifests should be equal")
	}
	// Config key is NOT part of the drift check.
	if !a.FilesEqual(SnapshotManifest{ConfigKey: "different", Files: []FileStamp{{"a", 1, 10}, {"b", 2, 20}}}) {
		t.Error("config key must not affect FilesEqual")
	}
	drift := []SnapshotManifest{
		{Files: []FileStamp{{"a", 1, 10}}},                             // removed file
		{Files: []FileStamp{{"a", 1, 10}, {"b", 2, 20}, {"c", 3, 30}}}, // added file
		{Files: []FileStamp{{"a", 999, 10}, {"b", 2, 20}}},             // mtime changed
		{Files: []FileStamp{{"a", 1, 99}, {"b", 2, 20}}},               // size changed
	}
	for i, d := range drift {
		if a.FilesEqual(d) {
			t.Errorf("drift case %d should not be equal", i)
		}
	}
}

func TestBuildFileStamps_StatsAndSorts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "z.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	stamps := BuildFileStamps(os.DirFS(dir), []string{"z.txt", "sub/a.txt", "missing.txt"})
	// missing.txt is skipped; the two real files are sorted ascending.
	if len(stamps) != 2 {
		t.Fatalf("got %d stamps, want 2 (missing file skipped): %+v", len(stamps), stamps)
	}
	if stamps[0].File != "sub/a.txt" || stamps[1].File != "z.txt" {
		t.Errorf("stamps not sorted ascending: %+v", stamps)
	}
	if stamps[1].Size != 5 { // "hello"
		t.Errorf("z.txt size = %d, want 5", stamps[1].Size)
	}
}

// TestNewWatchedIndexFromSnapshot_SeedsWatchingIndex verifies that seeding a
// WatchedIndex from a corpus (chunks+vecs) produces a searchable, closable
// watching index — the cold-start-M1 fast path — without re-walking.
func TestNewWatchedIndexFromSnapshot_SeedsWatchingIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"),
		[]byte("package auth\nfunc Login(user string) error { return nil }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Build a normal BM25 index over the dir (no model needed), then take
	// its raw corpus as the "loaded snapshot".
	built, err := FromPath(dir, ModeBM25, "line", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	chunks, vecs := built.Chunks(), built.Vecs()
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk from auth.go")
	}

	wi, err := NewWatchedIndexFromSnapshot(dir, ModeBM25, "line", "", nil, chunks, vecs, true, FSOptions{})
	if err != nil {
		t.Fatalf("NewWatchedIndexFromSnapshot: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	// The seeded index is queryable over the loaded corpus.
	if got := wi.Load(); got == nil || got.Len() != len(chunks) {
		t.Fatalf("seeded index Len = %v, want %d", got, len(chunks))
	}
	if res := wi.Search("Login", 5); len(res) == 0 {
		t.Error("expected a hit for 'Login' in the seeded index")
	}
}
