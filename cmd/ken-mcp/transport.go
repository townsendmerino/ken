package main

import (
	"os"
	"strings"

	"github.com/townsendmerino/ken/internal/envcfg"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// Transport selection + remote-HTTP security config (ADR-041, #15). ken-mcp
// defaults to stdio (OS-user boundary = auth boundary). KEN_MCP_TRANSPORT=http
// opts into the network-exposed Streamable HTTP transport, which requires an
// auth token and refuses several insecure combinations at startup.

const (
	transportStdio   = "stdio"
	transportHTTP    = "http"
	defaultHTTPAddr  = ":8080"
	defaultRateLimit = 100 // requests per client IP per minute
)

// resolveTransport reads KEN_MCP_TRANSPORT and, for http, the HTTP security
// config. It FAILS LOUD (logs + os.Exit(1)) on any insecure/misconfigured
// combination — no auth token, an unreadable token file, or DB row-sampling
// enabled — so a network-exposed server never starts in an unsafe state. On the
// stdio default it returns ("stdio", zero cfg) and never exits.
func resolveTransport(logger *kenmcp.Logger) (transport string, cfg kenmcp.HTTPConfig) {
	transport = envcfg.EnvEnum("KEN_MCP_TRANSPORT", []string{transportStdio, transportHTTP}, transportStdio, logger)
	if transport != transportHTTP {
		return transportStdio, kenmcp.HTTPConfig{}
	}

	addr := strings.TrimSpace(os.Getenv("KEN_MCP_ADDR"))
	if addr == "" {
		addr = defaultHTTPAddr
	}
	rateLimit := envcfg.EnvInt("KEN_MCP_RATE_LIMIT", defaultRateLimit, logger)
	if rateLimit < 0 {
		logger.Logf(kenmcp.LogWarn, "KEN_MCP_RATE_LIMIT=%d: must be non-negative — using default %d", rateLimit, defaultRateLimit)
		rateLimit = defaultRateLimit
	}
	cfg = kenmcp.HTTPConfig{
		Addr:            addr,
		AuthToken:       resolveAuthToken(logger),
		RateLimitPerMin: rateLimit,
	}

	// PII guard (ADR-017 stance, extended to the network): row-sampling turns a
	// remote server into a network-accessible search index over sampled DB
	// values. Hard-reject rather than ship the foot-gun.
	if sampleRows := envcfg.EnvInt("KEN_DB_SAMPLE_ROWS", 0, logger); sampleRows > 0 {
		logger.Logf(kenmcp.LogError,
			"KEN_DB_SAMPLE_ROWS=%d cannot be used with KEN_MCP_TRANSPORT=http: sampled DB row values "+
				"would be searchable by anyone with the bearer token. Set KEN_DB_SAMPLE_ROWS=0 (schema-only) "+
				"or use KEN_MCP_TRANSPORT=stdio.", sampleRows)
		os.Exit(1)
	}

	// Auth is mandatory for network exposure — no insecure default.
	if err := cfg.Validate(); err != nil {
		logger.Logf(kenmcp.LogError, "%v", err)
		os.Exit(1)
	}
	return transportHTTP, cfg
}

// resolveAuthToken reads the bearer token from KEN_MCP_AUTH_TOKEN_FILE
// (preferred — the secret never enters the process environment) or, failing
// that, KEN_MCP_AUTH_TOKEN. A configured-but-unreadable token file is a fatal
// startup error (fail loud rather than silently fall back to env/no-auth).
func resolveAuthToken(logger *kenmcp.Logger) string {
	if path := strings.TrimSpace(os.Getenv("KEN_MCP_AUTH_TOKEN_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Logf(kenmcp.LogError, "KEN_MCP_AUTH_TOKEN_FILE=%s: cannot read auth token: %v", path, err)
			os.Exit(1)
		}
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(os.Getenv("KEN_MCP_AUTH_TOKEN"))
}
