package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/hooksock"
)

func TestServeHook_checksAGoEditFromTheHeldPrograms(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := writeTSRepo(t, map[string]string{
		"go.mod":        "module example.com/shop\n\ngo 1.21\n",
		"main.go":       "package main\n\nimport \"example.com/shop/price\"\n\nfunc main() { println(price.Total(2, 3)) }\n",
		"price/calc.go": "package price\n\nfunc Total(unit float64, n int) float64 { return unit * float64(n) }\n",
	})
	dir, err := os.MkdirTemp("/tmp", "khs")
	if err != nil {
		t.Skip("no short directory for a socket")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	host := engine.NewGoTypedHost(engine.GoTypedOptions{})
	defer host.Close()
	counter := &checkCounter{}
	stop, _, err := ServeHook(&Config{DefaultRepo: root, GoTyped: host, UsageRecorder: counter}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	ask := func(repo, old, new string) (hooksock.Reply, bool) {
		return hooksock.Ask(context.Background(), dir, hooksock.Request{Repo: repo, Max: 15,
			Edits: []hooksock.Edit{{File: "price/calc.go", OldString: old, NewString: new}}})
	}
	got, ok := ask(root, "n int)", "n int, tax float64)")
	if !ok || got.Clean || got.Findings != 1 || !strings.Contains(got.Text, "ERROR: main.go:5: not enough arguments in call to price.Total") {
		t.Errorf("a breaking edit: ok %v, %+v", ok, got)
	}
	if len(got.Items) != 1 || got.Items[0].Kind != "error" || got.Items[0].File != "main.go" || got.Items[0].Line != 5 {
		t.Errorf("the findings are not handed over as items: %+v", got.Items)
	}
	// the end of the turn asks what is broken now: the file as it is, and a newline
	src, _ := os.ReadFile(filepath.Join(root, "price/calc.go"))
	touched := string(src) + "\n"
	if got, ok := hooksock.Ask(context.Background(), dir, hooksock.Request{Repo: root, Existing: true,
		Edits: []hooksock.Edit{{File: "price/calc.go", Content: &touched}}}); !ok || len(got.Existing) != 0 || got.Findings != 0 {
		t.Errorf("a touch of a package that compiles: ok %v, %+v", ok, got)
	}
	if got, ok := ask(root, "unit * float64(n)", "float64(n) * unit"); !ok || !got.Clean || got.Findings != 0 {
		t.Errorf("a clean edit: ok %v, %+v", ok, got)
	}
	// an edit the tool call itself will refuse is declined, so the hook stays silent
	if _, ok := ask(root, "not in the file", "x"); ok {
		t.Error("an edit that cannot be applied was answered")
	}
	// another repository on a server with no allowed roots: answered as go_check_edit answers a `repo` argument
	other := writeTSRepo(t, map[string]string{"go.mod": "module example.com/other\n\ngo 1.21\n", "price/calc.go": "package price\n\nfunc F(n int) {}\n"})
	if got, ok := ask(other, "n int)", "n int, m int)"); !ok || !got.Clean {
		t.Errorf("another local repository: ok %v, %+v", ok, got)
	}
	if _, ok := ask(filepath.Join(root, "nowhere"), "a", "b"); ok {
		t.Error("a directory that does not exist was answered")
	}
	if len(counter.rows) < 2 || counter.rows[0] != "go_check_edit breaks 1" || counter.rows[1] != "go_check_edit clean 0" {
		t.Errorf("counted %v", counter.rows)
	}
}
