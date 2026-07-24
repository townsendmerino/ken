package structural

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuild_OversizedSkipped pins C2: Build must apply the same
// maxEnrichBytes size guard as ExtractFile before parsing, so a file over
// the ceiling (which could FATAL-stack-overflow gotreesitter's GLR parser
// and crash the whole server) is skipped — not parsed. Both paths now route
// through extractGuarded, so this also guards against the guards drifting
// apart again. A normal file alongside it must still be indexed.
func TestBuild_OversizedSkipped(t *testing.T) {
	dir := t.TempDir()

	// A syntactically-valid Go file well over maxEnrichBytes (64 KiB) but
	// under the 2 MiB walk cap — the exact band the review flagged as
	// reaching Build.Parse unguarded.
	var b strings.Builder
	b.WriteString("package big\n\nvar table = []string{\n")
	for b.Len() < (maxEnrichBytes + 16<<10) { // comfortably over 64 KiB
		b.WriteString("\t\"entry-value-padding-to-grow-the-file\",\n")
	}
	b.WriteString("}\n")
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.go"),
		[]byte("package big\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fs := ix.File("big.go"); fs != nil {
		t.Errorf("big.go (%d bytes > maxEnrichBytes %d) was indexed; the Build size guard didn't fire", b.Len(), maxEnrichBytes)
	}
	if fs := ix.File("small.go"); fs == nil {
		t.Error("small.go was not indexed (the guard over-skipped a normal file)")
	}
}
