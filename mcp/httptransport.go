package mcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

// Remote transport (ADR-041). ken-mcp's default transport is stdio, where the
// OS-user boundary is the auth boundary. The optional Streamable HTTP transport
// exposes the same tool surface over the network for centralized / staging /
// team-shared deployments — which fundamentally changes the security model:
// anyone reachable on the network can issue MCP calls. This file is the
// security envelope every HTTP request passes through:
//
//   - Bearer-token authentication (constant-time, length-leak-free). HTTP mode
//     refuses to start without a token — no insecure-by-default (see Validate).
//   - Per-client-IP token-bucket rate limiting, outermost so an unauthenticated
//     flood can't turn the auth check itself into a DoS vector.
//
// TLS is intentionally out of scope: the deployment contract is bearer-over-TLS
// terminated by a reverse proxy in front of ken-mcp (ADR-041). Behind a proxy,
// r.RemoteAddr is the proxy's address, so per-IP limiting degrades to a global
// cap — a fine DoS backstop; X-Forwarded-For is deliberately NOT trusted
// (spoofable).

// ErrHTTPNoAuthToken is returned by HTTPConfig.Validate when the HTTP transport
// is requested with no bearer token — the insecure-by-default combination
// ken-mcp refuses to run. cmd/ken-mcp turns this into a loud startup failure.
var ErrHTTPNoAuthToken = errors.New(
	"remote HTTP transport requires an auth token: set KEN_MCP_AUTH_TOKEN " +
		"(or KEN_MCP_AUTH_TOKEN_FILE, preferred — the token is then never in the " +
		"environment) to a strong secret, or use the default KEN_MCP_TRANSPORT=stdio. " +
		"Refusing to serve MCP over the network without authentication")

// HTTPConfig configures the remote Streamable HTTP transport. Every field is
// populated from KEN_MCP_* env vars in cmd/ken-mcp.
type HTTPConfig struct {
	// Addr is the bind address, e.g. ":8080".
	Addr string
	// AuthToken is the required bearer token. An empty token is rejected by
	// Validate — HTTP mode never runs unauthenticated.
	AuthToken string
	// RateLimitPerMin caps requests per client IP per minute; 0 disables rate
	// limiting.
	RateLimitPerMin int
}

// Validate reports whether the HTTP config is safe to serve. It returns
// ErrHTTPNoAuthToken when no bearer token is set — the one hard failure that
// keeps ken-mcp from ever exposing an unauthenticated MCP endpoint.
func (c HTTPConfig) Validate() error {
	if strings.TrimSpace(c.AuthToken) == "" {
		return ErrHTTPNoAuthToken
	}
	return nil
}

// NewHTTPHandler wraps one shared MCP server in the SDK's Streamable HTTP
// handler, then in ken's security middleware (auth inner, rate-limit outer).
// The single *sdk.Server is returned for every request, so all connections
// share the same process state (the ADR-013 LRU repo cache + Refresher) — the
// transport is just a different I/O channel, it does not fork state.
//
// Callers must have run cfg.Validate() first; NewHTTPHandler assumes a token is
// present and does not re-check (it would otherwise silently build an
// unauthenticated handler).
func NewHTTPHandler(srv *sdk.Server, cfg HTTPConfig) http.Handler {
	getServer := func(*http.Request) *sdk.Server { return srv }
	// Stateless transport (the sessionless direction of the MCP spec, SEP-2567):
	// each request is an independent temporary session — no per-session server
	// goroutine to leak, and no session affinity needed behind a load balancer,
	// which suits a centralized/team-shared deployment. ken's tools are
	// request/response; the only server→client traffic is the in-request progress
	// heartbeat, which still reaches the client because it's emitted inside an
	// incoming request's context.
	var h http.Handler = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{Stateless: true})
	h = bearerAuthMiddleware(cfg.AuthToken, h)
	if cfg.RateLimitPerMin > 0 {
		h = rateLimitMiddleware(cfg.RateLimitPerMin, h)
	}
	return h
}

// bearerAuthMiddleware rejects any request whose Authorization header is not
// exactly "Bearer <token>". The comparison is over SHA-256 digests so it is
// constant-time AND leaks neither the token length nor a byte-position prefix
// through timing.
func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	wantSum := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSum := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ipRateLimiter holds one token-bucket limiter per client IP.
type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	perSec   rate.Limit
	burst    int
	maxKeys  int
}

func newIPRateLimiter(perMin int) *ipRateLimiter {
	burst := max(perMin, 1)
	return &ipRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		perSec:   rate.Limit(float64(perMin) / 60.0),
		burst:    burst,
		// Bound memory against a spray of distinct source IPs. On overflow the
		// whole map is dropped (everyone gets a fresh bucket); a brief allowance
		// bump is an acceptable price for a hard memory ceiling.
		maxKeys: 10000,
	}
}

func (l *ipRateLimiter) limiterFor(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.limiters) > l.maxKeys {
		l.limiters = make(map[string]*rate.Limiter)
	}
	lim, ok := l.limiters[ip]
	if !ok {
		lim = rate.NewLimiter(l.perSec, l.burst)
		l.limiters[ip] = lim
	}
	return lim
}

// rateLimitMiddleware caps requests per client IP. It sits outermost so an
// unauthenticated flood is throttled before it reaches (and can DoS) the auth
// comparison.
func rateLimitMiddleware(perMin int, next http.Handler) http.Handler {
	rl := newIPRateLimiter(perMin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.limiterFor(clientIP(r)).Allow() {
			w.Header().Set("Retry-After", "60")
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the request's source IP (host portion of RemoteAddr). Behind a
// reverse proxy this is the proxy's IP; X-Forwarded-For is intentionally not
// consulted (a client could spoof it to evade the limit).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
