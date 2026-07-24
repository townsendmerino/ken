package modelfetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEtagSHA256(t *testing.T) {
	sha := strings.Repeat("a", 64)
	cases := map[string]string{
		`"` + sha + `"`:                     sha, // quoted sha256 (HF LFS form)
		`W/"` + sha + `"`:                   sha, // weak validator
		sha:                                 sha, // bare
		`"` + strings.Repeat("b", 40) + `"`: "",  // git-blob sha1 (40 hex) → ignore
		`"deadbeef"`:                        "",  // too short
		`W/"weak-etag"`:                     "",  // not hex
		"":                                  "",  // absent
		`"` + strings.Repeat("g", 64) + `"`: "",  // 64 chars but non-hex
	}
	for in, want := range cases {
		if got := etagSHA256(in); got != want {
			t.Errorf("etagSHA256(%q) = %q, want %q", in, got, want)
		}
	}
}

// serveWithHeaders is an HF-shaped test server that serves each file's bytes
// with Content-Length plus whatever per-file headers hdr sets (ETag /
// X-Linked-Size), for the integrity tests.
func serveWithHeaders(t *testing.T, files map[string][]byte, hdr func(name string, w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if hdr != nil {
			hdr(parts[4], w)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func integrityFiles() map[string][]byte {
	return map[string][]byte{
		"model.safetensors": bytes.Repeat([]byte{0x01}, 2<<20), // ≥ minPlausibleSize
		"tokenizer.json":    []byte(`{"version":"1.0"}`),
		"config.json":       []byte(`{"hidden_size":256}`),
	}
}

func TestFetch_ChecksumMatchAccepted(t *testing.T) {
	files := integrityFiles()
	url := serveWithHeaders(t, files, func(name string, w http.ResponseWriter) {
		sum := sha256.Sum256(files[name])
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	})
	dest := t.TempDir()
	if _, err := Fetch(context.Background(), Options{Model: "x/y", Dest: dest, BaseURL: url, Progress: nopWriter{}}); err != nil {
		t.Fatalf("Fetch with correct ETag checksums should succeed: %v", err)
	}
}

func TestFetch_ChecksumMismatchRejected(t *testing.T) {
	files := integrityFiles()
	wrong := strings.Repeat("d", 64) // valid-looking sha256 that won't match the body
	url := serveWithHeaders(t, files, func(name string, w http.ResponseWriter) {
		if name == "model.safetensors" {
			w.Header().Set("ETag", `"`+wrong+`"`)
		}
	})
	dest := t.TempDir()
	_, err := Fetch(context.Background(), Options{Model: "x/y", Dest: dest, BaseURL: url, Progress: nopWriter{}})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Fetch err = %v, want a checksum mismatch error", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "model.safetensors")); !os.IsNotExist(statErr) {
		t.Error("a checksum-mismatched model.safetensors must not be left on disk")
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after rejected download: %s", e.Name())
		}
	}
}

func TestFetch_XLinkedSizeMismatchRejected(t *testing.T) {
	files := integrityFiles()
	url := serveWithHeaders(t, files, func(name string, w http.ResponseWriter) {
		if name == "model.safetensors" {
			w.Header().Set("X-Linked-Size", "99999999") // wrong LFS object size
		}
	})
	dest := t.TempDir()
	_, err := Fetch(context.Background(), Options{Model: "x/y", Dest: dest, BaseURL: url, Progress: nopWriter{}})
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("Fetch err = %v, want a size mismatch error", err)
	}
}
