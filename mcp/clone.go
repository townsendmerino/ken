package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
)

// CloneShallow shallow-clones a public http(s) repository into a
// deterministic per-URL subdirectory of $TMPDIR/ken-mcp/. Returns the
// clone directory and a cleanup that rm -rf's it. No authentication is
// configured — private repos are out of scope for v1 (semble matches).
//
// Determinism is by sha256(url) so concurrent calls don't collide, and
// stale dirs from previous processes are detectable and reusable should
// we ever add cross-process caching (we don't yet; Close() always rms).
func CloneShallow(ctx context.Context, urlStr string) (string, func(), error) {
	sum := sha256.Sum256([]byte(urlStr))
	dir := filepath.Join(os.TempDir(), "ken-mcp", hex.EncodeToString(sum[:])[:16])

	// If a previous attempt left a partial directory, clear it; go-git
	// otherwise refuses to clone into a non-empty dir.
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, fmt.Errorf("clone: prepare %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", nil, fmt.Errorf("clone: mkdir parent: %w", err)
	}
	if _, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:          urlStr,
		Depth:        1,
		SingleBranch: true,
		// No Progress writer — anything to stdout would corrupt the MCP
		// JSON-RPC stream (see cmd/ken-mcp/main.go for the contract).
	}); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("clone %s: %w (private repos are not supported in v1)", urlStr, err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}
