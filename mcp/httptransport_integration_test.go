package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerRoundTripper injects an Authorization header on every request, so the
// SDK's StreamableClientTransport (which has no header hook) can authenticate.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// pingServer is a minimal MCP server with a single "ping" tool, used to exercise
// the HTTP transport + auth middleware end to end without ken's full index/cache
// setup (ken's tools are transport-agnostic — the same *sdk.Server serves both
// stdio and HTTP, and is already covered by the stdio variants).
func pingServer() *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "ping-test", Version: "0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "ping", Description: "returns pong"},
		func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
		})
	return srv
}

func httpTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	h := NewHTTPHandler(pingServer(), HTTPConfig{AuthToken: token})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func newTestClient() *sdk.Client {
	return sdk.NewClient(&sdk.Implementation{Name: "ken-http-test", Version: "0"}, nil)
}

func transportFor(url, token string) *sdk.StreamableClientTransport {
	return &sdk.StreamableClientTransport{
		Endpoint: url,
		// The server is stateless (no standalone SSE stream; GET returns 405),
		// so don't open one from the client.
		DisableStandaloneSSE: true,
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}},
	}
}

// TestHTTPTransport_AuthedSessionWorks drives a real MCP session over the
// Streamable HTTP transport with a valid bearer token: the handshake completes
// and a tool call round-trips. This is the "well-formed MCP JSON-RPC over
// Streamable HTTP framing" guarantee, parallel to the stdio stdout-cleanliness
// variants.
func TestHTTPTransport_AuthedSessionWorks(t *testing.T) {
	ts := httpTestServer(t, "s3cret")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sess, err := newTestClient().Connect(ctx, transportFor(ts.URL, "s3cret"), nil)
	if err != nil {
		t.Fatalf("authed Connect failed: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("CallTool(ping) failed: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("ping returned no content")
	}
	if tc, ok := res.Content[0].(*sdk.TextContent); !ok || tc.Text != "pong" {
		t.Errorf("ping content = %+v, want text 'pong'", res.Content[0])
	}
}

// TestHTTPTransport_RejectsMissingAndBadToken pins that the transport is closed
// without a valid bearer token — the whole point of network auth.
func TestHTTPTransport_RejectsMissingAndBadToken(t *testing.T) {
	ts := httpTestServer(t, "s3cret")

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"wrong token", "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sess, err := newTestClient().Connect(ctx, transportFor(ts.URL, tc.token), nil)
			if err == nil {
				sess.Close()
				t.Fatalf("Connect succeeded with %s; want rejection", tc.name)
			}
		})
	}
}
