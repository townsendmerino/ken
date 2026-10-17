package engine

// sections_test.go — K9's primary correctness check: every markdown
// chunk's line range must fall inside exactly one Section with an
// identical HeadingPath. White-box (package engine) so it can enumerate
// every chunk the real markdown chunker produced directly, rather than
// depending on which chunks happen to rank into a query's top-K — the
// brief's invariant is about every hit, so the test checks every chunk
// that could ever become one.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSections_AgreeWithRealChunks runs the invariant over ken's own
// CLAUDE.md (read from the live repo checkout) and the nested-heading
// fixture above (preamble, 3-deep ATX nesting, setext headings, a code
// fence whose "#" line must not be mistaken for a heading).
func TestSections_AgreeWithRealChunks(t *testing.T) {
	claudeMD, err := os.ReadFile(filepath.Join("..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read ../CLAUDE.md: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), claudeMD, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.md"), []byte(mdFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := Open(ctx, dir, Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	ix := w.index.Load()
	byFile := map[string][]Section{}
	checked := 0
	for _, c := range ix.Chunks() {
		if c.Tombstoned {
			continue
		}
		secs, ok := byFile[c.File]
		if !ok {
			secs, err = w.Sections(ctx, c.File)
			if err != nil {
				t.Fatalf("Sections(%q): %v", c.File, err)
			}
			byFile[c.File] = secs
		}

		var containing *Section
		for i := range secs {
			if c.StartLine >= secs[i].StartLine && c.StartLine <= secs[i].EndLine {
				containing = &secs[i]
				break
			}
		}
		if containing == nil {
			t.Errorf("%s:%d-%d: StartLine falls inside no Section", c.File, c.StartLine, c.EndLine)
			continue
		}
		if c.EndLine > containing.EndLine {
			t.Errorf("%s:%d-%d: chunk spans past its Section (ends at line %d, section ends at %d, path %v)",
				c.File, c.StartLine, c.EndLine, c.EndLine, containing.EndLine, containing.HeadingPath)
		}

		got := w.headingPathFor(c.File, c.StartLine, map[string][]Section{c.File: secs})
		if !slicesEqual(got, containing.HeadingPath) {
			t.Errorf("%s:%d: headingPathFor = %v, want %v (the containing Section's path)",
				c.File, c.StartLine, got, containing.HeadingPath)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no markdown chunks were checked — fixture/indexing problem")
	}
	t.Logf("checked %d chunks across %d markdown files", checked, len(byFile))
}

// TestSections_PreambleHasEmptyPath pins the explicit brief requirement:
// text before the first heading is its own Section with an empty path.
func TestSections_PreambleHasEmptyPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.md"), []byte(mdFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := Open(ctx, dir, Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	secs, err := w.Sections(ctx, "fixture.md")
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	if len(secs) == 0 {
		t.Fatal("no sections returned")
	}
	if len(secs[0].HeadingPath) != 0 || secs[0].Level != 0 {
		t.Errorf("first section = %+v, want an empty HeadingPath and Level 0 (the preamble)", secs[0])
	}
	if secs[0].StartLine != 1 {
		t.Errorf("first section StartLine = %d, want 1", secs[0].StartLine)
	}
}

// TestSections_UnknownFileErrors: Sections on a path not in the index
// returns a clear error rather than silently reading an arbitrary file.
func TestSections_UnknownFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.md"), []byte(mdFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := Open(ctx, dir, Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = w.Close() }()

	if _, err := w.Sections(ctx, "does-not-exist.md"); err == nil {
		t.Error("Sections(unindexed path) = nil error, want an error")
	}
}
