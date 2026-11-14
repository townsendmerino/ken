package lspcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var py = Languages{".py": "python"}

func s(v string) *string { return &v }

// A file can be deleted, or moved and then edited under its new name. A file that goes is held as empty contents,
// which is how a language server is shown it.
func TestApply_deleteAndMove(t *testing.T) {
	root := tree(t, map[string]string{"a.py": "x = 1\n", "b.py": "y = 2\n", "c.py": "z = 3\n"})
	p, err := Apply(root, []FileEdit{
		{File: "a.py", Delete: true},
		{File: "b.py", MoveTo: "pkg/b2.py"},
		{File: "pkg/b2.py", OldString: "y = 2", NewString: "y = 3"},
		{File: "new.py", Content: s("n = 0\n")},
		{File: "new.py", Delete: true}, // created and deleted again: never there
	}, py, "only Python")
	if err != nil {
		t.Fatal(err)
	}
	at := func(name string) string { return filepath.Join(root, filepath.FromSlash(name)) }
	if len(p.Order) != 3 {
		t.Fatalf("edited files = %v, want a.py, b.py and pkg/b2.py", p.Order)
	}
	if !p.Deleted[at("a.py")] || len(p.Content[at("a.py")]) != 0 || !p.Deleted[at("b.py")] {
		t.Errorf("a.py and b.py should be deleted with empty contents: %v", p.Deleted)
	}
	if got := string(p.Content[at("pkg/b2.py")]); got != "y = 3\n" || !p.Created[at("pkg/b2.py")] || p.Deleted[at("pkg/b2.py")] {
		t.Errorf("pkg/b2.py = %q created %v", got, p.Created[at("pkg/b2.py")])
	}
	if _, there := p.Content[at("new.py")]; there {
		t.Error("a file created and deleted in the same edit is in the patch")
	}
	for _, tc := range []struct {
		name  string
		edits []FileEdit
		want  string
	}{
		{"delete a file that is not there", []FileEdit{{File: "no.py", Delete: true}}, "does not exist, so it cannot be deleted or moved"},
		{"move onto a file that exists", []FileEdit{{File: "a.py", MoveTo: "b.py"}}, "that file exists"},
		{"move to another language", []FileEdit{{File: "a.py", MoveTo: "a.txt"}}, "only Python"},
		{"move out of the repository", []FileEdit{{File: "a.py", MoveTo: "../a.py"}}, "outside the repository"},
		{"edit a file after deleting it", []FileEdit{{File: "a.py", Delete: true}, {File: "a.py", OldString: "x", NewString: "w"}}, "does not exist"},
	} {
		if _, err := Apply(root, tc.edits, py, "only Python"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one with %q", tc.name, err, tc.want)
		}
	}
	// a deleted file can be written again
	p, err = Apply(root, []FileEdit{{File: "a.py", Delete: true}, {File: "a.py", Content: s("x = 9\n")}}, py, "only Python")
	if err != nil || p.Deleted[at("a.py")] || string(p.Content[at("a.py")]) != "x = 9\n" {
		t.Errorf("delete then write: %v %+v", err, p)
	}
}

func TestProjectDirs(t *testing.T) {
	root := tree(t, map[string]string{
		"a.py": "", "web/x.py": "", "services/api/x.py": "", "services/api/deep/x.py": "",
		".git/x": "", "node_modules/pkg/x": "", "web/dist/x": "",
	})
	got := map[string]bool{}
	for _, d := range ProjectDirs(root, 2) {
		r, _ := filepath.Rel(root, d)
		got[filepath.ToSlash(r)] = true
	}
	for _, want := range []string{".", "web", "services", "services/api"} {
		if !got[want] {
			t.Errorf("ProjectDirs lacks %s: %v", want, got)
		}
	}
	for _, not := range []string{".git", "node_modules", "web/dist", "services/api/deep"} {
		if got[not] {
			t.Errorf("ProjectDirs enters %s", not)
		}
	}
}
