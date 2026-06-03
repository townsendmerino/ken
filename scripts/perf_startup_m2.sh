#!/usr/bin/env bash
# perf_startup_m2.sh — measure ken-mcp's startup wall (process start
# to "starting" log line) with KEN_MCP_RERANK off vs on. Pre-M2 with
# KEN_MCP_RERANK=on this would have included the ~491 ms encoder.Load.
# Post-M2 the cost is deferred to first hybrid+rerank query, so the
# two cells should land within noise of each other.
#
# Usage:
#   scripts/perf_startup_m2.sh [iterations]
#
# Default: 5 iterations per cell. Reports median + min/max in ms.

set -euo pipefail

N=${1:-5}
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$(mktemp -d)/ken-mcp"

cd "$ROOT"
echo "building $BIN..."
go build -o "$BIN" ./cmd/ken-mcp

# launch_and_time_startup launches ken-mcp with the given env, waits
# until it prints the "starting" line, then kills it. Echoes the
# elapsed wall in milliseconds.
launch_and_time_startup() {
    local label="$1"
    shift
    local stderr
    stderr=$(mktemp)
    local start_ns end_ns
    start_ns=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1e9')
    "$@" "$BIN" 2>"$stderr" </dev/null &
    local pid=$!
    # Wait until the "starting (...)" log line appears in stderr, then kill.
    while ! grep -q "starting " "$stderr" 2>/dev/null; do
        if ! kill -0 "$pid" 2>/dev/null; then
            # Process died; emit best-effort timing.
            break
        fi
        sleep 0.005
    done
    end_ns=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1e9')
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$stderr"
    awk -v s="$start_ns" -v e="$end_ns" 'BEGIN { printf "%.2f", (e-s)/1e6 }'
}

# summarize prints "label  median=… min=… max=… ms" from a stream of
# space-separated numbers passed as $@.
summarize() {
    local label="$1"
    shift
    local sorted
    sorted=$(printf "%s\n" "$@" | sort -n)
    local median
    median=$(echo "$sorted" | awk -v n=$# 'NR == int((n+1)/2)')
    local min max
    min=$(echo "$sorted" | head -1)
    max=$(echo "$sorted" | tail -1)
    printf "  %-40s median=%6sms  min=%6sms  max=%6sms\n" "$label" "$median" "$min" "$max"
}

echo
echo "ken-mcp startup wall (n=$N per cell, perl-Time::HiRes timer)"
echo

# Cell A: KEN_MCP_RERANK unset (baseline).
samples_off=()
for i in $(seq 1 $N); do
    samples_off+=( $(launch_and_time_startup "off" env -u KEN_MCP_RERANK) )
done
summarize "KEN_MCP_RERANK unset (baseline)" "${samples_off[@]}"

# Cell B: KEN_MCP_RERANK=on with M2 (lazy load). Model dir must
# exist; we don't actually load it, but the resolver does stat it.
samples_on=()
for i in $(seq 1 $N); do
    samples_on+=( $(launch_and_time_startup "on" env KEN_MCP_RERANK=on KEN_MCP_RERANK_MODEL_DIR="$HOME/.ken/rerank-model") )
done
summarize "KEN_MCP_RERANK=on  (M2 lazy)    " "${samples_on[@]}"

echo
echo "M0 prior measurement: encoder.Load(f32) = 491 ms in isolation."
echo "Pre-M2 the on-cell would have paid that ~491 ms before 'starting'."

rm -f "$BIN"
