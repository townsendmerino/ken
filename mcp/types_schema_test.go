package mcp

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestRepoArg_InlinedIntoToolSchema guards the #8 embedded-repoArg dedup: the
// go-sdk generates tool schemas by reflecting the arg structs, and jsonschema-go
// must INLINE the anonymous embedded repoArg (promote `repo` to a top-level
// property) — not nest it. There is otherwise no schema-shape test, so a future
// sdk/jsonschema-go bump that stopped inlining embeds would silently break the
// tool arg contract for agents. This catches that.
func TestRepoArg_InlinedIntoToolSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() (*jsonschema.Schema, error)
	}{
		{"DefinitionArgs", func() (*jsonschema.Schema, error) { return jsonschema.For[DefinitionArgs](nil) }},
		{"FindRelatedArgs", func() (*jsonschema.Schema, error) { return jsonschema.For[FindRelatedArgs](nil) }},
		{"SymbolsArgs", func() (*jsonschema.Schema, error) { return jsonschema.For[SymbolsArgs](nil) }},
	} {
		s, err := tc.make()
		if err != nil {
			t.Fatalf("%s: schema: %v", tc.name, err)
		}
		repo, ok := s.Properties["repo"]
		if !ok {
			t.Fatalf("%s: `repo` is not a top-level property — embedded repoArg was not inlined", tc.name)
		}
		if !strings.Contains(repo.Description, "git URL or local directory path") {
			t.Errorf("%s: repo description drifted: %q", tc.name, repo.Description)
		}
	}
}
