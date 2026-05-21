// Package modelfetch downloads the three files ken needs to run hybrid
// or semantic mode (model.safetensors, tokenizer.json, config.json)
// directly from HuggingFace's CDN, no Python tooling required. The whole
// point is to close the gap between ken's "single static binary" claim
// and the previous Quickstart's huggingface-cli dependency.
//
// Scope is intentionally minimal:
//   - Public models only (no HF auth flow). Gated/private models still
//     need huggingface-cli.
//   - No checksum verification at download. The embedding parity test
//     (internal/embed/parity_test.go) catches a corrupt model at first
//     inference; rebuilding HF's checksum subsystem here would duplicate
//     that with weaker guarantees.
//   - One model per destination dir. No registry, no version pinning
//     UX, no switching between models.
//
// The default model + destination match what ken's bench harnesses
// already expect, so a fresh user running `ken download-model` followed
// by `ken search ... --mode hybrid` works without further configuration.
package modelfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultModel is the Model2Vec snapshot ken's docs reference and tests
// validate against. minishlab/potion-code-16M ships three files;
// changing the default would mean re-running the golden fixture.
const DefaultModel = "minishlab/potion-code-16M"

// DefaultBaseURL is HuggingFace's public CDN. Overridable via Options
// for testing; production callers should leave it empty.
const DefaultBaseURL = "https://huggingface.co"

// modelFiles enumerates the three artifacts ken needs to run hybrid /
// semantic mode. The order is deliberate: model.safetensors is largest,
// so a user staring at the progress line sees the long-running file
// first and short ones at the end.
var modelFiles = []string{"model.safetensors", "tokenizer.json", "config.json"}

// Options configures Fetch. Zero-value Options is invalid — Model and
// Dest must be non-empty. Helper DefaultDest() resolves "~/.ken/model"
// for callers that want the standard location.
type Options struct {
	Model    string       // "<org>/<name>", e.g. "minishlab/potion-code-16M"
	Dest     string       // absolute path to the model directory
	Force    bool         // re-download files even if already present
	Progress io.Writer    // status lines go here; os.Stderr if nil
	BaseURL  string       // root URL, defaults to DefaultBaseURL (tests inject)
	Client   *http.Client // HTTP client, defaults to a 60-second-timeout client
}

// DefaultDest returns the conventional location ($HOME/.ken/model) the
// rest of ken's tooling expects when no explicit --model flag is given.
// Returns an error only if HOME is unset; callers can fall back to a
// repo-local path in that case.
func DefaultDest() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving $HOME: %w", err)
	}
	return filepath.Join(home, ".ken", "model"), nil
}

// Fetch downloads the three model files into opts.Dest, skipping files
// already present unless opts.Force. Stream-writes via a .tmp file +
// atomic rename so a cancelled or failed download leaves no
// half-finished artifacts that a future inference would misread.
//
// Returns the count of files newly downloaded (skips don't count).
// Context cancellation aborts mid-download cleanly; cleanup of .tmp
// files is best-effort.
func Fetch(ctx context.Context, opts Options) (int, error) {
	if opts.Model == "" {
		return 0, errors.New("modelfetch: Options.Model is required")
	}
	if opts.Dest == "" {
		return 0, errors.New("modelfetch: Options.Dest is required")
	}
	if opts.Progress == nil {
		opts.Progress = os.Stderr
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 5 * time.Minute}
	}

	if err := os.MkdirAll(opts.Dest, 0o755); err != nil {
		return 0, fmt.Errorf("creating %s: %w", opts.Dest, err)
	}

	fmt.Fprintf(opts.Progress, "ken: downloading %s → %s\n", opts.Model, opts.Dest)

	downloaded := 0
	for _, name := range modelFiles {
		target := filepath.Join(opts.Dest, name)
		if !opts.Force {
			if _, err := os.Stat(target); err == nil {
				fmt.Fprintf(opts.Progress, "  ✓ %s (already present; --force to re-download)\n", name)
				continue
			}
		}
		if err := fetchOne(ctx, opts, name, target); err != nil {
			return downloaded, err
		}
		downloaded++
	}

	if downloaded == 0 {
		fmt.Fprintln(opts.Progress, "ken: all 3 files already present")
	} else {
		fmt.Fprintf(opts.Progress, "ken: downloaded %d/%d files; ready for --mode hybrid\n", downloaded, len(modelFiles))
	}
	return downloaded, nil
}

// fetchOne pulls a single file from HF's `resolve/main` endpoint into
// target+".tmp", then atomically renames to target. HF's URL convention:
// https://huggingface.co/<org>/<name>/resolve/main/<filename>.
func fetchOne(ctx context.Context, opts Options, filename, target string) error {
	url := strings.TrimRight(opts.BaseURL, "/") + "/" + opts.Model + "/resolve/main/" + filename

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%s: building request: %w", filename, err)
	}
	// User-Agent identifies ken in HF's logs; helps if they ever need to
	// reach out about traffic patterns.
	req.Header.Set("User-Agent", "ken-modelfetch (https://github.com/townsendmerino/ken)")

	resp, err := opts.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: GET %s: %w", filename, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Truncate body so a 400 KB HTML error page doesn't flood the
		// terminal; the first 512 bytes are usually enough to identify
		// 404 vs 403 vs rate-limit-throttled.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s returned %d: %s", filename, url, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	tmp := target + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("%s: creating %s: %w", filename, tmp, err)
	}

	// Wrap the body in a progress writer so users see throughput on the
	// 64 MB safetensors download. On a TTY the line refreshes in place;
	// on non-TTY each update is a discrete line. Either way the writer
	// terminates the line itself, so no trailing Fprintln here.
	pw := newProgressWriter(opts.Progress, filename, resp.ContentLength)
	written, err := io.Copy(f, io.TeeReader(resp.Body, pw))
	closeErr := f.Close()
	pw.finish()

	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: writing %s: %w", filename, tmp, err)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: closing %s: %w", filename, tmp, closeErr)
	}

	// HuggingFace 200s with empty bodies are a known failure mode (their
	// CDN occasionally serves zero-byte responses for cached redirects);
	// the inference code would crash later in a less obvious way.
	if written == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: HF returned 200 with empty body (transient; retry)", filename)
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: rename %s → %s: %w", filename, tmp, target, err)
	}
	return nil
}

// progressWriter writes a "↓ <file> X.Y / Z.Y MB" status (or just
// "↓ <file> X.Y MB" when Content-Length is missing) at ~16 MB intervals
// as bytes flow past. On a TTY it refreshes one line in place via `\r`;
// on non-TTY consumers (pipelines, CI logs) it emits discrete lines so
// `tee` and `grep` stay readable.
type progressWriter struct {
	out           io.Writer
	filename      string
	contentLength int64
	bytesWritten  int64
	lastReported  int64
	isTTY         bool
}

// newProgressWriter type-asserts out to *os.File and checks Mode's
// char-device bit. Anything else (bytes.Buffer in tests, *log.Logger,
// captured-stderr pipes) falls into the safer line-per-update mode.
func newProgressWriter(out io.Writer, filename string, contentLength int64) *progressWriter {
	isTTY := false
	if f, ok := out.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			isTTY = true
		}
	}
	return &progressWriter{
		out:           out,
		filename:      filename,
		contentLength: contentLength,
		isTTY:         isTTY,
	}
}

func (p *progressWriter) Write(buf []byte) (int, error) {
	n := len(buf)
	p.bytesWritten += int64(n)
	// Skip progress entirely for sub-MB files — tokenizer.json and
	// config.json finish before any reporter could meaningfully update,
	// and rounding "97 bytes" to "0.0 MB" looks broken.
	if p.contentLength > 0 && p.contentLength < 1<<20 {
		return n, nil
	}
	// Report every ~16 MB: for the 64 MB safetensors that's 4 updates,
	// which is enough "you can see progress" signal without spamming
	// non-TTY consumers.
	finalReport := p.contentLength > 0 && p.bytesWritten >= p.contentLength
	if p.bytesWritten-p.lastReported < 16<<20 && !finalReport {
		return n, nil
	}
	p.lastReported = p.bytesWritten

	var body string
	if p.contentLength > 0 {
		body = fmt.Sprintf("  ↓ %s %.1f / %.1f MB", p.filename, float64(p.bytesWritten)/(1<<20), float64(p.contentLength)/(1<<20))
	} else {
		body = fmt.Sprintf("  ↓ %s %.1f MB", p.filename, float64(p.bytesWritten)/(1<<20))
	}
	if p.isTTY {
		// Refresh the same line in place; finish() writes the trailing
		// newline so the cursor doesn't dangle.
		fmt.Fprint(p.out, "\r"+body)
	} else {
		fmt.Fprintln(p.out, body)
	}
	return n, nil
}

// finish terminates the in-progress line. On TTY mode the refreshing
// progress line lacks a trailing newline by design (so each update can
// overwrite it via \r); finish closes that line off. On non-TTY mode
// every report already self-terminated, so finish is a no-op.
func (p *progressWriter) finish() {
	if p.isTTY {
		fmt.Fprintln(p.out)
	}
}
