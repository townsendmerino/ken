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
	res, _, _ = handleGoLocations(context.Background(), &Config{}, GoSymbolArgs{Symbol: "Hello", GroupBy: "module", repoArg: repoArg{Repo: repo}}, (*engine.GoTyped).References)
	if !strings.Contains(resultText(t, res), "want one of package, dir, file") {
		t.Errorf("unknown group_by: %q", resultText(t, res))
	}
	res, _, _ = handleGoRename(context.Background(), &Config{}, GoRenameArgs{Symbol: "Hello", repoArg: repoArg{Repo: repo}})
	if !strings.Contains(resultText(t, res), "new_name are required") {
		t.Errorf("rename without a new name: %q", resultText(t, res))
	}
}

// go_uses end to end through the handler: the go command lists the files, the rows come back counted, and the JSON
// form is cut to max while its totals stay whole.
func TestGoUses_handler(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list: skipped under -short")
	}
	if engine.GoPath() == "" {
		t.Skip("go not on PATH")
	}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	repo := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":        "module example.com/m\n\ngo 1.21\n",
		"lib/lib.go":    "package lib\n\ntype T struct{ N int }\n\nfunc New() T { return T{} }\n",
		"app/app.go":    "package app\n\nimport l \"example.com/m/lib\"\n\nfunc F() int {\n\tlib := l.New()\n\treturn lib.N\n}\n",
		"app/a_test.go": "package app\n\nimport \"example.com/m/lib\"\n\nvar _ lib.T\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, _, err := handleGoUses(context.Background(), &Config{}, GoUsesArgs{Importer: "app", Imported: "lib", repoArg: repoArg{Repo: repo}})
	text := resultText(t, res)
	for _, want := range []string{"uses of lib by app: 3 identifiers, 3 lines in 2 files", "New  func   1 non-test  0 test", "T    type   0 non-test  1 test", "T.N  field  1 non-test  0 test", "no " + engine.GoConfigFile} {
		if err != nil || !strings.Contains(text, want) {
			t.Errorf("go_uses lacks %q (err %v):\n%s", want, err, text)
		}
	}
	res, _, _ = handleGoUses(context.Background(), &Config{}, GoUsesArgs{Importer: "app", Imported: "lib", Output: "json", Max: 1, repoArg: repoArg{Repo: repo}})
	if js := resultText(t, res); !strings.Contains(js, `"total": 3`) || strings.Count(js, `"kind"`) != 1 {
		t.Errorf("json with max 1 should carry one row and the full total:\n%s", js)
	}
	res, _, _ = handleGoUses(context.Background(), &Config{}, GoUsesArgs{Importer: "app", repoArg: repoArg{Repo: repo}})
	if !strings.Contains(resultText(t, res), "importer and imported are required") {
		t.Errorf("missing imported: %q", resultText(t, res))
	}
	res, _, _ = handleGoUses(context.Background(), &Config{}, GoUsesArgs{Importer: "nope", Imported: "lib", repoArg: repoArg{Repo: repo}})
	if !strings.Contains(resultText(t, res), "no directory or package") {
		t.Errorf("unknown importer: %q", resultText(t, res))
	}
}

// With a host that keeps answers, the second identical go_references call is served from memory and says so, and the
// status tool reports what the tier holds. With no host (or a host that keeps nothing) nothing is kept or shown.
func TestGoTools_hostKeepsAnswersAndStatusReportsThem(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gopls: skipped under -short")
	}
	if engine.GoplsPath() == "" || engine.GoPath() == "" {
		t.Skip("gopls or go not on PATH")
	}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	repo := t.TempDir()
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		repo = r
	}
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.21\n",
		"a.go":   "package m\n\nfunc Hello() {}\n",
		"b.go":   "package m\n\nfunc Use() { Hello() }\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := engine.NewGoTypedHost(engine.GoTypedOptions{CacheBytes: 1 << 20, MaxResident: -1})
	defer host.Close()
	cfg := &Config{GoTyped: host}
	ask := func() string {
		res, _, err := handleGoLocations(context.Background(), cfg, GoSymbolArgs{Symbol: "Hello", repoArg: repoArg{Repo: repo}}, (*engine.GoTyped).References)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	first, second := ask(), ask()
	if !strings.Contains(first, "b.go") || strings.Contains(first, "INCOMPLETE") {
		t.Fatalf("the first answer should list the use in b.go:\n%s", first)
	}
	if strings.Contains(first, "remembered answer") || !strings.Contains(second, "remembered answer") {
		t.Errorf("first:\n%s\nsecond:\n%s\nwant only the second to be remembered", first, second)
	}
	res, _, err := handleStatus(context.Background(), cfg, StatusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, res); !strings.Contains(text, "Go typed tier (kept between queries)") || !strings.Contains(text, "1 remembered") {
		t.Errorf("status does not report the typed tier's answers:\n%s", text)
	}
	res, _, _ = handleStatus(context.Background(), &Config{GoTyped: engine.NewGoTypedHost(engine.GoTypedOptions{MaxResident: -1})}, StatusArgs{})
	if text := resultText(t, res); strings.Contains(text, "Go typed tier") {
		t.Errorf("a server that keeps nothing must not report a typed tier:\n%s", text)
	}
}

// go_references marks the lines that name a corresponding method and, with direct_only, leaves them out.
func TestGoReferences_directOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command: skipped under -short")
	}
	if engine.GoPath() == "" {
		t.Skip("go not on PATH")
	}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	repo := t.TempDir()
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		repo = r
	}
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.21\n",
		"a.go":   "package m\n\ntype Shape interface{ Area() int }\n\ntype Sq struct{}\n\nfunc (Sq) Area() int { return 1 }\n",
		"b.go":   "package m\n\nfunc Use(s Shape) int {\n\treturn s.Area() +\n\t\tSq{}.Area()\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(args GoSymbolArgs) string {
		args.Symbol, args.Repo = "Shape.Area", repo
		res, _, err := handleGoLocations(context.Background(), &Config{}, args, (*engine.GoTyped).References)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	all := ask(GoSymbolArgs{})
	for _, want := range []string{"2 lines in 1 files", "1 direct, 1 through a corresponding method", "(via m.Sq.Area)"} {
		if !strings.Contains(all, want) {
			t.Errorf("go_references lacks %q:\n%s", want, all)
		}
	}
	direct := ask(GoSymbolArgs{DirectOnly: true})
	if !strings.Contains(direct, "1 lines in 1 files") || !strings.Contains(direct, "direct uses only") || strings.Contains(direct, "Sq{}.Area()") {
		t.Errorf("direct_only should leave the call on Sq out and say so:\n%s", direct)
	}
	if js := ask(GoSymbolArgs{Output: "json"}); !strings.Contains(js, `"through": "m.Sq.Area"`) || !strings.Contains(js, `"labelled": true`) || !strings.Contains(js, `"direct": 1`) {
		t.Errorf("the JSON form should carry through, labelled and direct:\n%s", js)
	}
}
