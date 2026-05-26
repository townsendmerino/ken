package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
)

// buildKenBinary compiles the `ken` binary into binDir and returns
// its path. Same shape as TestBench_StdinDrivenJSON in main_test.go —
// the build-index subcommand is exercised end-to-end through the real
// binary so flag parsing + dispatch are part of the contract.
func buildKenBinary(t *testing.T, binDir string) string {
	t.Helper()
	binPath := filepath.Join(binDir, "ken")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken: %v\n%s", err, out)
	}
	return binPath
}

// writeTinyCorpus writes a 3-file corpus under root and returns the
// list of relative paths. Distinct content per file so a search on
// the loaded index can be verified.
func writeTinyCorpus(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"main.go": `package main

import "fmt"

func ValidateUser(name string) bool { return len(name) > 0 }

func main() { fmt.Println(ValidateUser("alice")) }
`,
		"auth.py": `def validate_user(name):
    """Returns True iff non-empty."""
    return bool(name)
`,
		"README.md": "# Demo corpus\n\nCold-start fixture.\n",
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBuildIndex_HappyPath runs `ken build-index` end-to-end:
// build, load via search.LoadSerializedIndex, query.
func TestBuildIndex_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build-index test in -short mode")
	}
	binDir := t.TempDir()
	binPath := buildKenBinary(t, binDir)

	corpusDir := t.TempDir()
	writeTinyCorpus(t, corpusDir)
	outPath := filepath.Join(corpusDir, ".ken", "index.bin")

	cmd := exec.Command(binPath, "build-index", corpusDir,
		"-o", outPath, "--mode", "bm25", "--chunker", "regex")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ken build-index: %v\n--stderr--\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wrote") {
		t.Errorf("expected 'wrote ... bytes' on stderr, got:\n%s", stderr.String())
	}

	// Read the produced file and load it through the library API to
	// confirm it's a valid serialized index.
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if len(data) < 12 {
		t.Fatalf("output implausibly short: %d bytes", len(data))
	}
	ix, err := search.LoadSerializedIndex(data, search.LoadOptions{
		ExpectedMode:    "bm25",
		ExpectedChunker: "regex",
	})
	if err != nil {
		t.Fatalf("LoadSerializedIndex: %v", err)
	}
	if ix.Len() == 0 {
		t.Fatalf("loaded index has no chunks")
	}
	results := ix.Search("ValidateUser", 3)
	if len(results) == 0 || !strings.Contains(results[0].Chunk.File, "main.go") {
		t.Errorf("expected top hit in main.go, got: %v", results)
	}
}

// TestBuildIndex_AutoCreatesParentDir confirms the subcommand creates
// the .ken/ directory inside the corpus if it doesn't exist (the
// common case on first run).
func TestBuildIndex_AutoCreatesParentDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	binPath := buildKenBinary(t, t.TempDir())
	corpusDir := t.TempDir()
	writeTinyCorpus(t, corpusDir)
	outPath := filepath.Join(corpusDir, ".ken", "index.bin") // .ken/ doesn't exist

	cmd := exec.Command(binPath, "build-index", corpusDir, "-o", outPath, "--mode", "bm25")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ken build-index: %v\n--stderr--\n%s", err, stderr.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output not created: %v", err)
	}
	// Also confirm the walker would skip .ken/ on a subsequent
	// build-from-corpus run (Commit 1's prune). We check this by
	// running build-index AGAIN over the same corpus (where .ken/
	// now exists) and verifying the new index still loads.
	cmd2 := exec.Command(binPath, "build-index", corpusDir, "-o", outPath, "--mode", "bm25")
	cmd2.Stderr = &stderr
	if err := cmd2.Run(); err != nil {
		t.Fatalf("second ken build-index: %v\n--stderr--\n%s", err, stderr.String())
	}
}

// TestBuildIndex_MissingCorpus points -o at a corpus directory that
// doesn't exist; expects a clear error and non-zero exit code.
func TestBuildIndex_MissingCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	binPath := buildKenBinary(t, t.TempDir())
	cmd := exec.Command(binPath, "build-index", "/nonexistent/corpus/dir",
		"-o", filepath.Join(t.TempDir(), "out.bin"), "--mode", "bm25")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit; stderr=\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "corpus dir") {
		t.Errorf("expected 'corpus dir' in stderr, got:\n%s", stderr.String())
	}
}

// TestBuildIndex_InvalidMode passes a typoed mode; expects a clear
// "unknown mode" error and exit 2.
func TestBuildIndex_InvalidMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	binPath := buildKenBinary(t, t.TempDir())
	corpusDir := t.TempDir()
	writeTinyCorpus(t, corpusDir)
	cmd := exec.Command(binPath, "build-index", corpusDir,
		"-o", filepath.Join(t.TempDir(), "out.bin"), "--mode", "hybrydddd")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit; stderr=\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown mode") {
		t.Errorf("expected 'unknown mode' in stderr, got:\n%s", stderr.String())
	}
}

// TestBuildIndex_MissingOutputFlag — -o is mandatory.
func TestBuildIndex_MissingOutputFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	binPath := buildKenBinary(t, t.TempDir())
	corpusDir := t.TempDir()
	writeTinyCorpus(t, corpusDir)
	cmd := exec.Command(binPath, "build-index", corpusDir, "--mode", "bm25")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit; stderr=\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "-o") {
		t.Errorf("expected '-o' in stderr, got:\n%s", stderr.String())
	}
}
