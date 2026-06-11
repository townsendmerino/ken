package modelfetch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHF spins up an httptest.Server that mimics HuggingFace's
// resolve/main/<file> URL convention. Tests inject its URL as the
// Options.BaseURL so production code goes through the real code path
// without hitting the real CDN.
//
// The handler matches /<org>/<name>/resolve/main/<filename>. Any
// non-matching path 404s — that's deliberate; the failed-download test
// uses this to verify partial-file cleanup.
func fakeHF(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path shape: /<org>/<name>/resolve/main/<filename>
		// We don't care about org/name here — we key off the filename.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[2] != "resolve" || parts[3] != "main" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		body, ok := files[parts[4]]
		if !ok {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", lenHeader(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lenHeader(n int) string {
	// strconv.Itoa avoided so the test file stays import-light.
	if n == 0 {
		return "0"
	}
	var d [20]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}

func TestFetch_SuccessDownloadsAllThree(t *testing.T) {
	srv := fakeHF(t, map[string][]byte{
		"model.safetensors": bytes.Repeat([]byte{0xab}, 8<<20), // 8 MB to exercise the progress path
		"tokenizer.json":    []byte(`{"version":"1.0"}`),
		"config.json":       []byte(`{"hidden_size":256}`),
	})
	dest := t.TempDir()

	got, err := Fetch(context.Background(), Options{
		Model:    "minishlab/potion-code-16M",
		Dest:     dest,
		BaseURL:  srv.URL,
		Progress: nopWriter{},
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != 3 {
		t.Errorf("downloaded count: got %d, want 3", got)
	}
	for _, name := range modelFiles {
		path := filepath.Join(dest, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s: empty after Fetch", name)
		}
	}
}

func TestFetch_SkipsAlreadyPresentFiles(t *testing.T) {
	// Bodies must clear minPlausibleSize so the second pass recognizes them
	// as real artifacts (not stubs) and skips.
	srv := fakeHF(t, map[string][]byte{
		"model.safetensors": bytes.Repeat([]byte{0xab}, 1<<20), // ≥1 MiB floor
		"tokenizer.json":    bytes.Repeat([]byte("x"), 8<<10),  // ≥4 KiB floor
		"config.json":       bytes.Repeat([]byte("y"), 1024),   // ≥512 B floor
	})
	dest := t.TempDir()

	// First Fetch: downloads all 3.
	first, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err != nil || first != 3 {
		t.Fatalf("first Fetch: count=%d err=%v", first, err)
	}

	// Second Fetch (no Force): nothing downloaded.
	second, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second != 0 {
		t.Errorf("expected 0 re-downloads on the second pass, got %d", second)
	}
}

// TestFetch_ReplacesStubFiles is the regression guard for the
// existence-only "already present" bug: leftover Git-LFS / HF-hub pointer
// stubs (or broken-symlink / truncated files) used to be reported as
// present, silently leaving a model that fails to load. A present-but-
// too-small file must now be re-downloaded.
func TestFetch_ReplacesStubFiles(t *testing.T) {
	real := map[string][]byte{
		"model.safetensors": bytes.Repeat([]byte{0xab}, 1<<20),
		"tokenizer.json":    bytes.Repeat([]byte("x"), 8<<10),
		"config.json":       bytes.Repeat([]byte("y"), 1024),
	}
	srv := fakeHF(t, real)
	dest := t.TempDir()

	// Pre-seed each path with a sub-floor "pointer" stub, the shape the
	// existence-only check used to accept.
	stub := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:deadbeef\nsize 547000000\n")
	for name := range real {
		if err := os.WriteFile(filepath.Join(dest, name), stub, 0o644); err != nil {
			t.Fatalf("seed stub %s: %v", name, err)
		}
	}

	got, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != 3 {
		t.Errorf("expected all 3 stubs replaced, got %d re-downloads", got)
	}
	// Each file is now the real artifact, not the stub.
	for name, want := range real {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if info.Size() != int64(len(want)) {
			t.Errorf("%s: size %d after Fetch, want real %d (stub not replaced)", name, info.Size(), len(want))
		}
	}
}

func TestFetch_ForceReDownloads(t *testing.T) {
	srv := fakeHF(t, map[string][]byte{
		"model.safetensors": []byte("v1"),
		"tokenizer.json":    []byte("v1"),
		"config.json":       []byte("v1"),
	})
	dest := t.TempDir()

	if _, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	}); err != nil {
		t.Fatalf("seed Fetch: %v", err)
	}
	// Confirm files were laid down.
	for _, name := range modelFiles {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("seed %s missing: %v", name, err)
		}
	}

	got, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Force: true, Progress: nopWriter{},
	})
	if err != nil {
		t.Fatalf("forced Fetch: %v", err)
	}
	if got != 3 {
		t.Errorf("--force should re-download all 3 files, got %d", got)
	}
}

func TestFetch_404LeavesNoPartialFiles(t *testing.T) {
	// model.safetensors is present; tokenizer.json is not. Fetch should
	// download safetensors successfully, then fail on tokenizer, and the
	// dest dir should contain *only* model.safetensors with no .tmp
	// residue from the failed tokenizer attempt.
	srv := fakeHF(t, map[string][]byte{
		"model.safetensors": []byte("safetensors-bytes"),
		// tokenizer.json deliberately missing → 404.
		"config.json": []byte("config-bytes"),
	})
	dest := t.TempDir()

	_, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err == nil {
		t.Fatal("expected an error when tokenizer.json 404s; got nil")
	}
	if !strings.Contains(err.Error(), "tokenizer.json") {
		t.Errorf("error doesn't mention tokenizer.json: %v", err)
	}

	// model.safetensors downloaded before the failure, so it should be
	// the only file present. Critically: no tokenizer.json.tmp.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	if !have["model.safetensors"] {
		t.Error("expected model.safetensors (downloaded before failure)")
	}
	for _, residue := range []string{"tokenizer.json", "tokenizer.json.tmp", "config.json"} {
		if have[residue] {
			t.Errorf("dest still contains %s after failed Fetch", residue)
		}
	}
}

func TestFetch_RejectsEmptyBody(t *testing.T) {
	// HF's CDN has been observed to serve 200/empty for certain cached
	// redirects. The downloader has to reject these explicitly; otherwise
	// the inference code crashes later in a less obvious way.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), Options{
		Model: "x/y", Dest: t.TempDir(), BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err == nil {
		t.Fatal("expected an error on empty 200 body; got nil")
	}
	if !strings.Contains(err.Error(), "empty body") {
		t.Errorf("error doesn't mention 'empty body': %v", err)
	}
}

func TestFetch_ContextCancelAborts(t *testing.T) {
	// Pre-cancel a context, then call Fetch. The first HTTP request
	// returns ctx.Err()-wrapped; no files written. Catches the case
	// where a Ctrl-C mid-download leaves residue.
	srv := fakeHF(t, map[string][]byte{
		"model.safetensors": []byte("anything"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := t.TempDir()
	_, err := Fetch(ctx, Options{
		Model: "x/y", Dest: dest, BaseURL: srv.URL, Progress: nopWriter{},
	})
	if err == nil {
		t.Fatal("expected an error from a pre-cancelled context; got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in error chain, got %v", err)
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		t.Errorf("unexpected file in dest after cancelled Fetch: %s", e.Name())
	}
}

// nopWriter discards the progress stream so test output stays readable.
type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
