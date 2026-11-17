package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// heldSource is a StructuralSource whose build never finishes: the state of
// a structural index that is still building (or rebuilding).
type heldSource struct{}

func (heldSource) StructuralIndex(context.Context) *engine.StructuralIndex { return nil }
func (heldSource) StructuralPending() bool                                 { return true }
func (heldSource) StructuralIfBuilt() *engine.StructuralIndex              { return nil }

// F2: a Workspace-backed bundle whose structural build is in flight must say
// "building", not "no extractors registered".
func TestStructuralUnavailable_WorkspaceBuildInFlightSaysBuilding(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inner, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	ws := engine.WrapIndex(inner.WatchedIndex(), heldSource{})

	b := &RepoBundle{Workspace: ws}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	cfg := &Config{Cache: cache, DefaultRepo: dir}
	r, _, err := handleOutline(ctx, cfg, OutlineArgs{Path: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	if strings.Contains(got, "no extractors") {
		t.Errorf("building index reported as unsupported: %s", got)
	}
	if !strings.Contains(strings.ToLower(got), "building") {
		t.Errorf("want a 'building' message, got: %s", got)
	}
}

func outlineText(t *testing.T, dir, path string) string {
	t.Helper()
	ctx := context.Background()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	b := &RepoBundle{Workspace: ws}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	defer func() { _ = ws.Close() }()
	cfg := &Config{Cache: cache, DefaultRepo: dir}
	r, _, err := handleOutline(ctx, cfg, OutlineArgs{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return extractText(t, r)
}

// F2: a corpus with no supported language gets the unsupported message,
// with file counts by extension.
func TestStructuralUnavailable_UnsupportedCountsFilesByLanguage(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	inner, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	b := &RepoBundle{Workspace: engine.WrapIndex(inner.WatchedIndex(), nil)} // settled: no index, not building
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	r, _, err := handleOutline(ctx, &Config{Cache: cache, DefaultRepo: dir}, OutlineArgs{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	if !strings.Contains(got, "no extractors") || !strings.Contains(got, ".txt ×2") || !strings.Contains(got, ".md ×1") {
		t.Errorf("unsupported message should count files by extension, got: %s", got)
	}
}

// F2: one file out of the structural index is named, not blamed on the repo.
func TestOutline_FileNotInStructuralIndexIsNamed(t *testing.T) {
	dir := t.TempDir()
	for n, body := range map[string]string{"a.go": "package p\n\nfunc A() {}\n", "notes.txt": "hi\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := outlineText(t, dir, "notes.txt")
	if !strings.Contains(got, `"notes.txt" exists but is not in the structural index`) {
		t.Errorf("got: %s", got)
	}
}
