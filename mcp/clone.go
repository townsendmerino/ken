package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
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
// SSRF guard (two layers): (1) a pre-flight check resolves the URL's host
// and rejects if any A/AAAA record is loopback / link-local / RFC1918 /
// RFC4193 / unspecified — a fast, clear rejection of the obvious shape (a
// metadata or internal endpoint by literal IP or hostname). (2) go-git
// dials through guardedCloneDial, which RE-validates the IP at connect time
// and dials it literally — so a DNS-rebinding TOCTOU (the host re-resolving
// to a private IP between the pre-flight check and go-git's own lookup) and
// redirects to internal hosts are both blocked, not just the pre-flight
// shape. Operators with a legitimate internal git host opt out via the
// documented env var (both layers honor it).
//
// Byte cap: guardedCloneDial also wraps each connection in a per-clone byte
// budget (KEN_MAX_CLONE_BYTES, default 2 GiB), so a hostile host can't
// stream an unbounded / pathological pack — the clone aborts with
// ErrCloneTooLarge and the partial dir is cleaned up. The MCP request ctx
// still bounds wall-clock time.
//
// Stability: best-effort (NOT part of the 1.0 hard-committed
// surface). The function signature is stable; the temp-dir naming
// scheme + the SSRF guard's allow-list may evolve. External
// consumers writing custom Builders can use it; just expect minor
// semantic shifts between releases.
func CloneShallow(ctx context.Context, urlStr string) (string, func(), error) {
	if !privateCloneAllowed() {
		if err := guardCloneTarget(urlStr); err != nil {
			return "", nil, err
		}
	}
	// Route go-git's clone through the guarded http client: re-validates
	// the IP at dial time (DNS-rebinding-safe) and caps the pack stream.
	installGuardedCloneClient()

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

// ── clone byte cap (#15) + DNS-rebinding-safe dial (#16) ────────────────

// defaultMaxCloneBytes caps the total bytes a single clone may read over
// the wire (TLS framing + the git pack). A shallow Depth-1 clone of a
// normal repo is a few MB to a few hundred MB; the cap stops a hostile
// server from streaming an unbounded / zip-bomb-style pack (the former L3
// limitation). Override with KEN_MAX_CLONE_BYTES (a byte count; 0 disables).
const defaultMaxCloneBytes int64 = 2 << 30 // 2 GiB

const envMaxCloneBytes = "KEN_MAX_CLONE_BYTES"

// ErrCloneTooLarge wraps into the clone error when a clone stream exceeds
// the byte cap. The partial clone dir is removed by CloneShallow's error path.
var ErrCloneTooLarge = errors.New("mcp: clone exceeded the byte cap (possible unbounded/hostile pack; raise KEN_MAX_CLONE_BYTES if legitimate)")

func maxCloneBytes() int64 {
	if v := os.Getenv(envMaxCloneBytes); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMaxCloneBytes
}

// cappedConn wraps a net.Conn and fails reads once the per-connection byte
// budget is exhausted. unlimited (cap ≤ 0) passes through untouched.
type cappedConn struct {
	net.Conn
	remaining int64
	unlimited bool
}

func (c *cappedConn) Read(p []byte) (int, error) {
	if c.unlimited {
		return c.Conn.Read(p)
	}
	if c.remaining <= 0 {
		return 0, ErrCloneTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// guardedCloneDial is the DialContext go-git's clones use. It re-validates
// the resolved IP at CONNECT time — closing the DNS-rebinding TOCTOU the
// pre-flight guardCloneTarget can't (the OS resolver runs again here, and
// HTTP redirects each get their own dial) — by resolving the host, dialing
// only a non-private IP literally (so TLS still verifies the real hostname
// via SNI), and wrapping the connection in the byte cap.
func guardedCloneDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	allowPrivate := privateCloneAllowed()
	var dialIP net.IP
	for _, ipa := range ips {
		if allowPrivate || !isPrivateAddr(ipa.IP) {
			dialIP = ipa.IP
			break
		}
	}
	if dialIP == nil {
		return nil, fmt.Errorf("%w: host=%s resolved only to private/loopback addresses (set %s=1 to override)",
			ErrPrivateCloneTarget, host, envAllowPrivateClone)
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
	if err != nil {
		return nil, err
	}
	limit := maxCloneBytes()
	return &cappedConn{Conn: conn, remaining: limit, unlimited: limit <= 0}, nil
}

var installGuardedCloneClientOnce sync.Once

// installGuardedCloneClient registers (process-once) a go-git http(s)
// client that dials through guardedCloneDial. Global to go-git's protocol
// registry, but ken-mcp's only go-git network use is CloneShallow, so it
// scopes to clones. Keep-alive is disabled so each clone gets a fresh byte
// budget (no pooled-conn carry-over between clones).
func installGuardedCloneClient() {
	installGuardedCloneClientOnce.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = guardedCloneDial
		t.DisableKeepAlives = true
		c := &http.Client{Transport: t} // no Client.Timeout — the MCP ctx bounds the clone
		gc := githttp.NewClient(c)
		gitclient.InstallProtocol("https", gc)
		gitclient.InstallProtocol("http", gc)
	})
}
