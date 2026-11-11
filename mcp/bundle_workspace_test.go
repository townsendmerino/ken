package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// A bundle built around an engine.Workspace (how cmd/ken-mcp builds them)
// and one assembled from an index and a structural builder (a third-party
// Builder) must serve the tools identically: the handlers see a Workspace
// either way.
func TestBundle_WorkspaceAndLegacyFieldsServeTheSameAnswers(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.go": "package p\n\n// Alpha adds.\nfunc Alpha() int { return Beta() + 1 }\n\nfunc Beta() int { return 2 }\n",
		"b.go": "package p\n\nfunc Gamma() int { return Alpha() }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	open := func() *engine.Workspace {
		ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	owned := open()  // goes into a bundle as Workspace; the cache closes it
	legacy := open() // supplies the legacy fields; closed here
	defer func() { _ = legacy.Close() }()

	bundles := map[string]*RepoBundle{
		"workspace": {Workspace: owned},
		"legacy": {
			Index: legacy.WatchedIndex(),
			StructuralBuilder: func(ctx context.Context) (*engine.StructuralIndex, error) {
				return legacy.Structural(ctx)
			},
		},
	}
	answers := map[string][]string{}
	for name, b := range bundles {
		cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
		cfg := &Config{Cache: cache, DefaultRepo: dir}
		var out []string
		r1, _, err := handleSearch(ctx, cfg, SearchArgs{Query: "Alpha Beta", TopK: 5, Output: "json"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractText(t, r1))
		r2, _, err := handleDefinition(ctx, cfg, DefinitionArgs{Symbol: "Alpha", Output: "json"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractText(t, r2))
		r3, _, err := handleCallers(ctx, cfg, CallersArgs{Symbol: "Alpha", Output: "json"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractText(t, r3))
		r4, _, err := handleOutline(ctx, cfg, OutlineArgs{Path: ".", Output: "json"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractText(t, r4))
		r5, _, err := handleOutline(ctx, cfg, OutlineArgs{Path: "a.go"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, extractText(t, r5))
		answers[name] = out

		if wi, err := cache.Get(ctx, dir); err != nil || wi == nil {
			t.Errorf("%s: Cache.Get = %v, %v; want the bundle's watched index", name, wi, err)
		}
		if b.StructuralIfBuilt() == nil {
			t.Errorf("%s: StructuralIfBuilt is nil after a structural tool ran", name)
		}
		cache.Close()
	}
	for i := range answers["workspace"] {
		if answers["workspace"][i] != answers["legacy"][i] {
			t.Errorf("answer %d differs between a Workspace bundle and a legacy bundle:\n workspace: %s\n legacy:    %s", i, answers["workspace"][i], answers["legacy"][i])
		}
	}
	if !strings.Contains(answers["workspace"][1], "a.go") {
		t.Errorf("definition did not find Alpha: %s", answers["workspace"][1])
	}
	// Reaping a Workspace bundle closes its Workspace; closing twice is safe.
	if err := owned.Close(); err != nil {
		t.Errorf("second Close of the reaped Workspace: %v", err)
	}
}
