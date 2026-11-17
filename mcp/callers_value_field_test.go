package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// F7: a method passed only as a struct field value (`Emit: s.emitTurn`), with
// no call site anywhere, must come back from `callers` under "Passed as a
// value" — an empty answer reads as "unused" (the 1.19.1 report).
func TestCallers_MethodValueInStructFieldNoCallSite(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"srv.go": "package srv\n\ntype Server struct{}\n\nfunc (s *Server) emitTurn(msg string) {}\n",
		"wire.go": "package srv\n\ntype Hooks struct {\n\tEmit func(string)\n}\n\n" +
			"func (s *Server) hooks() *Hooks {\n\treturn &Hooks{\n\t\tEmit: s.emitTurn,\n\t}\n}\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	b := &RepoBundle{Workspace: ws}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	r, _, err := handleCallers(ctx, &Config{Cache: cache, DefaultRepo: dir}, CallersArgs{Symbol: "emitTurn"})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	for _, want := range []string{"Passed as a value", "wire.go", "Server.hooks"} {
		if !strings.Contains(got, want) {
			t.Errorf("callers(emitTurn) missing %q:\n%s", want, got)
		}
	}
}
