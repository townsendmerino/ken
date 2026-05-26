package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
)

// ErrPrivateCloneTarget is returned by CloneShallow when the supplied
// http(s) URL resolves to a loopback, link-local, RFC1918, or RFC4193
// address. M2 SSRF guard: a hostile agent passing
// `repo="http://169.254.169.254/..."` or `repo="http://127.0.0.1:..."`
// would otherwise coax ken-mcp into issuing outbound HTTP requests
// from the host's network position. Operators with a legitimate
// internal git host can opt out via
// `KEN_ALLOW_PRIVATE_CLONE_TARGETS=1`.
var ErrPrivateCloneTarget = errors.New("mcp: clone target resolves to a private/loopback/link-local address")

// envAllowPrivateClone is the opt-out for the SSRF guard. Set to "1",
// "true", or "yes" to disable. Default off — the agent-supplied-URL
// threat model is the primary risk shape.
const envAllowPrivateClone = "KEN_ALLOW_PRIVATE_CLONE_TARGETS"

// CloneShallow shallow-clones a public http(s) repository into a
// deterministic per-URL subdirectory of $TMPDIR/ken-mcp/. Returns the
// clone directory and a cleanup that rm -rf's it. No authentication is
// configured — private repos are out of scope for v1 (semble matches).
//
// Determinism is by sha256(url) so concurrent calls don't collide, and
// stale dirs from previous processes are detectable and reusable should
// we ever add cross-process caching (we don't yet; Close() always rms).
//
// M2 SSRF guard: before invoking go-git, the URL's host is resolved
// and rejected if any of its A/AAAA records points at a loopback /
// link-local / RFC1918 / RFC4193 / unspecified address. This is a
// pre-flight defense: it doesn't survive a DNS rebinding TOCTOU, but
// it blocks the dominant attack shape (a hostile agent naming a
// metadata or internal endpoint by literal IP or by a hostname that
// resolves to one). Operators with a legitimate internal git host
// can opt out via the documented env var.
//
// L3 (documented limitation): there is no max-bytes / max-objects cap
// on the clone. A malicious host can serve a huge or pathological
// pack file; the only timeout in play is the MCP request ctx. If
// this becomes a real problem in production, the fix is a custom
// http.Transport wrapping the go-git client with a bounded-body
// reader — deferred until a deployment scenario justifies the
// per-platform complexity. Mitigation in the meantime: enforce
// reasonable ctx timeouts at the MCP layer, run ken-mcp in a
// network-bandwidth-limited container if the input source is
// untrusted.
func CloneShallow(ctx context.Context, urlStr string) (string, func(), error) {
	if !privateCloneAllowed() {
		if err := guardCloneTarget(urlStr); err != nil {
			return "", nil, err
		}
	}

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

// guardCloneTarget resolves the URL's host and rejects with
// ErrPrivateCloneTarget if any resolved IP is loopback, link-local,
// RFC1918, RFC4193, or unspecified. Empty / unparseable hosts also
// fail (a clone with no host can't proceed anyway).
func guardCloneTarget(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("mcp: parse clone URL %q: %w", urlStr, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("mcp: clone URL %q has empty host", urlStr)
	}
	// If the host parses as a literal IP, skip DNS and check it
	// directly. Covers the obvious-attack shape ("http://127.0.0.1/...",
	// "http://169.254.169.254/...") without a network round-trip.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateAddr(ip) {
			return fmt.Errorf("%w: %s (host=%s; set %s=1 to override)",
				ErrPrivateCloneTarget, urlStr, host, envAllowPrivateClone)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("mcp: resolve clone host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateAddr(ip) {
			return fmt.Errorf("%w: %s (host=%s, resolved=%s; set %s=1 to override)",
				ErrPrivateCloneTarget, urlStr, host, ip, envAllowPrivateClone)
		}
	}
	return nil
}

// isPrivateAddr reports whether ip is one of the address families
// SSRF guards typically reject. net.IP.IsPrivate covers RFC1918 +
// RFC4193 (since Go 1.17); IsLoopback / IsLinkLocalUnicast /
// IsUnspecified cover the rest.
func isPrivateAddr(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified()
}

// privateCloneAllowed reads the opt-out env var. Truthy values
// ("1", "true", "yes", case-insensitive) disable the guard;
// anything else keeps it on. Matches the existing KEN_DB_LISTEN
// truthy-parse pattern in cmd/ken-mcp/env.go.
func privateCloneAllowed() bool {
	switch v := os.Getenv(envAllowPrivateClone); v {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes":
		return true
	}
	return false
}
