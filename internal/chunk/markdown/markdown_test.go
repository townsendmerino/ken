package markdown

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/ken/internal/chunk"
	_ "github.com/townsendmerino/ken/internal/chunk/regex" // unused here but registered alongside markdown in real builds
)

// ============================================================================
// Helpers
// ============================================================================

// chunkMD chunks src as Markdown and asserts byte-fidelity: concatenating
// Chunk.Text in order must reproduce src exactly. Every test below leans
// on this so we can describe expected behavior in terms of chunk count
// and content without separately re-verifying that nothing was dropped.
func chunkMD(t *testing.T, src []byte, chunkSize int) []chunk.Chunk {
	t.Helper()
	c := New()
	chunks, err := c.Chunk(src, "markdown", chunkSize)
	if err != nil {
		t.Fatalf("Chunk(%dB, chunkSize=%d): %v", len(src), chunkSize, err)
	}
	assertByteFidelity(t, src, chunks)
	return chunks
}

func assertByteFidelity(t *testing.T, src []byte, chunks []chunk.Chunk) {
	t.Helper()
	var concat strings.Builder
	for _, c := range chunks {
		concat.WriteString(c.Text)
	}
	if got := concat.String(); got != string(src) {
		t.Errorf("byte-fidelity violation:\n--source (%dB)--\n%s\n--concat (%dB)--\n%s",
			len(src), src, len(got), got)
	}
}

// hugeBlock returns a string of n copies of line.
func hugeBlock(line string, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(line)
	}
	return b.String()
}

// ============================================================================
// 12 scenarios from the v0.6.0 prompt
// ============================================================================

// Scenario 1: simple doc — heading + paragraph + heading + paragraph.
// Expectation: 2 chunks, one per section.
func TestMarkdown_Scenario01_SimpleDoc(t *testing.T) {
	src := []byte("# A\n\nfirst body.\n\n# B\n\nsecond body.\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2\n%s", len(chunks), debugChunks(chunks))
	}
	if !strings.Contains(chunks[0].Text, "# A") || !strings.Contains(chunks[0].Text, "first body") {
		t.Errorf("chunk[0] missing 'A' or first body: %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[1].Text, "# B") || !strings.Contains(chunks[1].Text, "second body") {
		t.Errorf("chunk[1] missing 'B' or second body: %q", chunks[1].Text)
	}
}

// Scenario 2: deep nesting — each heading level is a boundary, so 4
// headings + content = 4 chunks.
func TestMarkdown_Scenario02_DeepNesting(t *testing.T) {
	src := []byte("# H1\nx\n\n## H2\ny\n\n### H3\nz\n\n#### H4\nw\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4\n%s", len(chunks), debugChunks(chunks))
	}
	for i, want := range []string{"# H1", "## H2", "### H3", "#### H4"} {
		if !strings.HasPrefix(chunks[i].Text, want) {
			t.Errorf("chunk[%d] expected to start with %q, got: %q", i, want, chunks[i].Text)
		}
	}
}

// Scenario 3: fenced code stays whole even if a `#` appears inside (the
// load-bearing scanner-state invariant), and `~~~` and ``` both work.
func TestMarkdown_Scenario03_FencedCodeBlocks(t *testing.T) {
	src := []byte("# Doc\n\n```bash\n# This is a comment, not a heading\necho hi\n```\n\n~~~python\n# Same here\nprint('hi')\n~~~\n")
	chunks := chunkMD(t, src, 1500)
	// One heading, one section — the section is small enough to stay one chunk.
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), debugChunks(chunks))
	}
	// The "# This is a comment" line must NOT have caused a split: still inside the same chunk.
	if !strings.Contains(chunks[0].Text, "# This is a comment") {
		t.Errorf("comment-inside-code lost: %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[0].Text, "# Same here") {
		t.Errorf("tilde-fenced code comment lost: %q", chunks[0].Text)
	}
}

// Scenario 4: YAML frontmatter is recognized at the top of the file and
// flows into the first chunk (the first section is just frontmatter +
// body before the first heading; the test confirms it doesn't blow up
// and stays atomic).
func TestMarkdown_Scenario04_YAMLFrontmatter(t *testing.T) {
	src := []byte("---\ntitle: x\ntags: [a, b]\n---\n\n# Body\n\nprose.\n")
	chunks := chunkMD(t, src, 1500)
	// Section 0 is the pre-heading block (frontmatter + blank); section 1 is the heading.
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want >=2\n%s", len(chunks), debugChunks(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "---") {
		t.Errorf("chunk[0] should start with frontmatter ---, got: %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[0].Text, "title: x") {
		t.Errorf("chunk[0] missing frontmatter body: %q", chunks[0].Text)
	}
}

// Scenario 5: a doc with no headings and long prose: it's one section
// that subdivides at paragraph boundaries when it exceeds chunkSize.
func TestMarkdown_Scenario05_NoHeadingsLongProse(t *testing.T) {
	para := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 4) + "\n"
	src := []byte(strings.Repeat(para+"\n", 30)) // 30 paragraphs separated by blanks
	chunks := chunkMD(t, src, 1500)
	if len(chunks) < 2 {
		t.Fatalf("expected paragraph subdivision into multiple chunks, got %d\n%s", len(chunks), debugChunks(chunks))
	}
}

// Scenario 6: a single huge code block exceeding the cap stays whole.
// Splitting mid-fence corrupts the code so this is the hard invariant.
// The section MAY split at the blank line between the heading and the
// fence (a harmless split — the fence stays atomic on the other side),
// but the fence content itself must never be partitioned across chunks.
func TestMarkdown_Scenario06_HugeAtomicCodeBlock(t *testing.T) {
	codeBody := hugeBlock("a = a + 1\n", 1000) // ~10 KB of code lines
	src := []byte("# Big code\n\n```\n" + codeBody + "```\n")
	chunks := chunkMD(t, src, 1500)

	// Find the chunk that opens the fence; assert it also closes the fence.
	// Splitting mid-fence would put the opening ``` in one chunk and the
	// closing ``` in another — the actual corruption we're guarding against.
	var fenceChunk *chunk.Chunk
	for i := range chunks {
		if strings.Contains(chunks[i].Text, "```\n") {
			fenceChunk = &chunks[i]
			break
		}
	}
	if fenceChunk == nil {
		t.Fatalf("no chunk contains the fence open:\n%s", debugChunks(chunks))
	}
	// The chunk containing the fence must contain BOTH the opening and
	// the closing fence (the entire atomic block).
	if strings.Count(fenceChunk.Text, "```\n") < 2 {
		t.Errorf("atomic fence was split across chunks:\n--fenceChunk (%dB)--\n%s",
			len(fenceChunk.Text), fenceChunk.Text[:min(500, len(fenceChunk.Text))])
	}
	// All code-body lines must be in the same chunk as the fence.
	if !strings.Contains(fenceChunk.Text, codeBody) {
		t.Errorf("code body split across chunks (fenceChunk has %d 'a = a + 1' occurrences)",
			strings.Count(fenceChunk.Text, "a = a + 1\n"))
	}
}

// Scenario 7: a doc with a table — the table is atomic (no split between
// header, separator, and rows even though no blank lines appear between).
func TestMarkdown_Scenario07_Table(t *testing.T) {
	src := []byte("# T\n\n| col1 | col2 |\n|------|------|\n| a    | b    |\n| c    | d    |\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), debugChunks(chunks))
	}
	for _, want := range []string{"| col1 | col2 |", "|------|------|", "| a    | b    |", "| c    | d    |"} {
		if !strings.Contains(chunks[0].Text, want) {
			t.Errorf("chunk missing %q", want)
		}
	}
}

// Scenario 8: a doc with nested lists — list block is atomic until a
// blank-line + non-list boundary.
func TestMarkdown_Scenario08_NestedLists(t *testing.T) {
	src := []byte("# L\n\n- item 1\n  - nested 1a\n  - nested 1b\n- item 2\n  - nested 2a\n- item 3\n\nAfter the list.\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), debugChunks(chunks))
	}
	for _, want := range []string{"- item 1", "- nested 1a", "- item 3", "After the list."} {
		if !strings.Contains(chunks[0].Text, want) {
			t.Errorf("chunk missing %q", want)
		}
	}
}

// Scenario 9: empty doc — must not panic, returns no chunks.
func TestMarkdown_Scenario09_EmptyDoc(t *testing.T) {
	for _, src := range [][]byte{nil, {}, []byte("")} {
		chunks, err := New().Chunk(src, "markdown", 1500)
		if err != nil {
			t.Errorf("Chunk(empty): %v", err)
		}
		if len(chunks) != 0 {
			t.Errorf("Chunk(empty): want 0 chunks, got %d", len(chunks))
		}
	}
}

// Scenario 10: mixed-content directory — when routed through
// chunk.ChunkFile, .md files go to markdown, others go to line. This
// confirms the SupportedLanguages-based fallback works end-to-end.
func TestMarkdown_Scenario10_MixedContentRouting(t *testing.T) {
	files := fstest.MapFS{
		"doc.md":    {Data: []byte("# Doc\n\nhi.\n")},
		"prog.go":   {Data: []byte("package main\nfunc main() {}\n")},
		"data.json": {Data: []byte("{\"a\": 1}\n")},
		"notes.txt": {Data: []byte("line1\nline2\n")},
	}

	type result struct {
		file  string
		first string // first chunk text
		count int
	}
	var out []result
	for name, f := range files {
		chunks, err := chunk.ChunkFile("markdown", name, f.Data, 1500)
		if err != nil {
			t.Fatalf("ChunkFile(%q): %v", name, err)
		}
		if len(chunks) == 0 {
			t.Errorf("ChunkFile(%q): 0 chunks", name)
			continue
		}
		out = append(out, result{file: name, first: chunks[0].Text, count: len(chunks)})
	}

	// For doc.md, the first chunk should look like a markdown chunk
	// (heading present). For prog.go / data.json / notes.txt the
	// line-fallback path is used: chunks slice the file by line windows
	// — the exact text is whatever the line chunker emits, but it must
	// be byte-identical to the source on concat.
	for _, r := range out {
		switch r.file {
		case "doc.md":
			if !strings.HasPrefix(r.first, "# Doc") {
				t.Errorf("doc.md first chunk should start with '# Doc', got: %q", r.first)
			}
		default:
			// Confirm round-trip via ChunkFile is byte-faithful too.
			var concat strings.Builder
			cs, _ := chunk.ChunkFile("markdown", r.file, files[r.file].Data, 1500)
			for _, c := range cs {
				concat.WriteString(c.Text)
			}
			if concat.String() != string(files[r.file].Data) {
				t.Errorf("line-fallback byte-fidelity violation on %q", r.file)
			}
		}
	}
}

// Scenario 11: setext headings (=== for H1, --- for H2).
func TestMarkdown_Scenario11_SetextHeadings(t *testing.T) {
	src := []byte("First Title\n===========\n\nbody1\n\nSecond Title\n------------\n\nbody2\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (one per setext heading)\n%s", len(chunks), debugChunks(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "First Title") {
		t.Errorf("chunk[0] should start with 'First Title', got: %q", chunks[0].Text)
	}
	if !strings.HasPrefix(chunks[1].Text, "Second Title") {
		t.Errorf("chunk[1] should start with 'Second Title', got: %q", chunks[1].Text)
	}
}

// Scenario 12: mixed line endings — CRLF + LF in the same file. The
// scanner must not get confused, and byte-fidelity must hold.
func TestMarkdown_Scenario12_MixedLineEndings(t *testing.T) {
	src := []byte("# A\r\n\r\nbody A line 1\r\nbody A line 2\r\n\r\n# B\n\nbody B line 1\nbody B line 2\n")
	chunks := chunkMD(t, src, 1500)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2\n%s", len(chunks), debugChunks(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "# A\r\n") {
		t.Errorf("chunk[0] should start with '# A\\r\\n', got: %q", chunks[0].Text)
	}
	if !strings.HasPrefix(chunks[1].Text, "# B\n") {
		t.Errorf("chunk[1] should start with '# B\\n', got: %q", chunks[1].Text)
	}
}

// ============================================================================
// Cross-cutting invariants
// ============================================================================

// TestMarkdown_IsMarkdownPath documents the exported helper for
// downstream callers (e.g. cmd/ken-mcp-docs deciding which chunker to
// configure).
func TestMarkdown_IsMarkdownPath(t *testing.T) {
	cases := map[string]bool{
		"README.md":      true,
		"docs/guide.md":  true,
		"page.mdx":       true,
		"x.markdown":     true,
		"main.go":        false,
		"data.json":      false,
		"no-ext":         false,
		"README.MD":      true, // case-insensitive
		"weird.MarkDown": true,
	}
	for p, want := range cases {
		if got := IsMarkdownPath(p); got != want {
			t.Errorf("IsMarkdownPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestMarkdown_RegisteredInChunkRegistry confirms init() ran and that
// chunk.Get("markdown") returns this chunker — required for the
// chunker-registry-driven dispatch path used by cmd/ken-mcp and
// mcp.Run when callers set ChunkerName="markdown".
func TestMarkdown_RegisteredInChunkRegistry(t *testing.T) {
	c, err := chunk.Get("markdown")
	if err != nil {
		t.Fatalf("chunk.Get(\"markdown\"): %v", err)
	}
	if c.Name() != "markdown" {
		t.Errorf("chunk.Get(\"markdown\").Name() = %q", c.Name())
	}
	langs := c.SupportedLanguages()
	if len(langs) != 1 || langs[0] != "markdown" {
		t.Errorf("SupportedLanguages() = %v, want [\"markdown\"]", langs)
	}
}

// debugChunks dumps chunks for failure messages.
func debugChunks(chunks []chunk.Chunk) string {
	var b strings.Builder
	for i, c := range chunks {
		fmt.Fprintf(&b, "--- chunk[%d] L%d-L%d (%dB) ---\n%s\n",
			i, c.StartLine, c.EndLine, len(c.Text), c.Text)
	}
	return b.String()
}
