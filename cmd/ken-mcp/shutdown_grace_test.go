//go:build !windows

package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinary_ShutdownGraceOnSignal exercises Fix 1's signal path end to end:
// a SIGINT makes the server drain in-flight requests within the bounded grace
// window and exit cleanly (code 0), rather than hanging or crashing. With no
// in-flight tool call the drain completes immediately, so the process exits
// well inside KEN_MCP_SHUTDOWN_GRACE. (The stdin-EOF path is already covered
// by TestBinary_StdoutIsCleanJSONRPC.)
//
// The signal is gated on a completed `initialize` handshake: once the server
// responds, it is provably inside srv.Run (past signal.NotifyContext), so the
// SIGINT is trapped and handled gracefully rather than default-terminating the
// process — which a fixed sleep raced against startup and made flaky.
func TestBinary_ShutdownGraceOnSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("skips process-signal integration in -short")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken-mcp")
	if out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken-mcp").CombinedOutput(); err != nil {
		t.Fatalf("go build ken-mcp: %v\n%s", err, out)
	}

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"KEN_MCP_MODE=bm25",         // no model needed → fast startup
		"KEN_MCP_AUTO_FETCH=0",      // no network
		"KEN_MCP_SHUTDOWN_GRACE=2s", // small grace so the test stays fast
	)
	stdin, err := cmd.StdinPipe() // held open so the server doesn't EOF-exit before we signal
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Handshake: newline-delimited JSON-RPC over stdio. A response to id:1
	// proves the server reached srv.Run.
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"grace-test","version":"0"}}}`+"\n")

	gotResp := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"id":1`) {
				gotResp <- true
				return
			}
		}
		gotResp <- false
	}()
	select {
	case ok := <-gotResp:
		if !ok {
			_ = cmd.Process.Kill()
			t.Fatal("server closed stdout before responding to initialize")
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("initialize handshake timed out")
	}

	// Now provably serving → SIGINT is trapped and handled gracefully.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		// Signal-driven shutdown with no in-flight work is a clean exit 0
		// (finish treats context.Canceled as clean).
		if werr != nil {
			if ee, ok := werr.(*exec.ExitError); ok {
				t.Fatalf("expected clean exit 0 on SIGINT, got exit code %d (signal-terminated?)", ee.ExitCode())
			}
			t.Fatalf("wait: %v", werr)
		}
	case <-time.After(4 * time.Second): // grace 2s + generous buffer
		_ = cmd.Process.Kill()
		t.Fatal("process did not exit within the grace window after SIGINT (shutdown hung)")
	}
}
