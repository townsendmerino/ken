package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// K10: a Hit's Text is exactly the file's lines StartLine..EndLine — no Arm B
// enrichment label — and its Hash is sha256 of that text.

// hitTextFixture writes a Go file, a Python file, a markdown file and a file
// with no structural extractor into a fresh temp dir.
func hitTextFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"calc/calc.go": `package calc

// Alpha adds one to whatever Beta returns.
func Alpha() int {
	return Beta() + 1
}

// Beta returns two.
func Beta() int {
	return helper(2)
}

func helper(n int) int { return n }
`,
		"tools/convert.py": `import json


def load_config(path):
    with open(path) as f:
        return json.load(f)


class Converter:
    def convert(self, value):
        if value is None:
            raise ValueError("no value")
        return transform(value)
`,
		"docs/guide.md": `# Guide

Some prose about the converter and the calculator.

## Testing

Run the tests before you push.
`,
		"notes.txt": `plain notes about alpha beta converter and the calculator
second line of notes
`,
	}
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fileLines returns the file's lines start..end (1-based inclusive), each
// with its own line terminator, exactly as the bytes on disk have them.
func fileLines(t *testing.T, root, rel string, start, end int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if start < 1 || end > len(lines) || start > end {
		t.Fatalf("%s: span %d-%d outside the file's %d lines", rel, start, end, len(lines))
	}
	return strings.Join(lines[start-1:end], "")
}

// checkHits asserts the K10 contract on every hit.
func checkHits(t *testing.T, root, label string, hits []engine.Hit) {
	t.Helper()
	for _, h := range hits {
		want := fileLines(t, root, h.File, h.StartLine, h.EndLine)
		if h.Text != want {
			t.Errorf("%s: %s:%d-%d Text = %q, want the file's lines %q", label, h.File, h.StartLine, h.EndLine, h.Text, want)
		}
		for _, line := range strings.Split(h.Text, "\n") {
			if strings.HasPrefix(line, "# ken:") {
				t.Errorf("%s: %s:%d-%d Text carries the enrichment label line %q", label, h.File, h.StartLine, h.EndLine, line)
			}
		}
		sum := sha256.Sum256([]byte(h.Text))
		if want := hex.EncodeToString(sum[:]); h.Hash != want {
			t.Errorf("%s: %s:%d-%d Hash = %s, want sha256(Text) = %s", label, h.File, h.StartLine, h.EndLine, h.Hash, want)
		}
	}
}

func TestHitTextIsFileLines(t *testing.T) {
	queries := []string{"alpha beta", "converter value", "calculator testing", "load config json", "helper"}
	allFiles := []string{"calc/calc.go", "tools/convert.py", "docs/guide.md", "notes.txt"}

	for _, tc := range []struct {
		name              string
		disableEnrichment bool
	}{
		{"enriched", false},
		{"unenriched", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hitTextFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			mode := "bm25"
			modelDir := ""
			if md := filepath.Join("..", "testdata", "model"); fileExists(filepath.Join(md, "model.safetensors")) {
				mode, modelDir = "hybrid", md
			}
			t.Logf("mode %s", mode)
			ws, err := engine.Open(ctx, root, engine.Options{Mode: mode, ModelDir: modelDir, DisableEnrichment: tc.disableEnrichment})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = ws.Close() }()

			seenFiles := map[string]bool{}
			for _, q := range queries {
				hits, err := ws.Search(ctx, q, 20)
				if err != nil {
					t.Fatalf("Search(%q): %v", q, err)
				}
				checkHits(t, root, "Search "+q, hits)
				for _, h := range hits {
					seenFiles[h.File] = true
				}
				hits, _, err = ws.SearchMode(ctx, q, 20, engine.ModeBM25)
				if err != nil {
					t.Fatalf("SearchMode(%q): %v", q, err)
				}
				checkHits(t, root, "SearchMode "+q, hits)
				hits, _, err = ws.SearchFiles(ctx, q, 20, engine.ModeBM25, allFiles)
				if err != nil {
					t.Fatalf("SearchFiles(%q): %v", q, err)
				}
				checkHits(t, root, "SearchFiles "+q, hits)
			}
			for _, f := range allFiles {
				if !seenFiles[f] {
					t.Errorf("no query hit %s; the fixture no longer covers it", f)
				}
			}

			if modelDir == "" {
				t.Log("testdata/model absent: FindRelated not checked")
				return
			}
			for _, f := range allFiles {
				hits, err := ws.FindRelated(ctx, f, 1, 20)
				if err != nil {
					t.Fatalf("FindRelated(%s): %v", f, err)
				}
				checkHits(t, root, "FindRelated "+f, hits)
			}
		})
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestHitHashIgnoresEditsElsewhereInFile is the ledger-identity check: the
// enrichment label lists the whole file's callees, so with the label in Text
// a new call in B changed A's Hash too. A's lines didn't change, so its Hash
// must not.
func TestHitHashIgnoresEditsElsewhereInFile(t *testing.T) {
	root := t.TempDir()
	// Two functions together larger than the regex chunker's 1500-byte
	// target, so it cuts at Bravo's boundary and they land in separate
	// chunks.
	var alpha, bravoBody strings.Builder
	alpha.WriteString("package ledger\n\n// Alpha is the first function.\nfunc Alpha() int {\n\ttotal := 0\n")
	for i := range 80 {
		alpha.WriteString("\ttotal += step(" + strconv.Itoa(i) + ")\n")
	}
	alpha.WriteString("\treturn total\n}\n\n// Bravo is the second function.\nfunc Bravo() int {\n\tx := 0\n")
	for range 60 {
		bravoBody.WriteString("\tx += 1\n")
	}
	before := alpha.String() + bravoBody.String() + "\treturn x + 7\n}\n"
	after := alpha.String() + bravoBody.String() + "\treturn x + fresh()\n}\n"
	path := filepath.Join(root, "ledger.go")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws, err := engine.Open(ctx, root, engine.Options{Mode: "bm25", Watch: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	alphaHit := func() engine.Hit {
		t.Helper()
		hits, err := ws.Search(ctx, "Alpha total step", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, h := range hits {
			if strings.Contains(h.Text, "func Alpha") {
				if strings.Contains(h.Text, "func Bravo") {
					t.Fatalf("Alpha and Bravo share a chunk (%d-%d); the fixture must split them", h.StartLine, h.EndLine)
				}
				return h
			}
		}
		t.Fatalf("no hit contains func Alpha: %+v", hits)
		return engine.Hit{}
	}
	first := alphaHit()
	gen0 := ws.Status().Generation

	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for ws.Status().Generation == gen0 {
		if time.Now().After(deadline) {
			t.Fatal("watcher never republished after the edit")
		}
		time.Sleep(50 * time.Millisecond)
	}
	hits, err := ws.Search(ctx, "fresh", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search(fresh) after edit = %v, %v; want the edited Bravo", hits, err)
	}
	second := alphaHit()
	if second.Hash != first.Hash {
		t.Errorf("Alpha's Hash changed after an edit to Bravo only:\n before %s %q\n after  %s %q", first.Hash, first.Text, second.Hash, second.Text)
	}
}
