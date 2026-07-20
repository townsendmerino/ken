#!/usr/bin/env bash
# rss_bench.sh — sample ken-mcp resident memory on a target repo.
#
# Mirrors the harness Denis Trofimov used in the 2026-07-18 Cursor-MCP bench
# so the numbers are directly comparable: cold-start the server against a
# repo, drive one query to force the index build, then sample VmRSS (idle
# resident set) and VmHWM (peak high-water mark) from /proc/<pid>/status
# while the server idles.
#
# Linux only — VmRSS/VmHWM come from /proc. On macOS use `/usr/bin/time -l`
# (peak RSS) or Instruments instead.
#
# Usage:
#   scripts/rss_bench.sh <repo-path> [mode]
# Env:
#   KEN_MCP_BIN     path to the ken-mcp binary (default: ken-mcp on PATH)
#   SAMPLES         number of samples (default 60)
#   INTERVAL        seconds between samples (default 3)  → default window 180s
#   KEN_MCP_MODE etc. pass through to the server (this script sets DEFAULT_REPO)
#
# Tip: to A/B the M2 GC work, run once with GOGC=100 (pre-M2 behavior) and
# once unset (ken-mcp's GOGC=50 default), same SAMPLES/INTERVAL.

set -euo pipefail

REPO="${1:?usage: rss_bench.sh <repo-path> [mode]}"
MODE="${2:-hybrid}"
BIN="${KEN_MCP_BIN:-ken-mcp}"
SAMPLES="${SAMPLES:-60}"
INTERVAL="${INTERVAL:-3}"

if [[ ! -r /proc/self/status ]]; then
  echo "rss_bench: needs Linux /proc (VmRSS/VmHWM); on macOS use '/usr/bin/time -l'." >&2
  exit 2
fi
if ! command -v "$BIN" >/dev/null 2>&1 && [[ ! -x "$BIN" ]]; then
  echo "rss_bench: ken-mcp binary '$BIN' not found (set KEN_MCP_BIN)." >&2
  exit 2
fi

STDERR_LOG="$(mktemp -t ken-rss-bench.XXXXXX.stderr)"
FIFO="$(mktemp -u -t ken-rss-bench.XXXXXX.fifo)"
mkfifo "$FIFO"

cleanup() {
  exec 3>&- 2>/dev/null || true
  [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null || true
  rm -f "$FIFO"
}
trap cleanup EXIT

# Launch the server reading from the FIFO so we know its PID and can hold
# stdin open (keeping the session — and the built index — alive to sample).
KEN_MCP_DEFAULT_REPO="$REPO" KEN_MCP_MODE="$MODE" \
  "$BIN" <"$FIFO" >/dev/null 2>"$STDERR_LOG" &
PID=$!
exec 3>"$FIFO"

# Minimal MCP session: initialize, then one search to force the default-repo
# cold build. Stdin stays open (fd 3) so the server idles afterward.
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"rss-bench","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"main"}}}' >&3

echo "rss_bench: pid=$PID repo=$REPO mode=$MODE samples=$SAMPLES interval=${INTERVAL}s" >&2
echo "elapsed_s  VmRSS_MiB  VmHWM_MiB"

peak_kb=0 last_rss_kb=0 start=0 elapsed=0
for i in $(seq 1 "$SAMPLES"); do
  if [[ ! -r "/proc/$PID/status" ]]; then
    echo "rss_bench: server pid $PID gone (crash? see $STDERR_LOG)" >&2
    break
  fi
  rss_kb=$(awk '/^VmRSS:/{print $2}' "/proc/$PID/status")
  hwm_kb=$(awk '/^VmHWM:/{print $2}' "/proc/$PID/status")
  last_rss_kb=$rss_kb
  (( hwm_kb > peak_kb )) && peak_kb=$hwm_kb
  printf '%9d  %9d  %9d\n' "$elapsed" "$((rss_kb/1024))" "$((hwm_kb/1024))"
  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done

echo >&2
echo "=== summary ===" >&2
echo "idle VmRSS (last sample): $((last_rss_kb/1024)) MiB" >&2
echo "peak VmHWM (build HWM):   $((peak_kb/1024)) MiB" >&2
echo "server stderr log:        $STDERR_LOG" >&2
