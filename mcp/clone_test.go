package mcp

import (
	"errors"
	"net"
	"strings"
	"testing"
)

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
