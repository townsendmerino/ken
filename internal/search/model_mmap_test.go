package search

import (
	"os"
	"testing"
)

// TestLoadModelDir_MmapMatchesHeapRead pins the FSOptions.MmapModel wiring:
// aikit's embed.LoadMmap must produce embeddings identical to the unchanged
// embed.LoadFromFS heap-read path — mmap only changes where the safetensors
// bytes come from, never the tensor values (see aikit's LoadMmap doc
// comment). If this ever drifted, ken-mcp's default-on KEN_MCP_MMAP_MODEL
// would silently serve different vectors than the CLI's heap-read builds.
func TestLoadModelDir_MmapMatchesHeapRead(t *testing.T) {
	modelDir := modelDirOrSkip(t)

	heap, err := LoadModelDir(modelDir, false)
	if err != nil {
		t.Fatalf("LoadModelDir(mmap=false): %v", err)
	}
	mapped, err := LoadModelDir(modelDir, true)
	if err != nil {
		t.Fatalf("LoadModelDir(mmap=true): %v", err)
	}

	texts := []string{
		"func ValidateToken(tok string) error",
		"database connection pooling logic",
		"",
	}
	for _, text := range texts {
		got := mapped.Encode(text)
		want := heap.Encode(text)
		if len(got) != len(want) {
			t.Fatalf("Encode(%q): mmap dim %d != heap dim %d", text, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("Encode(%q): mmap and heap embeddings diverge at index %d (%v vs %v)", text, i, got[i], want[i])
			}
		}
	}
}

// TestLoadModelDir_MmapMissingDir confirms the mmap path fails the same way
// the heap path does on a bad directory — no panic, no partial state.
func TestLoadModelDir_MmapMissingDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadModelDir(dir, true); err == nil {
		t.Fatal("LoadModelDir(mmap=true) on an empty dir: want error, got nil")
	}
}

// TestWalkAndChunkFS_MmapModelOption_EndToEnd confirms FSOptions.MmapModel
// actually reaches the live-build model load (the ken-mcp path) and produces
// a working semantic index, not just a compiling struct field.
func TestWalkAndChunkFS_MmapModelOption_EndToEnd(t *testing.T) {
	modelDir := modelDirOrSkip(t)
	root := t.TempDir()
	if err := os.WriteFile(root+"/auth.go", []byte("package auth\nfunc ValidateToken(tok string) error {\n\treturn nil\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := FromFSWithOptions(os.DirFS(root), ModeSemantic, "regex", modelDir, FSOptions{MmapModel: true})
	if err != nil {
		t.Fatalf("FromFSWithOptions(MmapModel=true): %v", err)
	}
	if ix.Len() == 0 {
		t.Fatal("indexed 0 chunks")
	}
	res := ix.Search("validate token", 5)
	if len(res) == 0 {
		t.Fatal("search returned no results for 'validate token'")
	}
}
