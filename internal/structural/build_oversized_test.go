package structural

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bigGoFile returns a syntactically valid Go file of at least n bytes with
// one function, Big, at the top.
func bigGoFile(n int) string {
	var b strings.Builder
	b.WriteString("package big\n\nfunc Big() int { return len(table) }\n\nvar table = []string{\n")
	for b.Len() < n {
		b.WriteString("\t\"entry-value-padding-to-grow-the-file\",\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func writeBigAndSmall(t *testing.T, bigBytes int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(bigGoFile(bigBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.go"),
		[]byte("package big\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuild_StructuralCapIsSeparateFromEnrichment: Build parses through the
// same guarded path as ExtractFile (C2), but with the structural index's own
// size cap (1 MiB by default), not enrichment's 64 KiB — so an 80 KiB file's
// definitions are found, while its enrichment label is still skipped and
// the Arm B output can't change.
func TestBuild_StructuralCapIsSeparateFromEnrichment(t *testing.T) {
	t.Setenv("KEN_STRUCTURAL_MAX_FILE_BYTES", "")
	dir := writeBigAndSmall(t, maxEnrichBytes+16<<10)
	data, err := os.ReadFile(filepath.Join(dir, "big.go"))
	if err != nil {
		t.Fatal(err)
	}
	if fs := ExtractFile("big.go", data); fs != nil {
		t.Errorf("ExtractFile (the enrichment path) parsed a %d-byte file; its 64 KiB cap must not move", len(data))
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.Defs("Big"); len(got) != 1 {
		t.Errorf("Defs(Big) = %v: the %d-byte file is under the 1 MiB structural cap and should be indexed", got, len(data))
	}
	if ix.File("small.go") == nil {
		t.Error("small.go was not indexed")
	}
}

// TestBuild_StructuralCapGuardsAndConfigures pins the cap's knobs: a file
// over it is skipped (not parsed) while a normal one beside it is indexed;
// KEN_STRUCTURAL_MAX_FILE_BYTES sets it; BuildOptions.MaxFileBytes beats
// the env; and Update keeps the cap the index was built with.
func TestBuild_StructuralCapGuardsAndConfigures(t *testing.T) {
	dir := writeBigAndSmall(t, 80<<10)
	ctx := context.Background()

	t.Setenv("KEN_STRUCTURAL_MAX_FILE_BYTES", "64KiB")
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ix.File("big.go") != nil {
		t.Error("big.go (80 KiB) indexed under KEN_STRUCTURAL_MAX_FILE_BYTES=64KiB")
	}
	if ix.File("small.go") == nil {
		t.Error("small.go not indexed: the guard over-skipped a normal file")
	}

	ix, err = BuildWithOptions(ctx, dir, BuildOptions{MaxFileBytes: 100 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if ix.File("big.go") == nil {
		t.Error("big.go not indexed with BuildOptions.MaxFileBytes=100KiB, which should beat the env's 64KiB")
	}

	// Grow big.go past 100 KiB: Update applies the index's own cap and
	// drops it.
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(bigGoFile(120<<10)), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged []string
	nix, changed := ix.Update(dir, []string{"big.go"}, nil, func(f string, a ...any) { logged = append(logged, f) })
	if !changed || nix.File("big.go") != nil {
		t.Errorf("Update of a file grown past the cap: changed=%v, still indexed=%v; want it dropped", changed, nix.File("big.go") != nil)
	}
	if len(logged) != 1 {
		t.Errorf("Update logged %d lines for the dropped file, want 1", len(logged))
	}

	for _, bad := range []string{"nope", "-5", "0"} {
		t.Setenv("KEN_STRUCTURAL_MAX_FILE_BYTES", bad)
		if got := ResolveMaxStructuralFileBytes(0); got != DefaultMaxStructuralFileBytes {
			t.Errorf("KEN_STRUCTURAL_MAX_FILE_BYTES=%q resolved to %d, want the default", bad, got)
		}
	}
}
