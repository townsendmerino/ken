package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

func prefixBundle(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"backend/webgpu/residency.go":       "package webgpu\n\nfunc Resident() {}\n",
		"backend/webgpu/residency_build.go": "package webgpu\n\nfunc BuildResident() {}\n",
		"backend/webgpu/other.go":           "package webgpu\n\nfunc Other() {}\n",
		"backend/cuda/cuda.go":              "package cuda\n\nfunc Cuda() {}\n",
	}
	for n, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	b := &RepoBundle{Workspace: ws}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	return &Config{Cache: cache, DefaultRepo: dir}, dir
}

// F3: a path ending inside a file name matches by plain prefix.
func TestSymbols_PlainPathPrefix(t *testing.T) {
	cfg, _ := prefixBundle(t)
	r, _, err := handleSymbols(context.Background(), cfg, SymbolsArgs{Path: "backend/webgpu/residency"})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	for _, want := range []string{"Resident", "BuildResident"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in: %s", want, got)
		}
	}
	if strings.Contains(got, "Other") || strings.Contains(got, "Cuda") {
		t.Errorf("matched files outside the prefix: %s", got)
	}
}

// F3: zero matches say what was matched and the nearest existing directory.
func TestSymbols_ZeroMatchNamesNearestDirectory(t *testing.T) {
	cfg, _ := prefixBundle(t)
	r, _, err := handleSymbols(context.Background(), cfg, SymbolsArgs{Path: "backend/webgpu/nope/deep"})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	if !strings.Contains(got, "backend/webgpu/nope/deep") || !strings.Contains(got, "nearest existing directory: backend/webgpu/") {
		t.Errorf("got: %s", got)
	}
	r, _, _ = handleOutline(context.Background(), cfg, OutlineArgs{Path: "backend/cudaa/x"})
	got = extractText(t, r)
	if !strings.Contains(got, "nearest existing directory: backend/") {
		t.Errorf("outline zero-match: %s", got)
	}
}
