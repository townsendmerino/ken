// External test package: importing the regex sub-chunker here (to trigger
// its init() registration) would be an import cycle from package chunk,
// so these live in chunk_test.
package chunk_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/chunk"
	_ "github.com/townsendmerino/ken/chunk/regex" // registers "regex"
)

func TestRegistry(t *testing.T) {
	for _, name := range []string{"line", "regex"} {
		c, err := chunk.Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		if c.Name() != name {
			t.Errorf("Get(%q).Name() = %q", name, c.Name())
		}
	}
	if _, err := chunk.Get("bogus"); err == nil {
		t.Error("Get(bogus) should error")
	}
	if names := chunk.Names(); !slices.Contains(names, "line") || !slices.Contains(names, "regex") {
		t.Errorf("Names() = %v, want to contain line and regex", names)
	}
}

func TestChunkFile_RegexStampsFileAndIsByteExact(t *testing.T) {
	src := []byte("package x\n\nfunc A() {}\n\nfunc B() {}\n")
	cs, err := chunk.ChunkFile("regex", "pkg/x.go", src, chunk.DefaultChunkSize)
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	var b strings.Builder
	for _, c := range cs {
		if c.File != "pkg/x.go" {
			t.Errorf("chunk File = %q, want pkg/x.go", c.File)
		}
		b.WriteString(c.Text)
	}
	if b.String() != string(src) {
		t.Errorf("regex chunking not byte-exact:\n got %q\nwant %q", b.String(), src)
	}
}

func TestChunkFile_UnsupportedLanguageFallsBackToLine(t *testing.T) {
	// 120-line markdown: "markdown" is not regex-supported, so ChunkFile
	// must route to the line chunker — whose 50/5 overlapping windows make
	// the concatenation LONGER than the source (a signature the byte-exact
	// regex chunker would never produce).
	var sb strings.Builder
	for i := range 120 {
		sb.WriteString("para ")
		sb.WriteByte(byte('a' + i%26))
		sb.WriteByte('\n')
	}
	src := []byte(sb.String())

	cs, err := chunk.ChunkFile("regex", "docs/readme.md", src, chunk.DefaultChunkSize)
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	if len(cs) < 2 {
		t.Fatalf("expected overlapping line windows (≥2 chunks), got %d", len(cs))
	}
	total := 0
	for _, c := range cs {
		if c.File != "docs/readme.md" {
			t.Errorf("chunk File = %q, want docs/readme.md", c.File)
		}
		total += len(c.Text)
	}
	if total <= len(src) {
		t.Errorf("expected line-chunker overlap (total %d > source %d); fallback not taken",
			total, len(src))
	}
}

func TestChunkFile_UnknownChunker(t *testing.T) {
	if _, err := chunk.ChunkFile("treesitter", "a.go", []byte("x"), 0); err == nil {
		t.Error("ChunkFile with unknown chunker should error")
	}
}
