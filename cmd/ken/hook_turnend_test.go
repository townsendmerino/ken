package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F9 option (a): the hook reports only breakage that survives to the end of the agent's turn.

func turnEndRepo(t *testing.T) (root string, edit func(file, old, new string) hookInput) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	t.Setenv("KEN_NO_USAGE_STATS", "1")
	root = t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for name, src := range map[string]string{
		"go.mod":        "module example.com/shop\n\ngo 1.21\n",
		"main.go":       "package main\n\nimport \"example.com/shop/price\"\n\nfunc main() { println(price.Total(2, 3)) }\n",
		"price/calc.go": "package price\n\nfunc Total(unit float64, n int) float64 { return unit * float64(n) }\n",
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
	edit = func(file, old, new string) hookInput {
		var in hookInput
		doc := map[string]any{"cwd": root, "session_id": "s1", "hook_event_name": "PreToolUse", "tool_name": "Edit",
			"tool_input": map[string]any{"file_path": filepath.Join(root, file), "old_string": old, "new_string": new}}
		b, _ := json.Marshal(doc)
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	return root, edit
}

// apply makes the edit the agent's tool call would, after the hook has been asked.
func apply(t *testing.T, root, file, old, new string) {
	t.Helper()
	p := filepath.Join(root, file)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s lacks %q", file, old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookTurnEnd(t *testing.T) {
	root, edit := turnEndRepo(t)
	ctx := context.Background()
	newOpts := func() hookOptions {
		return hookOptions{timeout: time.Minute, max: 15, stateDir: t.TempDir(), endOfTurn: true}
	}
	stop := func(o hookOptions, active bool) string {
		return hookStop(ctx, hookInput{SessionID: "s1", Cwd: root, StopHookActive: active}, o)
	}

	t.Run("breakage the next edit mends is never reported", func(t *testing.T) {
		o := newOpts()
		in := edit("price/calc.go", "n int)", "n int, tax float64)")
		if say := hookPre(ctx, in, o); say != "" {
			t.Fatalf("the edit was reported at once:\n%s", say)
		}
		apply(t, root, "price/calc.go", "n int)", "n int, tax float64)")
		in = edit("main.go", "price.Total(2, 3)", "price.Total(2, 3, 0)")
		if say := hookPre(ctx, in, o); say != "" {
			t.Fatalf("the mending edit was reported:\n%s", say)
		}
		apply(t, root, "main.go", "price.Total(2, 3)", "price.Total(2, 3, 0)")
		if say := stop(o, false); say != "" {
			t.Errorf("the end of the turn reported breakage that was mended:\n%s", say)
		}
	})

	t.Run("breakage that stands at the end of the turn arrives, once", func(t *testing.T) {
		o := newOpts()
		in := edit("price/calc.go", "tax float64)", "tax float64, round bool)")
		if say := hookPre(ctx, in, o); say != "" {
			t.Fatalf("the edit was reported at once:\n%s", say)
		}
		apply(t, root, "price/calc.go", "tax float64)", "tax float64, round bool)")
		say := stop(o, false)
		if !strings.Contains(say, "main.go:5") || !strings.Contains(say, "not enough arguments in call to price.Total") {
			t.Errorf("breakage that stands was not reported:\n%s", say)
		}
		if say := stop(o, false); say != "" {
			t.Errorf("the same breakage was reported again at the next stop:\n%s", say)
		}
	})

	t.Run("the stop that follows a report says nothing", func(t *testing.T) {
		o := newOpts()
		in := edit("price/calc.go", "round bool)", "round bool, more int)")
		_ = hookPre(ctx, in, o)
		apply(t, root, "price/calc.go", "round bool)", "round bool, more int)")
		if say := stop(o, true); say != "" {
			t.Errorf("a stop hook that is already active spoke (a loop):\n%s", say)
		}
	})

	t.Run("without --end-of-turn the report stays at the edit", func(t *testing.T) {
		o := newOpts()
		o.endOfTurn = false
		apply(t, root, "main.go", "price.Total(2, 3, 0)", "price.Total(2, 3, 0, false, 0)") // mend what the cases before left
		in := edit("price/calc.go", "more int)", "more int, again int)")
		if say := hookPre(ctx, in, o); !strings.Contains(say, "ERROR: main.go:5:") {
			t.Errorf("the default did not report:\n%s", say)
		}
		if say := stop(o, false); say != "" {
			t.Errorf("the default left something for the end of the turn:\n%s", say)
		}
	})
}
