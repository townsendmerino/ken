package hooksock

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// shortDir is a temporary directory with a path short enough for a Unix socket (t.TempDir's is not, on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "khs")
	if err != nil {
		dir, err = os.MkdirTemp("", "khs")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServeAndAsk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not checked on Windows")
	}
	dir := filepath.Join(shortDir(t), "run")
	ctx := context.Background()
	if _, ok := Ask(ctx, dir, Request{Repo: "/r"}); ok {
		t.Error("an answer with no server")
	}
	stop, path, err := Serve(dir, func(_ context.Context, req Request) Reply {
		if req.Repo == "/declined" {
			return Reply{Err: "not a repository this server may open"}
		}
		return Reply{Text: req.Repo + " " + req.Edits[0].File, Findings: 2}
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v, want 0700", st.Mode().Perm())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", st.Mode().Perm())
	}
	// a socket nothing listens on, left by a server that was killed
	dead := filepath.Join(dir, "mcp-1.sock")
	ln, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	_ = os.Chtimes(dead, time.Now().Add(time.Hour), time.Now().Add(time.Hour)) // asked first

	got, ok := Ask(ctx, dir, Request{Repo: "/r", Edits: []Edit{{File: "a.go"}}})
	if !ok || got.Text != "/r a.go" || got.Findings != 2 {
		t.Errorf("reply %+v, ok %v", got, ok)
	}
	if _, err := os.Stat(dead); err == nil {
		t.Error("the dead socket was left in place")
	}
	if _, ok := Ask(ctx, dir, Request{Repo: "/declined", Edits: []Edit{{File: "a.go"}}}); ok {
		t.Error("a declined request counted as an answer")
	}
	stop()
	stop() // twice is harmless
	if _, err := os.Stat(path); err == nil {
		t.Error("the socket file outlived the server")
	}
	if _, ok := Ask(ctx, dir, Request{Repo: "/r", Edits: []Edit{{File: "a.go"}}}); ok {
		t.Error("an answer after the server stopped")
	}
}

func TestAsk_givesUpWhenTheTimeIsUp(t *testing.T) {
	dir := shortDir(t)
	release := make(chan struct{})
	stop, _, err := Serve(dir, func(ctx context.Context, _ Request) Reply {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Reply{Text: "late"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := Ask(ctx, dir, Request{Repo: "/r"}); ok || time.Since(start) > 3*time.Second {
		t.Errorf("ok %v after %v", ok, time.Since(start))
	}
}
