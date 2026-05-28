package chunk

import "testing"

func TestLanguage(t *testing.T) {
	cases := map[string]string{
		"a/b/main.go":    "go",
		"pkg/foo.py":     "python",
		"x.pyi":          "python",
		"src/app.ts":     "typescript",
		"src/app.tsx":    "typescript",
		"web/util.js":    "typescript", // JS routes through the TS ruleset
		"web/util.mjs":   "typescript",
		"Main.java":      "java",
		"lib.rs":         "rust",
		"notes.md":       "markdown",
		"data.json":      "json",
		"weird.xyz":      "",
		"noext":          "",
		"Dockerfile":     "dockerfile",
		"Makefile":       "make",
		"path/to/go.mod": "gomod",
		"UPPER.GO":       "go", // case-insensitive
	}
	for p, want := range cases {
		if got := Language(p); got != want {
			t.Errorf("Language(%q) = %q, want %q", p, got, want)
		}
	}
}
