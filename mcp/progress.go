// progress.go — M10 streaming-results helper.
//
// MCP defines `notifications/progress` (server-to-client) for long-
// running tool calls: the client opts in by passing a progressToken
// in the request's `_meta` field; the server may then send periodic
// progress notifications associated with that token. The agent UI
// decides whether to render them; ken-mcp's job is only to emit a
// heartbeat the protocol can carry.
//
// Why a heartbeat (vs richer streaming):
//
//   - MCP's tool-call response is a single message. There's no way to
//     stream PARTIAL results back as the body — the agent always gets
//     the final formatted response in one shot.
//   - notifications/progress is the only between-call channel during a
//     tool invocation. We use it for "still working" status messages
//     that include monotonically-increasing elapsed seconds, so a
//     well-behaved client UI can render a "ken: searching... (4s)"
//     spinner instead of going dark on a cold rerankN=50 call.
//   - Clients that don't render progress notifications (most today)
//     simply ignore them — zero-cost no-op. Forward-compatible.
//
// For a richer streaming experience (preliminary stage-1 results
// printed BEFORE the rerank), see `ken search --stream` in cmd/ken —
// that path bypasses the MCP wire format entirely.

package mcp

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// progressHeartbeatInterval — how often "still working" notifications
// fire. 1 s is short enough for a UI to feel live, long enough that
// even a stdio transport doesn't choke on the small write volume.
// Calls that finish faster than this never emit (the first tick
// hasn't elapsed when the handler returns), keeping fast queries
// noise-free.
const progressHeartbeatInterval = 1 * time.Second

// startProgressHeartbeat spawns a goroutine that emits one
// progress notification per heartbeat tick until the returned stop()
// is called. No-op (returns a no-op stop) if the client didn't pass
// a progressToken — fast queries skip the goroutine entirely.
//
// The notification includes:
//
//	ProgressToken — echoes the client's token so the UI can correlate
//	Message       — fixed prefix + elapsed seconds (e.g. "ken search (4s)")
//	Progress      — elapsed seconds (monotonically increasing as MCP requires)
//	Total         — 0 ("unknown" per the spec; we can't predict rerank latency)
//
// Errors from NotifyProgress are silently swallowed — the heartbeat
// is a UX nicety, not load-bearing for the search itself, and a
// failed write usually means the client disconnected (in which case
// the parent ctx is about to cancel anyway).
func startProgressHeartbeat(ctx context.Context, req *sdk.CallToolRequest, prefix string) func() {
	if req == nil || req.Params == nil {
		return noopStop
	}
	token := req.Params.GetProgressToken()
	if token == nil || req.Session == nil {
		return noopStop
	}
	done := make(chan struct{})
	var stopped atomic.Bool
	start := time.Now()
	go func() {
		ticker := time.NewTicker(progressHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				elapsed := t.Sub(start)
				_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{
					ProgressToken: token,
					Message:       fmt.Sprintf("%s (%ds)", prefix, int(elapsed.Seconds())),
					Progress:      elapsed.Seconds(),
					Total:         0, // unknown — rerank latency varies
				})
			}
		}
	}()
	return func() {
		if stopped.CompareAndSwap(false, true) {
			close(done)
		}
	}
}

func noopStop() {}
