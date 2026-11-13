package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

func goRepo(t *testing.T, configJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if configJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, engine.GoConfigFile), []byte(configJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A repository's wrap command is code that repository chose: an agent-supplied path on an unconfined server does not
// get to run it, and the reply says it was dropped. The operator's default repo, and any repo on a confined server, do.
func TestOpenGoTyped_wrapNeedsAVouchedRepo(t *testing.T) {
	const withWrap = `{"configs":[{"name":"default"}],"wrap":["sh","-c","exit 9","--"]}`
	repo := goRepo(t, withWrap)

	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	gt, note, errRes := openGoTyped(&Config{}, repo, "")
	if errRes != nil {
		t.Fatal(resultText(t, errRes))
	}
	if len(gt.Config.Wrap) != 0 || !strings.Contains(note, "wrap command was not used") {
		t.Errorf("unconfined, agent-supplied repo: wrap %v note %q, want it dropped and said", gt.Config.Wrap, note)
	}

	gt, note, errRes = openGoTyped(&Config{DefaultRepo: repo}, "", "")
	if errRes != nil {
		t.Fatal(resultText(t, errRes))
	}
	if len(gt.Config.Wrap) != 4 || strings.Contains(note, "wrap") {
		t.Errorf("operator default repo: wrap %v note %q, want it kept", gt.Config.Wrap, note)
	}

	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", filepath.Dir(repo))
	gt, _, errRes = openGoTyped(&Config{}, repo, "")
	if errRes != nil {
		t.Fatal(resultText(t, errRes))
	}
	if len(gt.Config.Wrap) != 4 {
		t.Errorf("confined server: wrap %v, want it kept", gt.Config.Wrap)
	}
}

func TestOpenGoTyped_refusesURLsAndSaysWhenUnconfigured(t *testing.T) {
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	_, _, errRes := openGoTyped(&Config{}, "https://github.com/example/repo", "")
	if errRes == nil || !strings.Contains(resultText(t, errRes), "local checkout") {
		t.Fatalf("a URL repo must be refused with the reason; got %v", errRes)
	}
	_, note, errRes := openGoTyped(&Config{}, goRepo(t, ""), "")
	if errRes != nil {
		t.Fatal(resultText(t, errRes))
	}
	if !strings.Contains(note, "no "+engine.GoConfigFile) || !strings.Contains(note, "default build configuration") {
		t.Errorf("a repo with no config file must say only the default configuration was asked; note %q", note)
	}
}

func TestGoHandlers_symbolErrorsAreToolResults(t *testing.T) {
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	repo := goRepo(t, "")
	res, _, err := handleGoLocations(context.Background(), &Config{}, GoSymbolArgs{Symbol: "Nope", repoArg: repoArg{Repo: repo}}, (*engine.GoTyped).References)
	if err != nil || !strings.Contains(resultText(t, res), "no declaration found") {
		t.Errorf("unknown symbol: err %v text %q", err, resultText(t, res))
	}
	res, _, _ = handleGoLocations(context.Background(), &Config{}, GoSymbolArgs{repoArg: repoArg{Repo: repo}}, (*engine.GoTyped).References)
	if !strings.Contains(resultText(t, res), "symbol is required") {
		t.Errorf("empty symbol: %q", resultText(t, res))
	}
	res, _, _ = handleGoRename(context.Background(), &Config{}, GoRenameArgs{Symbol: "Hello", repoArg: repoArg{Repo: repo}})
	if !strings.Contains(resultText(t, res), "new_name are required") {
		t.Errorf("rename without a new name: %q", resultText(t, res))
	}
}
