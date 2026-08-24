//go:build bench

package temporal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	rels := make([]string, 0, len(files))
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, rel)
	}
	return dir, rels
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The trap that would quietly invalidate the whole experiment: a
// substring rename corrupts neighbouring identifiers, and a corpus full
// of mangled names would produce "the semantic arm rescued it" results
// that are really just artifacts of a broken tree.
func TestApplyRename_WordBoundariesOnly(t *testing.T) {
	dir, files := writeTree(t, map[string]string{
		"a.py": "def getUser():\n    return 1\n\ndef getUserName():\n    return getUser()\n",
		"b.py": "from a import getUser\nx = getUser()\n# getUserName stays\n",
	})
	m, err := ApplyRename(dir, "getUser", "fetchAccount", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Touched) != 2 {
		t.Errorf("touched %v, want both files", m.Touched)
	}
	for _, rel := range files {
		body := read(t, dir, rel)
		if strings.Contains(body, "fetchAccountName") {
			t.Errorf("%s: rename bled into getUserName: %q", rel, body)
		}
		if !strings.Contains(body, "getUserName") {
			t.Errorf("%s: getUserName should be untouched: %q", rel, body)
		}
		if strings.Contains(body, "getUser(") {
			t.Errorf("%s: bare getUser survived: %q", rel, body)
		}
	}
}

func TestApplyRename_ErrorsWhenSymbolAbsent(t *testing.T) {
	// A rename that silently no-ops would score as "retrieval survived
	// the rename" while nothing was renamed at all — a false pass.
	dir, files := writeTree(t, map[string]string{"a.py": "x = 1\n"})
	if _, err := ApplyRename(dir, "nosuch", "other", files); err == nil {
		t.Error("renaming an absent symbol must fail loudly, not no-op")
	}
}

func TestApplyMove_ContentSurvivesAtNewPath(t *testing.T) {
	dir, _ := writeTree(t, map[string]string{
		"src.py": "import os\n\ndef target():\n    return 42\n\ndef other():\n    return 0\n",
		"dst.py": "# destination\n",
	})
	m, err := ApplyMove(dir, "src.py", 3, 4, "dst.py")
	if err != nil {
		t.Fatal(err)
	}
	src, dst := read(t, dir, "src.py"), read(t, dir, "dst.py")
	if strings.Contains(src, "def target()") {
		t.Errorf("moved span still in source: %q", src)
	}
	if !strings.Contains(dst, "def target()") || !strings.Contains(dst, "return 42") {
		t.Errorf("moved span missing from destination: %q", dst)
	}
	if !strings.Contains(src, "def other()") {
		t.Errorf("move took more than its span: %q", src)
	}
	// The remap has to follow the content, or a path-based qrel scores
	// a correct retrieval as a miss.
	got := m.Resolve("src.py")
	if len(got) != 2 || got[0] != "src.py" || got[1] != "dst.py" {
		t.Errorf("Resolve(src.py) = %v, want both the original and the destination", got)
	}
}

func TestApplySplit_BothHalvesResolve(t *testing.T) {
	body := strings.Repeat("line\n", 40)
	dir, _ := writeTree(t, map[string]string{"big.go": body})
	m, err := ApplySplit(dir, "big.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	head := read(t, dir, "big.go")
	tail := read(t, dir, "big_part2.go")
	if strings.Count(head, "line") != 20 {
		t.Errorf("head has %d lines, want 20", strings.Count(head, "line"))
	}
	if strings.Count(tail, "line") != 20 {
		t.Errorf("tail has %d lines, want 20", strings.Count(tail, "line"))
	}
	got := m.Resolve("big.go")
	if len(got) != 2 || got[1] != "big_part2.go" {
		t.Errorf("Resolve(big.go) = %v, want the original plus the fragment", got)
	}
}

func TestResolve_UntouchedTargetResolvesToItself(t *testing.T) {
	// Most files survive any mutation. A target the mutation never
	// touched must still resolve, or every unrelated query scores as a
	// drift failure.
	m := Mutation{Kind: "rename", PathMap: map[string]string{"a.py": "b.py"}}
	got := m.Resolve("untouched.py")
	if len(got) != 1 || got[0] != "untouched.py" {
		t.Errorf("Resolve(untouched.py) = %v, want [untouched.py]", got)
	}
}

func TestApplySplit_RejectsOutOfRange(t *testing.T) {
	dir, _ := writeTree(t, map[string]string{"x.py": "a\nb\nc\n"})
	for _, at := range []int{0, 1, 99} {
		if _, err := ApplySplit(dir, "x.py", at); err == nil {
			t.Errorf("split at line %d should fail", at)
		}
	}
}

// The rename arm exists to strip the lexical arm's exact-match anchor
// so the semantic arm has to carry the query alone. A replacement that
// shares any BM25 token leaves the anchor in place and the whole
// measurement becomes an artifact — this is exactly what a "Renamed"+q
// prefix did, silently reporting 96% lexical survival.
func TestDisjointRename_SharesNoTokenWithTheOriginal(t *testing.T) {
	for _, symbol := range []string{
		"getUser", "parse_config", "HTTPServer", "_handler", "Session", "readInt32",
	} {
		replacement := DisjointRename(symbol)
		if SharesToken(symbol, replacement) {
			t.Errorf("DisjointRename(%q) = %q, which still shares a BM25 token", symbol, replacement)
		}
		if replacement != DisjointRename(symbol) {
			t.Errorf("DisjointRename(%q) is not deterministic", symbol)
		}
	}
	// Guard the guard: the naive prefix must be detected as sharing.
	if !SharesToken("getUser", "RenamedgetUser") {
		t.Error("SharesToken failed to catch the prefix case it exists for")
	}
}
