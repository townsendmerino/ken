#!/usr/bin/env bash
# kernel_demo_bench.sh — measure ken against Linux-kernel-scale corpora.
#
# Purpose: answer "is 'ken vs the Linux kernel' a good demo?" empirically,
# on the M-series machine docs/PERF-expectations.md standardizes on, BEFORE
# committing to it. It walks a *scale ramp* of kernel subsystems and records,
# per (corpus, mode): chunk count, cold index time, warm query p50/p95, and
# OS-level peak RSS — so the O(N) semantic-scan curve and the memory ceiling
# are visible rather than extrapolated.
#
# Why a ramp, not just the whole kernel: the semantic arm (aikit/ann.Flat) is
# brute-force cosine, O(N) in chunks; HNSW is unshipped (DESIGN.md §10). The
# SIMD win (aikit v1.4) is a constant factor on that O(N) scan — it moves the
# wall later, not away. Plotting p50 vs chunk-count tells you where the wall is.
#
# Usage:
#   scripts/kernel_demo_bench.sh                 # default ramp, bm25 + hybrid
#   MODES="bm25 hybrid hybrid-rerank" scripts/kernel_demo_bench.sh
#   KVER=v6.6 SCALES="fs/ext4|fs|fs mm kernel net|drivers/net" ...
#   KEN=/path/to/ken MODEL=/path/to/model scripts/kernel_demo_bench.sh
#
# Output: a results table on stdout + raw JSON records under ./kernel_bench_out/.
set -euo pipefail

# ---- config (all overridable via env) --------------------------------------
KVER="${KVER:-v6.6}"                      # kernel tag to measure (pinned = reproducible)
WORK="${WORK:-/tmp/ken-kernel-bench}"     # where the sparse kernel checkout lives
OUT="${OUT:-$(pwd)/kernel_bench_out}"     # JSON records + table land here
MODES="${MODES:-bm25 hybrid}"             # search modes to test
NQ="${NQ:-300}"                           # queries per perf-search run (warm latency sample)
TOPK="${TOPK:-10}"
CHUNKER="${CHUNKER:-regex}"               # regex is the release default; treesitter not advised at scale
# Scale ramp: '|'-separated groups; each group is a space-separated sparse-checkout set.
# Ordered small → large so you can stop the moment it gets ugly.
SCALES="${SCALES:-fs/ext4 fs/xfs fs/btrfs|fs|fs mm kernel|fs mm kernel net}"

# ---- resolve the ken binary ------------------------------------------------
# Prefer an explicit $KEN, then a built ./ken, then PATH, then build one.
if [[ -n "${KEN:-}" ]]; then :;
elif [[ -x "./ken" ]]; then KEN="./ken";
elif command -v ken >/dev/null 2>&1; then KEN="$(command -v ken)";
else
  echo ">> no ken binary found; building one (go build ./cmd/ken)…" >&2
  GOWORK=off go build -o /tmp/ken ./cmd/ken    # GOWORK=off: use proxy-pinned aikit, not ../aikit
  KEN="/tmp/ken"
fi
echo ">> ken: $KEN"

# ---- resolve a Model2Vec model (needed for hybrid/semantic) ----------------
if [[ -z "${MODEL:-}" ]]; then
  if   [[ -f "cmd/ken-mcp-docs/model/model.safetensors" ]]; then MODEL="cmd/ken-mcp-docs/model";
  elif [[ -f "$HOME/.ken/model/model.safetensors" ]];        then MODEL="$HOME/.ken/model";
  elif [[ -f "testdata/model/model.safetensors" ]];          then MODEL="testdata/model";
  else
    echo ">> no model found; fetching to ~/.ken/model (ken download-model)…" >&2
    "$KEN" download-model
    MODEL="$HOME/.ken/model"
  fi
fi
echo ">> model: $MODEL"

# ---- peak-RSS wrapper (truthful OS-level number, per perf.go's note) -------
# macOS: /usr/bin/time -l → "maximum resident set size" in BYTES.
# GNU:   gtime -v        → "Maximum resident set size (kbytes)".
rss_mb() { # $1 = stderr file from the timed run → prints MB (or "?")
  local f="$1"
  if grep -q "maximum resident set size" "$f" 2>/dev/null; then
    awk '/maximum resident set size/ {printf "%.0f", $1/1048576; exit}' "$f"
  elif grep -q "Maximum resident set size" "$f" 2>/dev/null; then
    awk -F: '/Maximum resident set size/ {gsub(/ /,"",$2); printf "%.0f", $2/1024; exit}' "$f"
  else echo "?"; fi
}
TIME_BIN="/usr/bin/time"; TIME_FLAG="-l"
command -v gtime >/dev/null 2>&1 && { TIME_BIN="$(command -v gtime)"; TIME_FLAG="-v"; }

# tiny JSON field reader (no jq dependency)
jget() { python3 -c "import sys,json;d=json.load(open(sys.argv[1]));print(eval(sys.argv[2],{'d':d}))" "$1" "$2" 2>/dev/null || echo "?"; }

# ---- representative query set (code-search: NL + symbol-shaped) -------------
mkdir -p "$OUT"
QFILE="$OUT/queries.txt"
cat > "$QFILE" <<'EOF'
allocate a new inode
how are dirty pages written back to disk
read ahead pages into the page cache
acquire a spinlock with interrupts disabled
TCP retransmission timeout handling
where is the slab allocator freelist managed
mount a filesystem and parse its superblock
copy data from user space safely
schedule the next runnable task
journal commit and checkpoint
ext4_map_blocks
kmem_cache_alloc
tcp_sendmsg
folio_mark_dirty
__vfs_read
try_to_wake_up
EOF

# ---- fetch the kernel (sparse, shallow — avoids the multi-GB full history) -
if [[ ! -d "$WORK/.git" ]]; then
  echo ">> sparse shallow clone torvalds/linux@$KVER → $WORK (one-time)…" >&2
  git clone --depth 1 --branch "$KVER" --filter=blob:none --sparse \
      https://github.com/torvalds/linux "$WORK"
fi

# ---- run the ramp ----------------------------------------------------------
TABLE="$OUT/results.tsv"
printf "corpus\tmode\tchunks\tindex_s\tp50_ms\tp95_ms\tpeakRSS_MB\n" > "$TABLE"

# NB: NOT `GROUPS` — that's a bash builtin array (caller's group IDs). Clobbering
# it aborts under `set -e` on macOS's stock bash 3.2 (`/usr/bin/env bash`). ADR-none.
IFS='|' read -r -a SCALE_GROUPS <<< "$SCALES"
for grp in "${SCALE_GROUPS[@]}"; do
  echo ">> ===== corpus: [$grp] =====" >&2
  git -C "$WORK" sparse-checkout set $grp >/dev/null
  loc=$(find "$WORK" \( -name '*.c' -o -name '*.h' \) -type f 2>/dev/null | wc -l | tr -d ' ')
  label="${grp// /+}"

  for mode in $MODES; do
    margs=(--mode "$mode" --chunker "$CHUNKER")
    [[ "$mode" != "bm25" ]] && margs+=(--model "$MODEL")
    tag="${label//\//_}__${mode}"   # slashes → _ so output filenames don't imply subdirs
    idxjson="$OUT/${tag}.index.json"; idxerr="$OUT/${tag}.index.err"
    srchjson="$OUT/${tag}.search.json"

    # cold index (wrapped for peak RSS)
    "$TIME_BIN" $TIME_FLAG "$KEN" perf index "$WORK" "${margs[@]}" \
        >"$idxjson" 2>"$idxerr" || { echo "   index FAILED ($mode) — see $idxerr" >&2; continue; }
    chunks=$(jget "$idxjson" "d['chunks']")
    index_ms=$(jget "$idxjson" "d['index_ms']")
    rss=$(rss_mb "$idxerr")

    # warm query latency
    "$KEN" perf search "$WORK" --queries "$QFILE" --n "$NQ" -k "$TOPK" "${margs[@]}" \
        >"$srchjson" 2>/dev/null || { echo "   search FAILED ($mode)" >&2; continue; }
    p50=$(jget "$srchjson" "d['latency']['p50_ms']")
    p95=$(jget "$srchjson" "d['latency']['p95_ms']")

    index_s=$(python3 -c "print(f'{$index_ms/1000:.1f}')" 2>/dev/null || echo "?")
    printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\n" "$label($loc files)" "$mode" "$chunks" "$index_s" "$p50" "$p95" "$rss" >> "$TABLE"
    echo "   $mode: chunks=$chunks index=${index_s}s p50=${p50}ms p95=${p95}ms rss=${rss}MB" >&2
  done
done

echo
echo "================ RESULTS ($KVER, $(uname -m), GOMAXPROCS=$(sysctl -n hw.ncpu 2>/dev/null || nproc)) ================"
column -t -s $'\t' "$TABLE"
echo
echo "Raw JSON records + query set: $OUT/"
echo "Read the curve: plot p50_ms against chunks. A flat-ish line = headroom;"
echo "a knee that climbs with chunk count = the O(N) flat-ANN wall (hybrid only;"
echo "bm25 has no cosine scan). Compare hybrid vs bm25 RSS for the embedding-matrix cost."
