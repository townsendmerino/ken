package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/structural"
)

// TestAppendFile_AppliesEnrichment pins M3: the incremental watch re-index
// (appendFile) must apply the SAME Arm B structural enrichment the initial
// build does, so a file edited mid-session isn't re-indexed without the
// func:/calls:/raises: label (which would leave a heterogeneous index whose
// tokens + embeddings drift from a fresh build). Before the fix, appendFile
// chunked raw text and this label was absent.
func TestAppendFile_AppliesEnrichment(t *testing.T) {
	src := "package auth\n\n" +
		"func ValidateToken(tok string) error { return parse(tok) }\n\n" +
		"func parse(s string) error { return nil }\n"

	root := makeTempRepo(t, map[string]string{"seed.txt": "seed\n"})
	wi := withShortDebounce(t, root, false) // no watcher; drive appendFile directly

	// The label the shared helper produces for this file — the initial build
	// prepends exactly this, so appendFile must too.
	efs := structural.ExtractFile("auth.go", []byte(src))
	if efs == nil {
		t.Fatal("ExtractFile returned nil for a Go file (test setup wrong)")
	}
	label := structural.EnrichFromFileStruct(efs, structural.EnrichOptions{})
	if label == "" {
		t.Fatal("expected a non-empty enrichment label for a Go func file")
	}

	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(wi.chunks)
	wi.appendFile("auth.go")
	if len(wi.chunks) == before {
		t.Fatal("appendFile added no chunks")
	}
	for _, c := range wi.chunks[before:] {
		if !strings.HasPrefix(c.Text, label) {
			t.Errorf("appended chunk not enriched:\n  want prefix %q\n  got %q", label, c.Text)
		}
	}
}
