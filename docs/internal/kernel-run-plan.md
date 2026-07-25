# Run plan — ken vs the Linux kernel (scale-out / "Large" corpus)

**Goal:** replace the *extrapolated* Linux-kernel row in `docs/PERF-expectations.md` with a
real, publishable measurement — index time, warm-query p50/p95, and peak RSS — and read the
**p50-vs-chunks curve** to locate the O(N) flat-ANN wall (`aikit.ann.Flat`, HNSW unshipped).
Run on the **M1 Pro** (the baseline `PERF-expectations.md` standardizes on). This cannot run
in the cloud sandbox (no Go 1.26.x, proxy/`golang.org/x` blocked) — it's a local run.

Two harnesses, pick per intent:
- **`scripts/kernel_demo_bench.sh`** — purpose-built scale *ramp* (subsystems → up), TSV table
  + the curve read-out. **Use this first.** It answers "is the kernel demoable / where's the wall."
- **`scripts/perf_collect.sh large`** — single-corpus, pprof-instrumented, records.jsonl +
  meta.json under the full PERF.md discipline. Use this for the profiled, publishable pass once
  the ramp says it's worth it.

---

## 0. One-time prerequisites

```bash
# 1. Go toolchain matching go.mod (go 1.26.5 / its toolchain directive). Verify:
go version

# 2. Build ken with the RELEASE flags PERF.md requires (race OFF, inlining ON,
#    same flags goreleaser uses) — a plain `go build` also works but the release
#    binary is what makes the number publishable. GOWORK=off pins aikit to the
#    go.mod-pinned release (currently v1.11.0), NOT a local ../aikit checkout
#    (reproducibility).
cd ~/tmcode/ken
CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags='-s -w' -o /tmp/ken-rel ./cmd/ken

# 3. Fetch the embedding model (potion-code-16M) — needed for semantic/hybrid.
#    Uses the hardened, integrity-verified fetch (code-review #1 fix).
/tmp/ken-rel download-model            # → ~/.ken/model

# 4. Truthful OS-level peak RSS: macOS `/usr/bin/time -l` is built in; the script
#    auto-detects `gtime` if present. Optional but nicer:
brew install gnu-time                  # gives gtime -v

# 5. (Only if you'll open pprof web UI from the perf_collect.sh profiles)
brew install graphviz

# 6. (Only for the §4 p50-vs-chunks plot)
pip install matplotlib
```

The kernel checkout is done *by the script* (sparse, shallow, `blob:none` — a few hundred MB,
not the multi-GB full history). No manual clone needed for the ramp.

---

## 1. Primary run — the scale ramp (`kernel_demo_bench.sh`)

```bash
cd ~/tmcode/ken
KVER=v6.10 \
KEN=/tmp/ken-rel \
MODEL=~/.ken/model \
MODES="bm25 hybrid" \
scripts/kernel_demo_bench.sh
```

- **`KVER=v6.10`** — pin to the documented Large workload (the script's own default is v6.6; override it so the number matches PERF.md's pin).
- Default ramp `SCALES="fs/ext4 fs/xfs fs/btrfs | fs | fs mm kernel | fs mm kernel net"` climbs
  small→large so you can **stop the moment the curve gets ugly**. Override with `SCALES=...` (`|`-separated groups).
- Add the reranker arm with `MODES="bm25 hybrid hybrid-rerank"` (also exercises the ~21×-less-RAM int8 reranker).
- Output: a table on stdout + raw JSON under `./kernel_bench_out/` (`results.tsv`, per-run `*.index.json` / `*.search.json`, `queries.txt`).

> **16 GB RAM caveat.** This M1 Pro has 16 GB, so watch the ramp's RSS column
> closely: the ~5M-chunk full-kernel *hybrid* embedding matrix is multi-GB, and
> you may hit the **memory ceiling before the ANN p50 knee**. That's a
> legitimate, publishable finding (it *is* the "does the embedded-binary demo
> balloon" answer, §4.2) — not a failed run. If RSS climbs toward the ceiling on
> the upper rungs, don't run the full-tree hybrid pass below; use bm25 or a
> subsystem (§5).

**If the ramp stays healthy** (p50 flat-ish, RSS sane) and you want the **full-kernel headline
number**, run one standalone whole-tree pass with the same binary/model:

```bash
W=/tmp/ken-kernel-bench                         # the script's checkout
git -C "$W" sparse-checkout disable             # whole tree (~80k files)
/usr/bin/time -l /tmp/ken-rel perf index "$W" \
    --mode hybrid --chunker regex --model ~/.ken/model \
    >full.hybrid.index.json 2>full.hybrid.index.err
/tmp/ken-rel perf search "$W" --queries kernel_bench_out/queries.txt \
    --n 300 -k 10 --mode hybrid --chunker regex --model ~/.ken/model >full.hybrid.search.json
# peak RSS is the "maximum resident set size" line in full.hybrid.index.err (bytes on macOS)
```

Keep `--chunker regex` (the release default); **treesitter is not advised at kernel scale** per the docs.

---

## 2. Profiled / publishable pass — `perf_collect.sh large`

For pprof profiles + `meta.json` + `records.jsonl` under the standard layout:

```bash
# Bootstrap the pinned checkout the harness expects:
git clone --depth 1 --branch v6.10 https://github.com/torvalds/linux \
    ~/.cache/linux-v6.10

cd ~/tmcode/ken
# regex+line only (skip the slow treesitter-at-scale pass); drop --modes to run all three.
KEN=/tmp/ken-rel scripts/perf_collect.sh large --chunkers=regex,line
```

Outputs land in `bench_out/large/<date>/` — `meta.json` (machine / go / ken-commit / flags),
`records.jsonl`, `*.gtime` (RSS truth), and `profiles/*.pprof`. Analyze off-line:
`go tool pprof profiles/hybrid-regex.index.cpu.pprof`.

---

## 3. What to record for publication (PERF.md discipline)

Every published number ships annotated with: **machine** (this box: `M1 Pro / 16 GB`),
**Go toolchain** (`go1.26.5`), **build flags** (`CGO_ENABLED=0 -trimpath -ldflags='-s -w'`),
**corpus pin** (`linux v6.10`), and the **exact invocation**. For the *headline* full-kernel
number, take a **median of 3** runs (PERF.md: N=3 for expensive end-to-end); the ramp itself
is fine as a single exploratory pass.

---

## 4. Analysis template — read the curve, not the point

The three things worth extracting from `kernel_bench_out/results.tsv`:

1. **p50 vs chunks (the wall).** Plot `p50_ms` against `chunks` per mode. bm25 is the control
   (no cosine scan → flat). hybrid climbing with chunk-count = the O(N) `ann.Flat` knee.
2. **hybrid − bm25 peak RSS (the embedding-matrix cost).** The delta at each rung is the vector
   memory; extrapolate to ~5M chunks for the "does the embedded-binary `mcp.Run` demo balloon
   to multi-GB" answer.
3. **Index throughput.** `chunks / index_s` — should stay roughly flat if indexing is not the wall.

Quick no-plot summary from the TSV:

```bash
awk -F'\t' 'NR>1 {printf "%-28s %-6s chunks=%-8s idx=%-6ss p50=%-7sms rss=%sMB\n",$1,$2,$3,$4,$5,$7}' \
    kernel_bench_out/results.tsv
```

Or a 15-line matplotlib plot (p50-vs-chunks, one line per mode):

```python
import csv, collections, matplotlib.pyplot as plt
rows = list(csv.DictReader(open("kernel_bench_out/results.tsv"), delimiter="\t"))
by = collections.defaultdict(list)
for r in rows: by[r["mode"]].append((int(r["chunks"]), float(r["p50_ms"])))
for mode, pts in by.items():
    pts.sort(); xs, ys = zip(*pts)
    plt.plot(xs, ys, marker="o", label=mode)
plt.xlabel("chunks"); plt.ylabel("p50 (ms)"); plt.legend(); plt.title("ken p50 vs corpus size (linux v6.10)")
plt.savefig("kernel_p50_curve.png", dpi=140); print("wrote kernel_p50_curve.png")
```

---

## 5. Decision rule (from `kernel-demo-feasibility.md`)

- **Ramp p50 stays low + RSS sane** → full-kernel hybrid is demoable; run the whole-tree pass (§1) for the headline and ship it with real numbers.
- **p50 climbs steeply across the ramp** → don't demo full-kernel *hybrid*. Honest alternatives:
  (a) scope to a subsystem ("ken searches the kernel's networking stack" — stays under the wall,
  and it's where the SIMD win is still visible), or (b) full-kernel **bm25** framed as
  indexing-throughput + lexical-search at scale (sidesteps the O(N) scan and the multi-GB matrix).
- Either way the finding is publishable: "measured honestly, found the flat-ANN wall at ~N
  chunks" is the empirical case for the HNSW work on the risk register.

---

## 6. Gotchas

- **The O(N) wall is expected, not a bug** — none of the recent fixes touch `ann.Flat`; HNSW is
  still unshipped. The task's job is to *locate* the wall, not be surprised by it.
- **`KVER` default is v6.6** in the demo script — always pass `KVER=v6.10` to match the pin.
- **`GOWORK=off`** when building ken, so the numbers reflect the go.mod-pinned aikit release
  (currently v1.11.0), not a local `../aikit` working tree.
- **This is not the "second-machine confirmation" errand.** That debt (DECISIONS.md) specifically
  needs a **Linux x86_64** box; the M1 Pro run here is the kernel-scale/demo measurement. Different
  goal, different machine — don't conflate them in the write-up.
- **`giant`/chromium is still a TBD stub** — this plan is the `large` (kernel) workload only.
- The recent guards make this safe to run: `structural.Build`'s parse cap (C2) means a large
  generated/table-driven kernel file won't stack-overflow the process mid-index; `KEN_MAX_FILES`
  (default 1M; kernel ~80k) won't false-reject; the ctx-cancellable build means a long run is
  interruptible.
```
