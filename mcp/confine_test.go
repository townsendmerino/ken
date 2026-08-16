package mcp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfineLocalRepo_DisabledByDefault(t *testing.T) {
	// Env unset ⇒ any local path is allowed (historical behavior).
	if err := confineLocalRepo("/etc"); err != nil {
		t.Errorf("confinement should be off by default; got %v", err)
	}
}

func TestConfineLocalRepo_Enforced(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	t.Setenv(envAllowedRepoRoots, root)

	// A path under the root is allowed (the root itself, and a subdir).
	if err := confineLocalRepo(root); err != nil {
		t.Errorf("root itself should be allowed; got %v", err)
	}
	sub := filepath.Join(root, "project")
	if err := confineLocalRepo(sub); err != nil {
		t.Errorf("subdir of root should be allowed; got %v", err)
	}

	// A path outside every root is rejected.
	if err := confineLocalRepo(outside); err == nil {
		t.Errorf("path outside allowed roots should be rejected")
	} else if !strings.Contains(err.Error(), "outside the allowed roots") {
		t.Errorf("error should explain confinement; got %v", err)
	}

	// A shared string prefix must NOT count as "under" (root vs root-sibling).
	sibling := root + "-sibling"
	if err := confineLocalRepo(sibling); err == nil {
		t.Errorf("%q shares a string prefix with the root but is not under it; should be rejected", sibling)
	}

	// Remote URLs bypass path confinement (guarded separately by clone.go).
	if err := confineLocalRepo("https://github.com/org/repo"); err != nil {
		t.Errorf("http(s) URL should bypass path confinement; got %v", err)
	}
}

func TestConfineLocalRepo_MultipleRoots(t *testing.T) {
	a, b, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv(envAllowedRepoRoots, a+string(filepath.ListSeparator)+b)

	for _, ok := range []string{a, b, filepath.Join(b, "x")} {
		if err := confineLocalRepo(ok); err != nil {
			t.Errorf("%q should be allowed under one of the roots; got %v", ok, err)
		}
	}
	if err := confineLocalRepo(outside); err == nil {
		t.Errorf("%q is under neither root; should be rejected", outside)
	}
}

// TestResolveRepo_DefaultExemptFromConfinement pins that the operator-configured
// DefaultRepo bypasses confinement even when it sits outside the allowed roots —
// only agent-supplied args are confined.
func TestResolveRepo_DefaultExemptFromConfinement(t *testing.T) {
	allowed := t.TempDir()
	defaultRepo := t.TempDir() // deliberately NOT under `allowed`
	t.Setenv(envAllowedRepoRoots, allowed)

	cfg := &Config{DefaultRepo: defaultRepo}

	// No arg → falls back to the default, which is exempt.
	got, err := resolveRepo(cfg, "")
	if err != nil {
		t.Fatalf("default repo should be exempt from confinement; got %v", err)
	}
	if got != defaultRepo {
		t.Errorf("resolveRepo(\"\") = %q, want the default %q", got, defaultRepo)
	}

	// Agent-supplied arg outside the roots → rejected.
	if _, err := resolveRepo(cfg, defaultRepo); err == nil {
		t.Errorf("agent-supplied path outside roots should be rejected even if it equals the default")
	}
}
