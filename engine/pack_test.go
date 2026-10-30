package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// writePackRepo writes one file with many chunks about widgets and several
// files with one widget chunk each, so file-saturation decay decides how
// many of the big file's chunks reach the top of the list.
func writePackRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var big strings.Builder
	big.WriteString("package widgets\n\n")
	for i := range 8 {
		fmt.Fprintf(&big, "// resizeWidget%d resizes the widget frame and redraws the widget border.\nfunc resizeWidget%d(w *Widget) {\n", i, i)
		for j := range 40 {
			fmt.Fprintf(&big, "\tw.frame.resize(%d, %d) // resize the widget frame, then redraw the border\n", i, j)
		}
		big.WriteString("\tw.redrawBorder()\n}\n\n")
	}
	files := map[string]string{"widgets/resize.go": big.String()}
	for i := range 6 {
		files[fmt.Sprintf("other/f%d.go", i)] = fmt.Sprintf("package other\n\n// helper%d mentions a widget once.\nfunc helper%d() string { return \"widget\" }\n", i, i)
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSearchWith_ZeroOptionsIsSearchMode(t *testing.T) {
	dir := writePackRepo(t)
	modelDir := modelDirForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "hybrid", ModelDir: modelDir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	const q = "resize the widget frame and redraw the border"
	want, _, err := ws.SearchMode(ctx, q, 10, engine.ModeHybrid)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ws.SearchWith(ctx, q, 10, engine.ModeHybrid, engine.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("SearchWith(zero) returned %d hits, SearchMode %d", len(got), len(want))
	}
	for i := range want {
		if got[i].File != want[i].File || got[i].StartLine != want[i].StartLine || got[i].Score != want[i].Score {
			t.Errorf("hit %d: SearchWith(zero) %s:%d %v, SearchMode %s:%d %v", i, got[i].File, got[i].StartLine, got[i].Score, want[i].File, want[i].StartLine, want[i].Score)
		}
	}
}

func TestSearchWith_BudgetPackingKeepsSameFileChunks(t *testing.T) {
	dir := writePackRepo(t)
	modelDir := modelDirForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "hybrid", ModelDir: modelDir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	const q = "resize the widget frame and redraw the border"
	countBig := func(opts engine.SearchOptions) int {
		hits, _, err := ws.SearchWith(ctx, q, 6, engine.ModeHybrid, opts)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, h := range hits {
			if h.File == "widgets/resize.go" {
				n++
			}
		}
		return n
	}
	shipped, packed := countBig(engine.SearchOptions{}), countBig(engine.SearchOptions{BudgetPacking: true})
	if packed <= shipped {
		t.Errorf("BudgetPacking: %d of the top 6 hits from widgets/resize.go, shipped %d; want more (file saturation off)", packed, shipped)
	}
}
