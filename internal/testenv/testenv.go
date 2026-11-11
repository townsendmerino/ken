// Package testenv starts the binaries ken's tests build (ken, ken-mcp,
// servers built on mcp.Run) in a scratch environment, so a test run never
// reads or writes the developer's real ~/.ken: the rerank cache, the usage
// log, a downloaded model.
//
// Every test that runs such a binary uses Command or CommandContext.
// internal/buildchecks' TestSubprocessTestsUseTestenv fails for a test file
// that calls os/exec directly for anything but the go and git tools.
package testenv

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// passthrough are the variables a child needs from the real environment:
// where to find programs and temporary space, what Windows needs to start
// a process at all, and where a coverage run collects its data.
var passthrough = []string{"PATH", "TMPDIR", "TEMP", "TMP", "SystemRoot", "GOCOVERDIR"}

// Env returns the scratch environment: the passthrough variables, HOME
// (and USERPROFILE, which is home on Windows) pointing at a new temporary
// directory that the test removes when it ends, and KEN_NO_USAGE_STATS=1.
// Nothing else is inherited, so a KEN_* variable set in the developer's
// shell cannot change what a test sees. Append to it to configure the
// child; a later entry overrides an earlier one.
func Env(t testing.TB) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "KEN_NO_USAGE_STATS=1"}
	for _, k := range passthrough {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Command is exec.Command with Env(t) as the child's environment.
func Command(t testing.TB, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = Env(t)
	return cmd
}

// CommandContext is exec.CommandContext with Env(t) as the child's
// environment.
func CommandContext(t testing.TB, ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = Env(t)
	return cmd
}
