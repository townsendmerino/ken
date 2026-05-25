package mcpdb_test

// Test harness for the mcp.Run + mcp/db.Setup binary tests. The
// mini-binary at testdata/embedded-with-db/main.go is built per-test
// via `go build` and exec'd via sdk.CommandTransport, matching the
// pattern cmd/ken-mcp's TestBinary_StdoutIsCleanJSONRPC tests use.
//
// We can't reuse cmd/ken-mcp's binary here because the v0.6.0
// contract this file verifies is specifically about the mcp.Run +
// mcp/db.Setup code path — a separate binary, separate dep tree,
// separate test surface.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// buildEmbeddedWithDBBinary compiles testdata/embedded-with-db into
// a per-test temp file and returns its path. Built once per test so
// each test gets a fresh executable.
func buildEmbeddedWithDBBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "embedded-with-db")

	// testdata/embedded-with-db lives at ./testdata/embedded-with-db
	// relative to the package directory.
	srcDir, err := filepath.Abs("testdata/embedded-with-db")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "build", "-o", binPath, srcDir).CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", srcDir, err, out)
	}
	return binPath
}

// newBinaryCommand prepares an exec.Cmd that spawns the mini-binary
// with the given DSN passed via the MY_DB_DSN env var. Empty DSN is
// the "no DB" branch.
func newBinaryCommand(t *testing.T, ctx context.Context, binPath, dsn string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"MY_DB_DSN=" + dsn, // mini-binary reads this; matches the README example
	}
	return cmd
}

// stderrBuf is a mutex-wrapped bytes.Buffer for thread-safe stderr
// capture from the spawned binary. sdk.CommandTransport hands the
// child's stderr to whatever we set on cmd.Stderr.
type stderrBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newStderrBuf() *stderrBuf { return &stderrBuf{} }

func (b *stderrBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stderrBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
