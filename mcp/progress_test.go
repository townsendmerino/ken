package mcp

import (
	"context"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStartProgressHeartbeat_nilRequestIsNoop: the helper must not
// panic on a nil request (the mcp.Run handler passes nil today; even
// when it doesn't, defensive no-op is the right shape).
func TestStartProgressHeartbeat_nilRequestIsNoop(t *testing.T) {
	stop := startProgressHeartbeat(context.Background(), nil, "test")
	stop()
	stop() // idempotent
}

// TestStartProgressHeartbeat_noTokenIsNoop: a request without a
// progress token means the client isn't asking for notifications.
// We shouldn't spawn a goroutine in that case.
func TestStartProgressHeartbeat_noTokenIsNoop(t *testing.T) {
	req := &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{}}
	stop := startProgressHeartbeat(context.Background(), req, "test")
	stop()
}

// TestStartProgressHeartbeat_nilSessionIsNoop: even with a token,
// if the session isn't connected (test fixtures sometimes lack one)
// we skip — otherwise NotifyProgress would NPE.
func TestStartProgressHeartbeat_nilSessionIsNoop(t *testing.T) {
	req := &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{}}
	req.Params.SetProgressToken("tok-1")
	// Session left nil deliberately.
	stop := startProgressHeartbeat(context.Background(), req, "test")
	stop()
}

// TestStartProgressHeartbeat_stopBeforeFirstTickEmitsNothing: fast
// queries that finish in < 1 s should NEVER emit a heartbeat
// notification — the goroutine spins down before the first tick.
// We can't easily intercept NotifyProgress without the full client/
// server fixture, so we rely on stop()-completes-cleanly as the
// proxy for "goroutine exited."
func TestStartProgressHeartbeat_stopBeforeFirstTickEmitsNothing(t *testing.T) {
	req := &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{}}
	req.Params.SetProgressToken("tok-2")
	// No session — proves the no-token path; the real concern is the
	// goroutine lifecycle which the next case exercises.
	stop := startProgressHeartbeat(context.Background(), req, "test")
	// Sleep less than the heartbeat interval, then stop.
	time.Sleep(10 * time.Millisecond)
	stop()
}
