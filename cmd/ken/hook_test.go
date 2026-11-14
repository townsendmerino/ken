package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookCheck(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	t.Setenv("KEN_NO_USAGE_STATS", "1")
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for name, src := range map[string]string{
		"go.mod":        "module example.com/shop\n\ngo 1.21\n",
		"main.go":       "package main\n\nimport \"example.com/shop/price\"\n\nfunc main() { println(price.Total(2, 3)) }\n",
		"price/calc.go": "package price\n\n// Total is the price of n units.\nfunc Total(unit float64, n int) float64 { return unit * float64(n) }\n",
		"notes.md":      "notes\n",
		".git/HEAD":     "ref: refs/heads/main\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call := func(tool, input string) hookInput {
		var in hookInput
		doc := `{"cwd": ` + string(mustJSON(t, filepath.Join(root, "price"))) + `, "hook_event_name": "PreToolUse", "tool_name": "` + tool + `", "tool_input": ` + input + `}`
		if err := json.Unmarshal([]byte(doc), &in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	calc := string(mustJSON(t, filepath.Join(root, "price", "calc.go")))
	var why bytes.Buffer
	o := hookOptions{timeout: time.Minute, max: 15, why: &why}

	// an edit that breaks a caller in another package: the repository is found from the file, not the session's cwd
	text, tier := hookCheck(context.Background(), call("Edit", `{"file_path": `+calc+`, "old_string": "n int)", "new_string": "n int, tax float64)"}`), o)
	if tier != "go" || !strings.Contains(text, "ERROR: main.go:5: not enough arguments in call to price.Total") ||
		!strings.Contains(text, "nothing was blocked") || !strings.Contains(text, "ken checked this edit to price/calc.go") {
		t.Errorf("a breaking edit: tier %q\n%s\n%s", tier, text, why.String())
	}
	reply, _ := json.Marshal(hookReply("PreToolUse", text))
	if !strings.HasPrefix(string(reply), `{"hookSpecificOutput":{"additionalContext":"ken checked`) || strings.Contains(string(reply), "permissionDecision") {
		t.Errorf("reply decides something about the tool call: %s", reply)
	}

	for name, in := range map[string]hookInput{
		"a clean edit":           call("Edit", `{"file_path": `+calc+`, "old_string": "unit * float64(n)", "new_string": "float64(n) * unit"}`),
		"an edit that will fail": call("Edit", `{"file_path": `+calc+`, "old_string": "not in the file", "new_string": "x"}`),
		"a file of no tier":      call("Write", `{"file_path": `+string(mustJSON(t, filepath.Join(root, "notes.md")))+`, "content": "more\n"}`),
		"another tool":           call("Bash", `{"command": "ls"}`),
		"a file outside":         call("Write", `{"file_path": "/nowhere/else/x.go", "content": "package x\n"}`),
	} {
		if text, _ := hookCheck(context.Background(), in, o); text != "" {
			t.Errorf("%s: the hook spoke:\n%s", name, text)
		}
	}
	if !strings.Contains(why.String(), "price/calc.go not checked") {
		t.Errorf("--why did not say why the failing edit was left alone: %q", why.String())
	}
	// a whole new file, given by a path relative to the session's directory
	text, _ = hookCheck(context.Background(), call("Write", `{"file_path": "extra.go", "content": "package price\n\nvar x int = \"no\"\n"}`), o)
	if !strings.Contains(text, "ERROR: price/extra.go:3:") {
		t.Errorf("a new file with an error:\n%s", text)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
