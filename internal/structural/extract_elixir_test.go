package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_ElixirBasics confirms the Elixir extractor handles defmodule
// (module-as-class), def/defp functions with params, local + remote calls,
// import/alias → Imports, and raise → Raises — across both the `do...end` and
// the `do:` keyword body forms.
func TestBuild_ElixirBasics(t *testing.T) {
	dir := t.TempDir()
	src := `defmodule Account do
  import Logger
  alias MyApp.Repo

  def deposit(amt) do
    validate(amt)
    Repo.save(amt)
    amt + 1
  end

  defp validate(amt), do: raise ArgumentError, "negative"
end
`
	if err := os.WriteFile(filepath.Join(dir, "account.ex"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("account.ex")
	if fs == nil {
		t.Fatal("no FileStruct for account.ex")
	}

	funcs := map[string]bool{}
	for _, f := range fs.Functions {
		funcs[f.Name] = true
	}
	for _, want := range []string{"deposit", "validate"} {
		if !funcs[want] {
			t.Errorf("missing function %q; got %v", want, funcs)
		}
	}

	var hasModule bool
	for _, c := range fs.Classes {
		if c.Name == "Account" {
			hasModule = true
		}
	}
	if !hasModule {
		t.Errorf("missing module Account; got %d classes", len(fs.Classes))
	}

	calls := map[string]bool{}
	for _, c := range fs.CalleeNames() {
		calls[c] = true
	}
	if !calls["validate"] || !calls["save"] {
		t.Errorf("expected validate (local) + save (Repo.save remote) in calls; got %v", calls)
	}

	var raisesArg bool
	for _, r := range fs.Raises {
		if r == "ArgumentError" {
			raisesArg = true
		}
	}
	if !raisesArg {
		t.Errorf("expected ArgumentError in Raises; got %v", fs.Raises)
	}

	imports := map[string]bool{}
	for _, im := range fs.Imports {
		imports[im] = true
	}
	if !imports["Logger"] || !imports["Repo"] {
		t.Errorf("expected Logger (import) + Repo (alias MyApp.Repo leaf) in Imports; got %v", fs.Imports)
	}
}
