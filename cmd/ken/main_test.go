package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSearchArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantRoot  string
		wantQuery string
		wantK     int
		wantJSON  bool
		wantErr   bool
	}{
		{
			// The regression: a trailing -k used to be swallowed into the
			// query and ignored (Go flag stops at the first positional).
			name:      "flag after positionals (regression)",
			args:      []string{"repo", "find", "this", "thing", "-k", "3"},
			wantRoot:  "repo",
			wantQuery: "find this thing",
			wantK:     3,
		},
		{
			name:      "flag before positionals",
			args:      []string{"-k", "5", "repo", "query"},
			wantRoot:  "repo",
			wantQuery: "query",
			wantK:     5,
		},
		{
			name:      "k=N form, mid-args",
			args:      []string{"repo", "-k=7", "alpha", "beta"},
			wantRoot:  "repo",
			wantQuery: "alpha beta",
			wantK:     7,
		},
		{
			name:      "double-dash long form",
			args:      []string{"repo", "q", "--k", "9"},
			wantRoot:  "repo",
			wantQuery: "q",
			wantK:     9,
		},
		{
			name:      "default k when absent",
			args:      []string{"repo", "hello", "world"},
			wantRoot:  "repo",
			wantQuery: "hello world",
			wantK:     10,
		},
		{
			name:      "--json after positionals",
			args:      []string{"repo", "validate user", "--json"},
			wantRoot:  "repo",
			wantQuery: "validate user",
			wantK:     10,
			wantJSON:  true,
		},
		{
			name:      "--json mixed with -k",
			args:      []string{"--json", "repo", "find", "-k", "3"},
			wantRoot:  "repo",
			wantQuery: "find",
			wantK:     3,
			wantJSON:  true,
		},
		{name: "missing -k value", args: []string{"repo", "q", "-k"}, wantErr: true},
		{name: "non-integer -k", args: []string{"repo", "q", "-k", "abc"}, wantErr: true},
		{name: "non-integer -k swallows trailing junk", args: []string{"repo", "q", "-k", "3abc"}, wantErr: true},
		{name: "negative -k", args: []string{"-k", "-1", "repo", "q"}, wantErr: true},
		{name: "too few positionals", args: []string{"repo"}, wantErr: true},
		{name: "no args", args: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSearchArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSearchArgs(%q) = %+v, want error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSearchArgs(%q) unexpected error: %v", tt.args, err)
			}
			if got.root != tt.wantRoot || got.query != tt.wantQuery || got.k != tt.wantK || got.jsonOut != tt.wantJSON {
				t.Errorf("parseSearchArgs(%q) = {root:%q query:%q k:%d json:%v}, want {root:%q query:%q k:%d json:%v}",
					tt.args, got.root, got.query, got.k, got.jsonOut,
					tt.wantRoot, tt.wantQuery, tt.wantK, tt.wantJSON)
			}
		})
	}
}

// TestBench_StdinDrivenJSON builds the real `ken` binary and drives the
// `bench` subcommand end-to-end: stdin = N query lines, stdout = N JSON
// records, index built once. This is the contract the semble benchmark
// adapter (bench/semble/run_ken.py) depends on, so we test the binary
// directly rather than the in-process helpers — the same way the MCP
// stdout-clean test guards cmd/ken-mcp.
func TestBench_StdinDrivenJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess bench test in -short mode")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken: %v\n%s", err, out)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(repoRoot, "testdata", "repo")

	// stdin: three real queries + one blank line + one '#' comment to
	// exercise the skip rules. Expect exactly three JSON records out.
	stdin := strings.NewReader("validate_user\n\n# comment line\ncircle area\nmakeWidget\n")
	cmd := exec.Command(binPath, "bench", fixture, "--mode", "bm25", "--chunker", "regex", "-k", "3")
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ken bench: %v\n--stderr--\n%s", err, stderr.String())
	}

	type rec struct {
		Query   string `json:"query"`
		Results []struct {
			FilePath  string  `json:"file_path"`
			StartLine int     `json:"start_line"`
			EndLine   int     `json:"end_line"`
			Score     float64 `json:"score"`
			Content   string  `json:"content"`
		} `json:"results"`
	}
	var got []rec
	for line := range strings.SplitSeq(strings.TrimRight(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r rec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3 (blank + '# comment' must be skipped)\nstdout:\n%s", len(got), stdout.String())
	}
	wantQueries := []string{"validate_user", "circle area", "makeWidget"}
	wantTopFile := map[string]string{
		"validate_user": "auth.py",
		"circle area":   "geometry.rs",
		"makeWidget":    "widget.ts",
	}
	for i, r := range got {
		if r.Query != wantQueries[i] {
			t.Errorf("record %d query = %q, want %q", i, r.Query, wantQueries[i])
		}
		if len(r.Results) == 0 {
			t.Errorf("query %q: no results", r.Query)
			continue
		}
		if !strings.HasSuffix(r.Results[0].FilePath, wantTopFile[r.Query]) {
			t.Errorf("query %q: top hit = %s, want suffix %s",
				r.Query, r.Results[0].FilePath, wantTopFile[r.Query])
		}
	}
	// The "indexed N chunks" status message must go to stderr only —
	// stdout is the structured channel the adapter parses.
	if !strings.Contains(stderr.String(), "ken bench: indexed") {
		t.Errorf("expected 'ken bench: indexed …' on stderr, got:\n%s", stderr.String())
	}
}
