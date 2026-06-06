#!/usr/bin/env bash
# scripts/perf_collect.sh — workload collection wrapper for `ken perf`.
#
# Drives `ken perf index` + `ken perf search` across the full
# (mode × chunker) cross-product for one workload, wraps each
# invocation in /usr/bin/time -v (Linux) / gtime -v (macOS via
# `brew install gnu-time`) for truthful OS-level peak RSS, captures
# pprof CPU + heap profiles, and writes everything to
# bench_out/<workload>/<date>/.
#
# Usage:
#   scripts/perf_collect.sh [flags] WORKLOAD
#
# WORKLOAD:
#   small    # ken itself (sub-second iteration)
#   medium   # ~/.cache/semble-bench (matches docs/BENCH.md corpus)
#   large    # Linux kernel checkout at v6.10 (see WORKLOAD_LARGE)
#   giant    # currently a stub — see docs/internal/PERF.md Workloads
#   all      # small + medium + large in sequence
#
# Flags:
#   --modes=LIST       Comma-separated subset of {bm25,semantic,hybrid}. Default: all.
#   --chunkers=LIST    Comma-separated subset of {regex,treesitter,line}. Default: all.
#
# Examples:
#   scripts/perf_collect.sh medium                            # full 3×3 matrix (18 invocations)
#   scripts/perf_collect.sh medium --modes=bm25               # bm25-only (6 invocations)
#   scripts/perf_collect.sh large --chunkers=regex,line       # skip slow treesitter (12 invocations)
#   scripts/perf_collect.sh all --modes=bm25 --chunkers=regex # smoke pass across every workload
#
# Workload sources:
#   small  — uses $PWD (the ken repo HEAD checkout). No setup needed.
#   medium — uses ${WORKLOAD_MEDIUM:-$HOME/.cache/semble-bench}.
#            Bootstrap per docs/BENCH.md:
#              git clone https://github.com/MinishLab/semble /tmp/semble
#              cd /tmp/semble && uv sync && python benchmarks/sync_repos.py
#   large  — uses ${WORKLOAD_LARGE:-$HOME/.cache/linux-v6.10}.
#            Bootstrap:
#              git clone --depth 1 --branch v6.10 \
#                https://github.com/torvalds/linux $HOME/.cache/linux-v6.10
#   giant  — stub; requires chromium or equivalent. The corpus choice
#            is Phase-1 territory; see docs/internal/PERF.md Workloads for the
#            chromium-vs-synthetic discussion.
#
# Per-invocation outputs (under bench_out/<workload>/<date>/):
#   meta.json                                  — machine + go + ken-commit + start/end
#   records.jsonl                              — one `ken perf` JSON record per line
#   <mode>-<chunker>.index.gtime               — /usr/bin/time -v output (RSS truth)
#   profiles/<mode>-<chunker>.index.cpu.pprof  — pprof CPU profile (index phase)
#   profiles/<mode>-<chunker>.index.mem.pprof  — pprof heap profile (index phase)
#   <mode>-<chunker>.search.gtime
#   profiles/<mode>-<chunker>.search.cpu.pprof
#   profiles/<mode>-<chunker>.search.mem.pprof
#
# Analysis happens off-line with `pprof`, `benchstat`, and ad-hoc shell
# tooling over records.jsonl.
#
# Exits non-zero on any sub-command failure. Idempotent: re-running on
# the same day overwrites the day's records.

set -euo pipefail

# ── argument parsing ────────────────────────────────────────────────
ALL_MODES=(bm25 semantic hybrid)
ALL_CHUNKERS=(regex treesitter line)

print_usage() {
  cat >&2 <<'USAGE'
usage: scripts/perf_collect.sh [flags] WORKLOAD

WORKLOAD:  small | medium | large | giant | all

Flags:
  --modes=LIST       Comma-separated subset of {bm25,semantic,hybrid}. Default: all.
  --chunkers=LIST    Comma-separated subset of {regex,treesitter,line}. Default: all.
USAGE
}

usage_exit() { print_usage; exit 2; }

# Validate a CSV "value,value,..." subset against an allowed-values array.
# Returns 0 + prints the validated CSV (re-emitted unchanged) on success;
# returns non-zero + prints an error on unknown value. Empty input is
# treated as "use the default" and the caller substitutes the full set.
validate_subset() {
  local kind="$1" csv="$2"
  shift 2
  local allowed=("$@") v ok
  IFS=',' read -ra got <<<"$csv"
  for v in "${got[@]}"; do
    ok=0
    for a in "${allowed[@]}"; do
      [[ "$v" == "$a" ]] && { ok=1; break; }
    done
    if [[ "$ok" -eq 0 ]]; then
      echo "error: --$kind: unknown value '$v' (allowed: ${allowed[*]})" >&2
      return 1
    fi
  done
  printf '%s' "$csv"
}

MODES_FILTER=""
CHUNKERS_FILTER=""
POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --modes=*)    MODES_FILTER="${1#--modes=}"; shift ;;
    --modes)      [[ $# -ge 2 ]] || { echo "error: --modes requires a value" >&2; exit 2; }
                  MODES_FILTER="$2"; shift 2 ;;
    --chunkers=*) CHUNKERS_FILTER="${1#--chunkers=}"; shift ;;
    --chunkers)   [[ $# -ge 2 ]] || { echo "error: --chunkers requires a value" >&2; exit 2; }
                  CHUNKERS_FILTER="$2"; shift 2 ;;
    -h|--help)    print_usage; exit 0 ;;
    --)           shift; while [[ $# -gt 0 ]]; do POSITIONAL+=("$1"); shift; done ;;
    -*)           echo "error: unknown flag '$1'" >&2; usage_exit ;;
    *)            POSITIONAL+=("$1"); shift ;;
  esac
done

if [[ ${#POSITIONAL[@]} -ne 1 ]]; then
  usage_exit
fi
WORKLOAD="${POSITIONAL[0]}"

if [[ -n "$MODES_FILTER" ]]; then
  validate_subset modes "$MODES_FILTER" "${ALL_MODES[@]}" >/dev/null || exit 2
  IFS=',' read -ra SELECTED_MODES <<<"$MODES_FILTER"
else
  SELECTED_MODES=("${ALL_MODES[@]}")
fi
if [[ -n "$CHUNKERS_FILTER" ]]; then
  validate_subset chunkers "$CHUNKERS_FILTER" "${ALL_CHUNKERS[@]}" >/dev/null || exit 2
  IFS=',' read -ra SELECTED_CHUNKERS <<<"$CHUNKERS_FILTER"
else
  SELECTED_CHUNKERS=("${ALL_CHUNKERS[@]}")
fi

# ── locate gtime / time ─────────────────────────────────────────────
# OS-level peak RSS comes from gtime -v / time -v (GNU time). macOS
# users need `brew install gnu-time` for gtime. Fall back to bash
# `time` only if neither is present (warning emitted; the .gtime file
# will be empty in that case but the run still completes).
TIME_CMD=""
if command -v gtime >/dev/null 2>&1; then
  TIME_CMD="gtime"
elif [[ -x /usr/bin/time ]] && /usr/bin/time -v true 2>/dev/null; then
  TIME_CMD="/usr/bin/time"
else
  echo "warning: gtime / /usr/bin/time -v not available; OS-level RSS won't be captured" >&2
  echo "  install with: brew install gnu-time (macOS) or apt-get install time (Linux)" >&2
fi

# ── workload resolution ─────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

resolve_workload() {
  case "$1" in
    small)
      echo "$REPO_ROOT"
      ;;
    medium)
      echo "${WORKLOAD_MEDIUM:-$HOME/.cache/semble-bench}"
      ;;
    large)
      echo "${WORKLOAD_LARGE:-$HOME/.cache/linux-v6.10}"
      ;;
    giant)
      echo ""
      ;;
    *)
      return 1
      ;;
  esac
}

# ── run one workload ────────────────────────────────────────────────
run_workload() {
  local wl="$1"
  local wl_path
  wl_path="$(resolve_workload "$wl")" || { echo "unknown workload: $wl" >&2; exit 2; }

  if [[ "$wl" == "giant" ]]; then
    echo "skip: giant workload is a Phase-1-defined stub — see docs/internal/PERF.md Workloads" >&2
    return 0
  fi
  if [[ -z "$wl_path" || ! -d "$wl_path" ]]; then
    echo "error: $wl workload path '$wl_path' does not exist; see script header for bootstrap" >&2
    exit 1
  fi

  local date_stamp
  date_stamp="$(date +%Y-%m-%d)"
  local out_dir="$REPO_ROOT/bench_out/$wl/$date_stamp"
  mkdir -p "$out_dir/profiles"

  # Build ken once with release flags so every invocation measures the
  # same binary. -trimpath strips host paths; -ldflags='-s -w' strips
  # symbol table + DWARF for the binary the user would actually ship.
  local ken_bin="$out_dir/ken"
  echo "build: $ken_bin (trimpath, -s -w; CGO_ENABLED=0)" >&2
  CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$ken_bin" "$REPO_ROOT/cmd/ken"

  # Optional queries file for `perf search`. Falls back to a small
  # built-in set if not provided. Phase-1 hardware can point this at a
  # 1000-query corpus pulled from semble's annotations.
  local queries_file="${PERF_QUERIES:-}"
  local queries_owned=0
  if [[ -z "$queries_file" ]]; then
    queries_file="$out_dir/queries.txt"
    queries_owned=1
    cat >"$queries_file" <<'EOF'
# Minimal built-in query set for sanity. Override with PERF_QUERIES=FILE.
parse url
build index
http client
function definition
error handling
async await
class constructor
return statement
import package
type alias
EOF
  fi

  # ── meta.json header ──────────────────────────────────────────────
  # modes_run / chunkers_run let downstream analysis distinguish a
  # filtered run (--modes=bm25) from a full 3×3 matrix run. Critical
  # for any aggregate comparison across dated dirs.
  local started_at modes_json chunkers_json
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  modes_json="$(printf '"%s",' "${SELECTED_MODES[@]}")"
  chunkers_json="$(printf '"%s",' "${SELECTED_CHUNKERS[@]}")"
  {
    echo "{"
    echo "  \"workload\": \"$wl\","
    echo "  \"workload_path\": \"$wl_path\","
    echo "  \"started_at\": \"$started_at\","
    echo "  \"uname\": \"$(uname -a)\","
    echo "  \"go_version\": \"$(go version)\","
    echo "  \"ken_commit\": \"$(git -C "$REPO_ROOT" rev-parse HEAD)\","
    echo "  \"build_flags\": \"CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'\","
    echo "  \"modes_run\": [${modes_json%,}],"
    echo "  \"chunkers_run\": [${chunkers_json%,}]"
    echo "}"
  } >"$out_dir/meta.json"

  # Truncate records.jsonl so re-running today doesn't accumulate.
  : >"$out_dir/records.jsonl"

  # ── (mode × chunker) cross-product ────────────────────────────────
  # Use the globally-filtered SELECTED_MODES / SELECTED_CHUNKERS so a
  # --modes=bm25 --chunkers=regex invocation runs only the 1×1 = 2
  # invocations instead of 3×3 = 18.
  local mode chunker tag
  for mode in "${SELECTED_MODES[@]}"; do
    for chunker in "${SELECTED_CHUNKERS[@]}"; do
      tag="$mode-$chunker"
      echo "  $wl: $tag" >&2

      # `perf index` — one record + cpu + mem profile.
      local cpu_index="$out_dir/profiles/$tag.index.cpu.pprof"
      local mem_index="$out_dir/profiles/$tag.index.mem.pprof"
      local gtime_index="$out_dir/$tag.index.gtime"
      if [[ -n "$TIME_CMD" ]]; then
        "$TIME_CMD" -v -o "$gtime_index" \
          "$ken_bin" perf index "$wl_path" \
          --mode "$mode" --chunker "$chunker" \
          --cpuprofile "$cpu_index" --memprofile "$mem_index" \
          >>"$out_dir/records.jsonl"
      else
        "$ken_bin" perf index "$wl_path" \
          --mode "$mode" --chunker "$chunker" \
          --cpuprofile "$cpu_index" --memprofile "$mem_index" \
          >>"$out_dir/records.jsonl"
      fi

      # `perf search` — one record + cpu + mem profile.
      local cpu_search="$out_dir/profiles/$tag.search.cpu.pprof"
      local mem_search="$out_dir/profiles/$tag.search.mem.pprof"
      local gtime_search="$out_dir/$tag.search.gtime"
      if [[ -n "$TIME_CMD" ]]; then
        "$TIME_CMD" -v -o "$gtime_search" \
          "$ken_bin" perf search "$wl_path" \
          --mode "$mode" --chunker "$chunker" \
          --queries "$queries_file" --n 1000 -k 10 \
          --cpuprofile "$cpu_search" --memprofile "$mem_search" \
          >>"$out_dir/records.jsonl"
      else
        "$ken_bin" perf search "$wl_path" \
          --mode "$mode" --chunker "$chunker" \
          --queries "$queries_file" --n 1000 -k 10 \
          --cpuprofile "$cpu_search" --memprofile "$mem_search" \
          >>"$out_dir/records.jsonl"
      fi
    done
  done

  # ── meta.json footer (rewrite with ended_at) ──────────────────────
  local ended_at
  ended_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  python3 - "$out_dir/meta.json" "$ended_at" <<'PY' || true
import json, sys
path, ended = sys.argv[1], sys.argv[2]
with open(path) as f:
    d = json.load(f)
d["ended_at"] = ended
with open(path, "w") as f:
    json.dump(d, f, indent=2)
PY

  # Clean up the built-in queries file if we created it; preserve the
  # user's own file if PERF_QUERIES pointed somewhere.
  if [[ "$queries_owned" -eq 1 ]]; then
    rm -f "$queries_file"
  fi

  echo "done: $out_dir" >&2
}

case "$WORKLOAD" in
  small|medium|large|giant)
    run_workload "$WORKLOAD"
    ;;
  all)
    # --modes / --chunkers filters apply uniformly to each workload in
    # the cascade — handy for a smoke pass like
    # `scripts/perf_collect.sh all --modes=bm25 --chunkers=regex`.
    run_workload small
    run_workload medium
    run_workload large
    ;;
  *)
    echo "error: unknown workload '$WORKLOAD'" >&2
    usage_exit
    ;;
esac
