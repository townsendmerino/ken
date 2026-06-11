package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// readOnlyConn is a minimal net.Conn whose Read serves from r; the other
// net.Conn methods are unused by cappedConn so the embedded nil is fine.
type readOnlyConn struct {
	net.Conn
	r io.Reader
}

func (c readOnlyConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// TestCappedConn_AbortsOverCap: a stream larger than the byte budget reads
// up to the cap then fails with ErrCloneTooLarge (the #15 pack-size guard).
func TestCappedConn_AbortsOverCap(t *testing.T) {
	src := readOnlyConn{r: bytes.NewReader(make([]byte, 100))}
	c := &cappedConn{Conn: src, remaining: 50}
	buf := make([]byte, 256)
	total := 0
	var last error
	for {
		n, err := c.Read(buf)
		total += n
		if err != nil {
			last = err
			break
		}
	}
	if !errors.Is(last, ErrCloneTooLarge) {
		t.Fatalf("expected ErrCloneTooLarge, got %v (read %d)", last, total)
	}
	if total > 50 {
		t.Errorf("read %d bytes past the 50-byte cap", total)
	}
}

// TestCappedConn_UnderCap: a stream within budget reads fully and ends in
// EOF, never ErrCloneTooLarge.
func TestCappedConn_UnderCap(t *testing.T) {
	src := readOnlyConn{r: bytes.NewReader(make([]byte, 100))}
	c := &cappedConn{Conn: src, remaining: 200}
	buf := make([]byte, 256)
	total := 0
	for {
		n, err := c.Read(buf)
		total += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error under cap: %v", err)
		}
	}
	if total != 100 {
		t.Errorf("read %d bytes, want 100", total)
	}
}

// TestGuardedCloneDial_RejectsPrivate is the #16 dial-time SSRF guard: the
// connection-time re-validation rejects a private/loopback target even
// though no pre-flight check runs here. (Literal IP → resolver short-circuit,
// so no network.)
func TestGuardedCloneDial_RejectsPrivate(t *testing.T) {
	t.Setenv(envAllowPrivateClone, "") // ensure guard is on
	for _, addr := range []string{"127.0.0.1:443", "169.254.169.254:80", "10.0.0.5:443"} {
		_, err := guardedCloneDial(context.Background(), "tcp", addr)
		if !errors.Is(err, ErrPrivateCloneTarget) {
			t.Errorf("guardedCloneDial(%q) = %v, want ErrPrivateCloneTarget", addr, err)
		}
	}
}

func TestMaxCloneBytes(t *testing.T) {
	t.Setenv(envMaxCloneBytes, "1048576")
	if got := maxCloneBytes(); got != 1048576 {
		t.Errorf("explicit: got %d want 1048576", got)
	}
	t.Setenv(envMaxCloneBytes, "0") // 0 = disable cap
	if got := maxCloneBytes(); got != 0 {
		t.Errorf("disable: got %d want 0", got)
	}
	t.Setenv(envMaxCloneBytes, "garbage") // bad value → default
	if got := maxCloneBytes(); got != defaultMaxCloneBytes {
		t.Errorf("bad value: got %d want default %d", got, defaultMaxCloneBytes)
	}
}

// TestIsPrivateAddr pins the address-family classification used by
// the M2 SSRF guard. Failure here means the underlying net.IP
// predicates moved and the guard's coverage shifted with them.
func TestIsPrivateAddr(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
		why  string
	}{
		{"127.0.0.1", true, "loopback v4"},
		{"::1", true, "loopback v6"},
		{"169.254.169.254", true, "EC2/GCP metadata link-local"},
		{"fe80::1", true, "link-local v6"},
		{"10.0.0.5", true, "RFC1918 10/8"},
		{"172.16.0.1", true, "RFC1918 172.16/12"},
		{"192.168.1.1", true, "RFC1918 192.168/16"},
		{"fc00::1", true, "RFC4193 ULA"},
		{"0.0.0.0", true, "unspecified v4"},
		{"::", true, "unspecified v6"},
		{"8.8.8.8", false, "public v4 (Google DNS)"},
		{"1.1.1.1", false, "public v4 (Cloudflare DNS)"},
		{"2001:4860:4860::8888", false, "public v6 (Google DNS)"},
		{"140.82.114.4", false, "public v4 (github.com sample)"},
	}
	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("invalid test fixture IP %q", tt.ip)
		}
		got := isPrivateAddr(ip)
		if got != tt.want {
			t.Errorf("isPrivateAddr(%s) = %v, want %v (%s)", tt.ip, got, tt.want, tt.why)
		}
	}
}

// TestGuardCloneTarget_LiteralIPs confirms that the guard fast-paths
// IP-literal URLs without a DNS lookup, and produces an error that
// names ErrPrivateCloneTarget so callers can errors.Is-check it.
func TestGuardCloneTarget_LiteralIPs(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1/repo",
		"http://[::1]:8080/repo",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/internal/git",
		"https://192.168.1.1/local",
	} {
		err := guardCloneTarget(u)
		if !errors.Is(err, ErrPrivateCloneTarget) {
			t.Errorf("guardCloneTarget(%q) = %v, want ErrPrivateCloneTarget", u, err)
		}
		if err != nil && !strings.Contains(err.Error(), "KEN_ALLOW_PRIVATE_CLONE_TARGETS") {
			t.Errorf("error message should name the opt-out env var, got: %v", err)
		}
	}
}

// TestGuardCloneTarget_PublicLiteralIPs confirms the guard does NOT
// fire on public IPs (literal form).
func TestGuardCloneTarget_PublicLiteralIPs(t *testing.T) {
	for _, u := range []string{
		"http://8.8.8.8/dns",
		"https://1.1.1.1/dns",
	} {
		if err := guardCloneTarget(u); err != nil {
			t.Errorf("guardCloneTarget(%q) = %v, want nil (public IP)", u, err)
		}
	}
}

// TestGuardCloneTarget_EmptyHost rejects URLs with no host (which
// can't be cloned anyway).
func TestGuardCloneTarget_EmptyHost(t *testing.T) {
	for _, u := range []string{
		"http:///path",
		"https://",
	} {
		err := guardCloneTarget(u)
		if err == nil {
			t.Errorf("guardCloneTarget(%q) = nil, want error", u)
		}
	}
}

// TestPrivateCloneAllowed pins the opt-out env-var truthy-parse.
func TestPrivateCloneAllowed(t *testing.T) {
	t.Setenv(envAllowPrivateClone, "")
	if privateCloneAllowed() {
		t.Error("empty env should not allow")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", "Yes"} {
		t.Setenv(envAllowPrivateClone, v)
		if !privateCloneAllowed() {
			t.Errorf("env=%q should allow", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "random"} {
		t.Setenv(envAllowPrivateClone, v)
		if privateCloneAllowed() {
			t.Errorf("env=%q should not allow", v)
		}
	}
}
