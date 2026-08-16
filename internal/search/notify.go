package search

// notify.go — WatchedIndex observability + notifications (split out of watch.go,
// #6): the on-swap / on-flush hooks callers register, the flush-summary
// formatting, and the leveled logger. No corpus or fsnotify logic here.

import (
	"fmt"
	"time"
)

// SetOnSwap installs a channel that receives one nonblocking send
// each time the watcher publishes a new snapshot. Used by tests to
// synchronize on rebuilds. Calling with nil disables. Safe to call
// before NewWatchedIndex returns or between rebuilds.
func (w *WatchedIndex) SetOnSwap(ch chan<- struct{}) {
	w.onSwapMu.Lock()
	defer w.onSwapMu.Unlock()
	w.onSwap = ch
}

// SetOnFlush installs a callback invoked once per snapshot publish
// with a one-line summary like "reindexed: 1234 chunks total,
// 3 files changed in 47 ms". `ken index --watch` uses this to give
// interactive users feedback that the watcher is alive; ken-mcp uses
// it at info-level so reindex activity shows up in --log-level=info
// runs. Pass nil to disable. Safe to call at any time.
func (w *WatchedIndex) SetOnFlush(f func(msg string)) {
	w.onFlushMu.Lock()
	defer w.onFlushMu.Unlock()
	w.onFlush = f
}

// notifyFlush calls the OnFlush callback (if set) with a one-line
// summary of the just-published snapshot. Format is stable enough for
// users to grep but not part of any public contract.
func (w *WatchedIndex) notifyFlush(totalChunks, filesChanged, compacted int, dur time.Duration) {
	w.onFlushMu.Lock()
	f := w.onFlush
	w.onFlushMu.Unlock()
	if f == nil {
		return
	}
	f(formatFlush(totalChunks, filesChanged, compacted, dur))
}

// formatFlush builds the OnFlush message. Pulled out for testability.
// Duration is always emitted as integer milliseconds — a sub-ms rebuild
// shows as "0 ms" rather than "0s" (time.Duration.String collapses
// fractions, which makes the message inconsistent across small repos).
// The "(compacted N tombstones)" suffix is appended only when N>0 so
// pure-write flushes keep their existing v0.3 format.
func formatFlush(totalChunks, filesChanged, compacted int, dur time.Duration) string {
	msg := "reindexed: " +
		intStr(totalChunks) + " chunks total, " +
		intStr(filesChanged) + " files changed in " +
		intStr(int(dur.Milliseconds())) + " ms"
	if compacted > 0 {
		msg += " (compacted " + intStr(compacted) + " tombstones)"
	}
	return msg
}

// intStr is a tiny strconv helper to keep the formatFlush call site
// readable. Avoids importing strconv just for one call.
func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

// notifySwap delivers one nonblocking signal to the onSwap channel if
// one is registered. Tests use this to synchronize on rebuilds.
func (w *WatchedIndex) notifySwap() {
	w.onSwapMu.Lock()
	ch := w.onSwap
	w.onSwapMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// logf writes a diagnostic to the FSOptions.LogWriter (nil discards).
// Used by the watcher loop, which has no other logger — every message
// goes to stderr in the ken-mcp server, never stdout (the JSON-RPC
// channel). Prefixed so operators can grep the watcher's output.
func (w *WatchedIndex) logf(format string, args ...any) {
	if w.fsOpts.LogWriter == nil {
		return
	}
	fmt.Fprintf(w.fsOpts.LogWriter, "search: watch: "+format+"\n", args...)
}
