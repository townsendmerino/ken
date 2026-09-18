package search

import (
	"testing"

	_ "github.com/townsendmerino/aikit/chunk/markdown"
)

// chunkOneFile silently swaps in the "markdown" chunker for .md files
// whenever it's registered in the binary (cmd/ken, cmd/ken-mcp, and now
// cmd/ken perf all blank-import it), regardless of the caller-requested
// chunker — except when "line" was requested explicitly, which stays an
// escape hatch. This had zero coverage; a regression here would silently
// change what every shipped binary's default --chunker=regex produces for
// every .md file in a repo.
func TestChunkOneFile_MarkdownAutoRoute(t *testing.T) {
	data := []byte("# Title\n\nIntro paragraph.\n\n## Section One\n\nBody text for section one.\n\n## Section Two\n\nMore body text here for section two.\n")

	want, err := chunkOneFile("markdown", "README.md", data, false)
	if err != nil {
		t.Fatalf("chunkOneFile(markdown): %v", err)
	}
	if len(want) < 2 {
		t.Fatalf("fixture didn't exercise heading-based splitting: got %d chunk(s) from the markdown chunker", len(want))
	}

	got, err := chunkOneFile("regex", "README.md", data, false)
	if err != nil {
		t.Fatalf("chunkOneFile(regex): %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("chunkOneFile(regex, README.md) = %d chunks, want %d (should auto-route to the registered markdown chunker)", len(got), len(want))
	}
	for i := range got {
		if got[i].Text != want[i].Text {
			t.Fatalf("chunk %d text differs between requested=regex and requested=markdown; auto-route did not actually use the markdown chunker", i)
		}
	}

	// "line" is the explicit opt-out and must never be silently overridden.
	lineChunks, err := chunkOneFile("line", "README.md", data, false)
	if err != nil {
		t.Fatalf("chunkOneFile(line): %v", err)
	}
	if len(lineChunks) != 1 {
		t.Fatalf("chunkOneFile(line, README.md) = %d chunks, want 1 (whole-file fallback; this fixture is under the line chunker's 50-line threshold) — got %d, meaning \"line\" was overridden", len(lineChunks), len(lineChunks))
	}

	// A non-markdown file must never be routed to the markdown chunker.
	goData := []byte("package foo\n\nfunc Bar() {}\n")
	goChunks, err := chunkOneFile("regex", "foo.go", goData, false)
	if err != nil {
		t.Fatalf("chunkOneFile(regex, foo.go): %v", err)
	}
	if len(goChunks) != 1 || goChunks[0].Text != string(goData) {
		t.Fatalf("chunkOneFile(regex, foo.go) unexpectedly diverged — markdown routing must be gated on Language(rel) == \"markdown\"")
	}
}
