# Plan: Pure-Go CodeRankEmbed Neural Rerank (Stage 6)

**Status:** Proposal (for Claude Code to implement; the desktop app does not edit the tree)
**Goal:** Add a second-stage neural reranker that re-scores the top-N hybrid candidates with a *pure-Go forward pass* of `nomic-ai/CodeRankEmbed`, with zero cgo and no per-platform vendored artifacts.
**Non-goal:** Replacing potion-code-16M. Potion remains the cheap first-stage retriever over the whole corpus; CodeRankEmbed only ever sees the query plus the top-N shortlist.

> **Read §12 first.** The build is gated on two cheap experiments (M0 ceiling validation, M2/M3 latency go/no-go) that can kill the feature before the expensive forward-pass work. Both are knowable early; neither requires finishing the port. This ordering is deliberate — the two things most likely to sink the project are front-loaded, not buried.

---

## 0. Why this is feasible (the headline)

Three facts make a pure-Go port tractable rather than a research project:

1. **CodeRankEmbed is a bi-encoder, not a cross-encoder.** A document's embedding is query-independent, so the doc side is *cacheable*. At query time the only mandatory transformer work is **one** forward pass for the query; candidates can be embedded lazily and memoized by content hash. This collapses the steady-state cost to `1 query forward + N cached lookups + N cosines`.
2. **The tokenizer is already done.** CodeRankEmbed uses the stock `bert-base-uncased` WordPiece tokenizer (`do_lower_case=true`, `BertNormalizer`, `BertPreTokenizer`). `internal/embed/tokenize.go` already implements this exact pipeline and is parity-tested against `transformers.AutoTokenizer`. The only addition needed is `[CLS] … [SEP]` wrapping.
3. **The weight loader is already done.** `internal/embed/safetensors.go` reads F32 tensors zero-copy by name. CodeRankEmbed ships a single F32 `model.safetensors`. We just read more tensors.

The genuinely new work is ~1–2k LOC of transformer forward pass (embeddings → 12 × NomicBert layer → CLS pool) plus a numerical-parity harness. No new architecture research is required — the config is fully specified below.

---

## 1. The model, exactly

From `nomic-ai/CodeRankEmbed/config.json` (architecture `NomicBertModel`):

| Field | Value | Consequence for the port |
|---|---|---|
| `n_layer` | 12 | 12 identical encoder blocks |
| `n_embd` | 768 | hidden size |
| `n_head` | 12 | head_dim = 64 |
| `n_inner` | 3072 | MLP intermediate |
| `activation_function` | `swiglu` | **gated** MLP, SiLU gate (see §6.4) |
| `prenorm` | **false** | **post-norm** residual structure (BERT-style), two LayerNorms per block |
| `use_rms_norm` | false | standard LayerNorm (weight + bias) |
| `layer_norm_epsilon` | 1e-12 | LN eps |
| `qkv_proj_bias` | false | no bias on the Q/K/V projection |
| `mlp_fc1_bias` / `mlp_fc2_bias` | false | no bias on either MLP matmul |
| `rotary_emb_fraction` | 1.0 | RoPE applied to the **full** 64-dim head |
| `rotary_emb_base` | **1000** | note: 1000, *not* the usual 10000 |
| `rotary_emb_interleaved` | false | NeoX-style `rotate_half`, not GPT-J interleaved |
| `scale_attn_weights` | true | attention scale = 1/√64 = 0.125 |
| `causal` | false | bidirectional; only padding is masked |
| `parallel_block` | false | sequential attention → MLP |
| `type_vocab_size` | 2 | token-type embeddings exist; single segment → row 0 |
| `vocab_size` | 30528 | bert vocab 30522 padded to a multiple of 64 |
| `n_positions` / `max_trained_positions` | 8192 / 2048 | RoPE supports long ctx; we cap chunks well under this |
| `torch_dtype` | float32 | weights are F32 — matches our loader |

Pooling (`1_Pooling/config.json`): **CLS token** (`pooling_mode_cls_token=true`). `modules.json` lists only `Transformer` + `Pooling` — **no `Normalize` module**, so the model's raw sentence vector is the un-normalized CLS hidden state. (We L2-normalize ourselves before cosine; ranking is invariant to it, but parity goldens should be compared as cosine, not bit-exact.)

Tokenizer (`tokenizer_config.json`): `BertTokenizer`, `do_lower_case=true`, specials `[PAD]=0 [UNK]=100 [CLS]=101 [SEP]=102 [MASK]=103`.

**Mandatory query prefix** (from the model card): queries — and only queries — must be prefixed with
`"Represent this query for searching relevant code: "`. Documents (code) get no prefix.

> ⚠️ **Confirm against the checkpoint before coding the forward pass.** The table above is from the config; the *exact tensor names and the fc1 output width* must be read from `model.safetensors`. First task in Milestone 1 is to dump `SafetensorsFile.Names()` and every shape. In particular verify: (a) fc1 weight is `[2*n_inner, n_embd] = [6144, 768]` (gate+value fused, split on dim 0), (b) whether the attention out-projection carries a bias (`qkv_proj_bias=false` governs only the input projection), (c) presence of an embeddings LayerNorm and a final encoder LayerNorm.

---

## 2. What is reused vs. new

**Reused as-is**
- `internal/embed/safetensors.go` — F32 zero-copy tensor reads. CodeRankEmbed is all F32.
- `internal/embed/tokenize.go` — WordPiece + BertNormalizer + BertPreTokenizer. Load CodeRankEmbed's own `tokenizer.json` (30522 vocab) through the existing `LoadTokenizerFromFS`.
- `internal/embed/pool.go::l2Normalize` — same float64-accumulated L2 norm.
- `internal/modelfetch/` — extend to fetch the rerank model snapshot.
- The candidate over-fetch in `hybrid.go` (already `k*5`) — the shortlist the reranker consumes.

**New (the work)**
- `internal/coderank/` — the transformer (weights struct, forward pass, RoPE, attention, SwiGLU, LN, CLS pool). Pure Go.
- `Tokenizer.EncodeWithSpecials(text)` (or a thin wrapper) — prepend `[CLS]`, append `[SEP]`. ~10 LOC; keep the existing `Encode` untouched so potion parity is unaffected.
- A `Reranker` interface + `ModeHybridRerank` wiring in `internal/search`.
- A doc-embedding cache (content-hash → `[]float32`).
- A golden-parity harness (`scripts/pin_coderank.py` + `internal/coderank/golden_test.go`).
- CLI/MCP/env plumbing + model resolution + `ken download-model --rerank`.

---

## 3. Package layout

```
internal/coderank/
  model.go        // CodeRankModel: load weights, Encode(text, isQuery) []float32
  weights.go      // layerWeights struct; tensor-name → struct mapping; shape validation
  forward.go      // embeddings → layers → CLS pool
  attention.go    // RoPE + multi-head self-attention (bidirectional)
  mlp.go          // SwiGLU gated MLP
  layernorm.go    // standard LayerNorm (weight+bias, eps 1e-12), float64 accumulators
  rope.go         // rotary cos/sin tables (base 1000), rotate_half apply
  linalg.go       // matmul wrappers over gonum (see §7), plus softmax/silu
  model_test.go
  golden_test.go  // skips unless testdata/coderank-model present (mirrors embed)
```

Keep it a sibling of `internal/embed`, not inside it: different inference algorithm, different artifact, different test gating. The two share only `tokenize.go` and `safetensors.go`, which already live in `embed` and are importable.

> Naming: the existing `rerank.go` / `rerankTopK` in `internal/search` is the **heuristic boost/penalty** pass. To avoid collision, call the new stage **"neural rerank"** everywhere and name the type `NeuralReranker`.

---

## 4. Weight loading (`weights.go`)

Read once at startup via the existing safetensors loader. Expected per-layer tensors (verify exact names from the dump — nomic uses `emb_ln`, `encoder.layers.{i}.*`):

```
embeddings.word_embeddings.weight        [30528, 768] F32
embeddings.token_type_embeddings.weight  [2, 768]     F32
emb_ln.weight / emb_ln.bias              [768]        F32
per layer i in 0..11:
  attn.Wqkv.weight                       [2304, 768]  F32   (no bias)
  attn.out_proj.weight                   [768, 768]   F32   (bias? confirm)
  norm1.weight / norm1.bias              [768]        F32   (attention post-LN)
  mlp.fc1.weight                         [6144, 768]  F32   (gate+value; no bias)
  mlp.fc2.weight                         [768, 3072]  F32   (no bias)
  norm2.weight / norm2.bias              [768]        F32   (output post-LN)
(optional) final encoder LayerNorm       [768]        F32
```

Validate every shape against the config at load and fail loudly with the tensor name — this is the analogue of the embed loader's shape checks. Ignore any MLM/classification-head tensors (`summary_*`, `lm_head`, `cls.*`) — unused for embeddings.

**Memory:** 137M F32 ≈ **~550 MB** resident if loaded the current way (whole file into a `[]byte`, tensors alias it). That's ~9× potion. Acceptable on a dev box, heavy for a small MCP host. Two mitigations, both deferrable:
- Add an `mmap` path to the safetensors loader (the loader already flags this as a future option). Zero-copy aliasing already works; only the `os.ReadFile` needs to become a mapped region.
- Optional int8 weight quantization (§7) → ~140 MB and a speedup.

---

## 5. Tokenization for the transformer

Use the existing pipeline, add specials:

```
ids = [CLS] ++ tokenizer.Encode(text) ++ [SEP]
```

- Queries: `text = "Represent this query for searching relevant code: " + rawQuery` (prefix first, *then* tokenize — the prefix tokenizes as ordinary wordpieces).
- Truncate to a max length (start at 512; the model trained at 2048 and supports 8192, but rerank candidates are chunk-sized — 512 keeps latency bounded and matches `tokenizer_config.max_length=512`). Truncate from the right (`truncation_side=right`), preserving `[CLS]` and re-appending `[SEP]`.
- token_type ids are all 0 (single segment). No need to materialize — just add `token_type_embeddings[0]` to every position.

No attention-padding mask is needed if we run **one sequence per forward pass** (recommended first cut, §6). Batched mode (§9) adds a padding mask.

---

## 6. The forward pass (the core)

All accumulators in **float64**, output cast to float32 at the end — the same precision contract the embed package already enforces (and the same trap that bit float32 accumulation before). LayerNorm, softmax, and the matmul reductions all accumulate in f64.

### 6.1 Embeddings
```
h[t] = word_emb[ids[t]] + token_type_emb[0]      for t in 0..L-1
h    = LayerNorm(h; emb_ln.weight, emb_ln.bias)   // eps 1e-12
```
No absolute position embedding (rotary handles position).

### 6.2 Per-layer structure (post-norm, prenorm=false)
```
a = SelfAttention(h)                       // §6.3
h = LayerNorm(h + a; norm1.w, norm1.b)
m = SwiGLU_MLP(h)                           // §6.4
h = LayerNorm(h + m; norm2.w, norm2.b)
```
Dropout is identity at inference (drop all `*_pdrop`).

### 6.3 Self-attention (`attention.go`)
```
QKV = h · Wqkvᵀ                  // [L,768]·[768,2304] → [L,2304], no bias
split into Q,K,V each [L,768]; reshape to [n_head=12, L, head_dim=64]
apply RoPE to Q,K (per head, full 64 dims, base 1000, rotate_half) // §6.5
scores = (Q · Kᵀ) * (1/√64)      // [12, L, L]; bidirectional, no causal mask
attn   = softmax(scores, axis=last)        // f64 softmax
ctx    = attn · V                          // [12, L, 64] → merge heads → [L,768]
out    = ctx · Woᵀ (+ bo if present)       // [L,768]
```

### 6.4 SwiGLU MLP (`mlp.go`)
```
u = h · fc1ᵀ                     // [L,768]·[768,6144] → [L,6144], no bias
gate, val = split(u, 2, axis=last)         // each [L,3072]
g = SiLU(gate) ⊙ val                       // SiLU(x)=x·sigmoid(x)
m = g · fc2ᵀ                     // [L,3072]·[3072,768] → [L,768], no bias
```
> Confirm the split convention (which half is gate) and fc1 width from the checkpoint shapes — if fc1 is `[6144,768]` the gate/value split is on the 6144 axis. If the dump shows separate `fc1` and `fc12`/`fc1_gate` tensors, adapt the read accordingly.

### 6.5 RoPE (`rope.go`)
```
inv_freq[i] = 1 / (1000^(2i/64))           for i in 0..31
pos m: cosθ = cos(m · inv_freq), sinθ = sin(m · inv_freq)   // precompute [L, 32]
rotate_half(x): x1=x[:32], x2=x[32:]; return concat(-x2, x1)
RoPE(x)[m] = x·cos + rotate_half(x)·sin    // cos/sin broadcast to 64 by duplicating halves
```
Precompute cos/sin tables once per forward call (cheap), keyed by sequence length.

### 6.6 Pooling
```
sentence = h[0]                            // CLS hidden state, [768]
return l2Normalize(sentence)               // our cosine convention
```

---

## 7. Matmul backend — pure Go (and the f32/f64/parity/latency coupling)

Wrap the kernel behind `linalg.go` so the backend is swappable from day one:

```go
// dst[M,N] = a[M,K] · bᵀ where b is [N,K] (weights stored row-major [out,in])
func matmulBT(a, b []float32, M, K, N int) []float32
```

Most weights are stored `[out, in]`, so we want `A · Bᵀ` (no transpose copy needed).

**Three knobs that trade against each other — do not treat them as independent "decide later" items:**

- **Parity bar** wants f64 accumulation in the GEMM reductions (the discipline the embed package already follows).
- **Latency** (§2 risk, existential) wants the fast f32 path.
- These collide: gonum's fast kernel is `Sgemm` (f32 accumulation); strict f64 means `Dgemm` (~2× memory, slower). So *insisting on the tight cosine bar can force the slow GEMM and worsen the latency story.*

**Resolution: let end-to-end NDCG be the acceptance gate, not the cosine bar (see §11).** For a reranker, only *ordering* quality matters — bit-fidelity to the reference embedding does not. Start with **f32 GEMM** and a **loose** cosine sanity bar (1e-3, even 1e-2 is acceptable) and confirm NDCG holds. Only tighten toward f64 if ordering actually degrades. Keep f64 for the cheap, parity-sensitive, non-GEMM reductions (LayerNorm, softmax, final L2) where it costs nothing.

**Backend choice — weigh the dependency, don't default to it.** `gonum.org/v1/gonum` is pure Go (no cgo) with asm kernels, but it's a large dependency for what is essentially **one** GEMM, and ken has been deliberate about deps. Because **parallelizing across candidates matters more than single-kernel quality** (§ optimizations below), a **hand-rolled ~80-LOC blocked f32 GEMM is likely the better fit**: dependency-free, no f32-vs-f64 API mismatch to fight, and consistent with ken's minimalism. Decide in M3 by benchmarking the hand-rolled kernel against gonum `Sgemm` on real shapes — but bias toward hand-rolled unless gonum is clearly faster. (Counterpoint to actually measure: gonum's `Sgemm` may beat a naive hand-roll handily; the blocked version closes most of that gap. Measure before deciding.)

> **Honest cost note:** the ~1–2k-LOC hand-rolled transformer is a *permanent, numerically-sensitive maintenance surface* — a real ongoing cost compared to potion's tiny pooling inference. This is part of the M0 go/no-go calculus, not a footnote.

**Optimizations, in priority order (all pure Go):**
1. **Parallelize across candidates** — reuse the `runtime.NumCPU()` worker-pool pattern from `walkAndChunkFSWithModel`. N independent forward passes fan out trivially. This is the single biggest lever and is *not* deferred — it's part of the M2/M3 latency gate.
2. **Batch into one GEMM per layer** — pad candidates to max length, add a padding mask in attention. Bigger GEMMs amortize better. Adds mask bookkeeping; do after single-sequence correctness.
3. **int8 weight quantization** — per-channel symmetric quant of the big projection weights, f32 activations. ~4× memory cut and a speedup; its own parity check. Later milestone.

**Latency budget (this is the existential risk — see §2/§13, not a perf afterthought).** Single thread, f32, L≈256: dominated by the two MLP GEMMs, ~12 layers × (256·768·6144 + 256·3072·768)·2 ≈ **~29 GFLOP per sequence**. At a pessimistic pure-Go 10–30 GFLOP/s that is ~1–3 s **per sequence**. The plan's "steady state = 1 query forward + N cache hits" assumes the candidate docs are *already cached* — but code-search queries are diverse, real doc-cache hit rates are low, so most queries pay **many cold forwards**. Cold path at `rerankN=50` is therefore ~50–150 s single-thread, ~6–19 s across 8 cores — for the first query, recurring as new chunks surface. **That is a rough interactive story and must be confronted early (M2/M3 gate), not optimized in M7.** Likely landing spots: a small `rerankN`, an `isSymbolQuery` gate (§9.3), batching, or accepting this as a batch/offline feature rather than always-on. Measure the real gonum/hand-rolled kernel throughput before despairing — the GFLOP/s estimate is deliberately pessimistic.

---

## 8. Doc-embedding cache — the perf keystone

Because the doc side is query-independent:

- `internal/coderank` exposes `Encode(text, isQuery bool) []float32`.
- The `NeuralReranker` holds an LRU (or a map keyed by `chunk.Chunk` content hash) of doc embeddings. On a candidate, look up by hash; miss → compute + store.
- Tie cache invalidation to the existing incremental-index churn: a chunk's content hash changes when the file changes (fsnotify path already re-chunks), so stale entries simply never get hit again; bound the cache with an LRU so deleted chunks age out. **No correctness coupling to tombstones** — the hash is the key. (Confirmed by the implementing agent: coupling this to `watch.go`'s incremental-index machinery would be fragile; the content-hash key is the right call.)
- **Caveat (see §7 latency):** the cache helps repeated/overlapping result sets, but code-search queries are diverse, so real-world hit rates are modest. Do not let the cache's existence paper over the cold-path latency — that is sized by §2/§7 and gated in M2/M3.
- Optional warm-up: a background goroutine can pre-embed the top-of-corpus chunks after index build, but the default should be lazy (never pay for cold chunks that never surface).

This is what makes "the heavy model, in pure Go, at query time" actually interactive.

---

## 9. Integration into `internal/search`

### 9.1 Interface
```go
// Reranker re-scores candidates for a query. Implementations are goroutine-safe.
type Reranker interface {
    // Returns one score per candidate, higher = more relevant.
    Rerank(query string, cands []chunk.Chunk) []float64
}
```
`NeuralReranker` (in a new `internal/search` file or `internal/coderank`) implements it: embed query once (with prefix), embed/lookup each candidate, cosine.

### 9.2 New mode
Add `ModeHybridRerank` to the `Mode` enum and `ParseMode`/`ModeNames`. It runs the full hybrid pipeline, then reranks the shortlist. Keep `ModeHybrid` unchanged so nothing regresses and the rerank is strictly opt-in.

### 9.3 Wiring point (`index.go` `SearchMode`)
The cleanest seam is *inside* the hybrid branch, operating on `hybridSearch`'s output **before** truncation to `k`:

```go
case ModeHybrid, ModeHybridRerank:
    ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, k, -1)
    if mode == ModeHybridRerank && ix.reranker != nil {
        ranked = ix.reranker.apply(query, ranked, ix.chunks, rerankN) // rerankN ≥ 5*k, e.g. 50–100
    }
    // existing tombstone filter + truncate to k
```

- `rerankN` (how deep to rerank) is the key recall knob. The reranker can only reorder what stage-1 surfaced, so set `rerankN` ≥ the hybrid over-fetch (`k*5`) and measure recall@N (§11). Expose as `KEN_RERANK_TOP_N` / `--rerank-top-n`.
- **Score combination:** start by *replacing* the fused score with the rerank cosine for ordering (cleanest, matches how a reranker is meant to dominate). Optionally blend `final = β·rerankCos + (1-β)·normalizedFusedScore` behind a flag if pure replacement hurts on symbol queries (where BM25 exactness matters); decide empirically.
- Store `reranker Reranker` on `Index`; nil when disabled. `SearchMode` downgrades `ModeHybridRerank → ModeHybrid` transparently if `reranker == nil`, mirroring the existing model-missing downgrade pattern.
- **`isSymbolQuery` gate is part of the design, not an optional tweak.** Reuse `adaptive.go::isSymbolQuery`: skip the neural rerank entirely for symbol queries and fall straight through to `ModeHybrid`. Symbol queries already lean on BM25 exactness (`alphaSymbol=0.3`) and gain little from semantic rerank — so this is a free latency win exactly where the reranker adds least. Wire it in M4 and measure its NDCG-neutrality in M6.

### 9.4 `find_related`
`FindRelated` is already a bi-encoder cosine query. It could optionally rerank its results with the same machinery, but leave it on potion for v1 — keep scope to `search`.

---

## 10. Model fetch, resolution, CLI, MCP

Mirror the existing patterns exactly so it feels native:

- **Resolution order:** `--rerank-model <DIR>` → `$KEN_RERANK_MODEL_DIR` → `~/.ken/rerank-model` → (dev fallback) `./testdata/coderank-model`. Same priority shape as the embed model.
- **`ken download-model --rerank`** fetches the CodeRankEmbed snapshot (`config.json`, `tokenizer.json`, `model.safetensors`, `1_Pooling/config.json`) into `~/.ken/rerank-model` via the existing `internal/modelfetch` (no Python).
- **CLI:** `ken search … --mode=hybrid-rerank [--rerank-model DIR] [--rerank-top-n 50]`.
- **MCP env (`cmd/ken-mcp/env.go` helpers):**
  - `KEN_MCP_RERANK` = `off`/`on` (default `off` in v1 — opt-in while it's new).
  - `KEN_MCP_RERANK_MODEL_DIR` — empty ⇒ rerank disabled with a stderr warning (same downgrade ethos as the missing-embed-model case: degrade, don't crash).
  - `KEN_MCP_RERANK_TOP_N` — default 50; validated via `envInt`.
  - `KEN_MCP_RERANK_CACHE_SIZE` — LRU bound for doc embeddings; default e.g. 4096.
  - All validated at startup with the existing warn-and-fallback discipline. **Stdout contract:** the model load + any warning must go to stderr only — the `TestBinary_StdoutIsCleanJSONRPC` guard already enforces this; add a case that boots with rerank enabled.

---

## 11. Testing & validation

**Acceptance hierarchy — get the order right.** For a reranker, *ordering quality is the product*; fidelity to the reference embedding is a means, not the goal. So:

- **PRIMARY acceptance gate: end-to-end NDCG@10** on the CoIR slice (§ M6). If `hybrid-rerank` beats `hybrid` by a worthwhile margin, the feature works — regardless of how loose the embedding cosine is.
- **SECONDARY sanity check: golden cosine.** A loose bar that catches gross forward-pass bugs (a transposed weight, wrong RoPE), not a fidelity contract. This is a deliberate departure from the embed package's tight 1e-5 — and the right one here, because the f64-everywhere bar would force the slow GEMM (§7).

1. **End-to-end NDCG (PRIMARY).** Extend `docs/BENCH.md`'s harness: run the CoIR slice in `testdata/bench/` under `hybrid` and `hybrid-rerank`, report NDCG@10 for both, plus **recall@rerankN**. Recall@N is the diagnostic: if it's low, raise `rerankN` before blaming the model (rerank is recall-bounded by stage-1, §9.3). Expected lift ~40 → high-40s/50s, *not* the standalone 60.1. **This number is also produced in M0 with the Python model — the M6 run just confirms the Go port reproduces it.**
2. **Forward-pass golden cosine (SECONDARY).** `scripts/pin_coderank.py` loads `nomic-ai/CodeRankEmbed` via `sentence-transformers` (`trust_remote_code=True`), encodes ~20 queries + snippets (with the query prefix, an empty string, a unicode/`[UNK]`-heavy string, and a >512-token doc for truncation), dumps `{text, is_query, embedding}` to `testdata/coderank_golden.json`, sanitizing non-finite → null like `pin_inference.py`. Go test asserts **cosine ≥ 1 − 1e-3** (start here with f32 GEMM; tighten only if NDCG needs it). Skip unless `testdata/coderank-model/` present (mirrors `internal/embed`).
3. **Tokenizer parity** — existing harness covers WordPiece; add a check that `[CLS]/[SEP]` wrapping + query prefix match `BertTokenizer(...)` ids.
4. **Component unit tests** — RoPE vs a tiny hand-computed example; LayerNorm vs known mean/var; SiLU; softmax; `matmulBT` vs a naive triple loop on random small matrices. These localize forward-pass drift to one component instead of hunting it across the stack.
5. **Determinism** — same query → same ordering across runs (fixed iteration order; no map iteration in score paths).
6. **Stdout-clean MCP test** — boot `ken-mcp` with rerank enabled + a model dir; assert clean JSON-RPC.

---

## 12. Milestones — two gates before the expensive work

| # | Deliverable | Gate? | Rough effort |
|---|---|---|---|
| **M0** | **Ceiling validation, zero Go.** `pin_coderank.py` loads real CodeRankEmbed, reranks ken's *actual* stage-1 shortlist (export the hybrid top-`rerankN` for the CoIR queries), reports NDCG@10 + recall@rerankN for several `rerankN` values. | **GO/NO-GO for everything below.** If real-model-over-ken-stage-1 lifts NDCG only ~2–3 pts (stage-1 recall caps it), the pure-Go port is **not** worth ~2–3 weeks + 550 MB + multi-second cold latency. Inverts the risk: measure payoff before building. | **~1 d** |
| M1 | Dump CodeRankEmbed tensor names/shapes; `weights.go` loader + shape validation; `EncodeWithSpecials` + query prefix; `pin_coderank.py` golden generation | — | 2–3 d |
| M2 | Single-sequence forward pass (`forward/attention/mlp/rope/layernorm`); pass the **loose** golden cosine bar; **measure real per-sequence latency on target hardware** | **Latency reality check** (with M3) | 4–7 d |
| M3 | `linalg.go`: benchmark hand-rolled blocked f32 GEMM vs gonum `Sgemm`, pick one; add candidate-parallelism; component unit tests | **GO/NO-GO on interactive latency.** With parallelism + chosen `rerankN`, is a cold first query tolerable? If not, decide now: shrink `rerankN`, gate on `isSymbolQuery`, or reposition as a batch/offline feature. Do **not** defer this to M7. | 3–5 d |
| M4 | `NeuralReranker` + doc-embedding LRU cache; `isSymbolQuery` gate; `ModeHybridRerank`; `SearchMode` wiring + transparent downgrade | — | 2–3 d |
| M5 | CLI flags, `~/.ken/rerank-model` resolution, `download-model --rerank`, MCP env vars + stdout-clean test | — | 2–3 d |
| M6 | NDCG + recall@N benchmark in `docs/BENCH.md` (confirms the Go port reproduces M0's number); tune `rerankN` and score-combination β | — | 2–3 d |
| M7 (opt) | Batched GEMM with padding mask (parallelism already landed in M3) | — | 2–4 d |
| M8 (opt) | int8 weight quantization + its own parity check; mmap safetensors path | — | 3–5 d |

**M0 and M3 are the decision points.** M0 answers "is the payoff real?" in a day with no Go. M3 answers "is it interactively fast enough?" before most of the integration cost is sunk. Only if both pass is the full M1–M6 path (~2–3 weeks) justified.

---

## 13. Risks & open questions

Ranked by what's most likely to kill the feature:

- **(1) Payoff ceiling.** Rerank is recall-bounded by stage-1, so the lift may be small (§9.3). **This is the top risk and is resolved cheaply by M0** — measure the real-model lift over ken's actual shortlist *before* building anything. If it's ~2–3 pts, stop.
- **(2) Cold-cache latency — existential, not a perf footnote.** A pure-Go 137M forward is ~1–3 s/seq single-thread; diverse queries mean low doc-cache hit rates, so most queries pay many cold forwards (`rerankN=50` → tens of seconds single-thread, single-digit-to-teens seconds across cores, on the *first* query and recurring as new chunks surface). **Gated at M2/M3, not M7.** Levers: small `rerankN`, the `isSymbolQuery` gate (§9.3), batching/parallelism, or repositioning as batch/offline. Measure the real kernel throughput before despairing — the GFLOP/s estimate is deliberately pessimistic and gonum `Sgemm` may beat it.
- **(3) Parity vs. speed coupling.** f64-everywhere (tight bar) forces the slow `Dgemm` and worsens (2). Resolved by making NDCG the acceptance gate and the cosine bar loose with f32 GEMM (§7, §11).
- **(4) Maintenance surface.** ~1–2k LOC of numerically-sensitive transformer is a permanent cost vs potion's trivial pooling. Weigh in the M0 go/no-go, not just at merge time.
- **Numerical-parity debugging.** Even with a loose bar, expect a "layer 7 diverges" session. Mitigation: per-component unit tests (§11.4) localize drift; f64 for the cheap non-GEMM reductions.
- **Tensor naming / fc1 split / out_proj bias.** Resolved by dumping the checkpoint first (M1). `qkv_proj_bias=false` governs only the input projection — confirm `out_proj` bias from shapes. Do not code from assumptions.
- **Memory (~550 MB).** Fine for dev/Claude-Code use; for small MCP hosts, prioritize mmap (M8) or int8.
- **Upstream drift.** CodeRankEmbed is a frozen checkpoint (unlike semble's evolving rerank constants), so no ongoing-sync burden — a point in this approach's favor.

---

## 14. Decisions to record (ADRs)

- **ADR-0xx: M0 ceiling validation gates the build.** Record the measured NDCG@10 lift + recall@rerankN of real-CodeRankEmbed over ken's stage-1 shortlist as the decision input. The whole project is conditional on this number being worthwhile.
- **ADR-0xx: Pure-Go transformer reranker, no cgo.** Rejected alternatives: out-of-process sidecar (breaks single-static-binary identity), onnx-go/gomlx (stale op coverage / cgo-XLA fast path). Chose a hand-rolled forward pass. **Matmul backend (hand-rolled blocked f32 GEMM vs gonum) decided in M3 by benchmark, biased toward dependency-free hand-roll.**
- **ADR-0xx: NDCG is the acceptance gate; embedding cosine is a loose sanity bar.** Rationale: a reranker's product is ordering, not embedding fidelity; the tight-parity bar would force the slow f64 GEMM and break the latency budget. Deliberate departure from the embed package's 1e-5 contract.
- **ADR-0xx: Bi-encoder rerank with content-hash doc-side caching, decoupled from tombstones.** Query-independent doc embeddings make pure-Go viable; cache keyed by content hash (not coupled to `watch.go`); rerank is recall-bounded by stage-1, so `rerankN` is the tuning knob, not model choice.
- **ADR-0xx: Rerank is opt-in (`ModeHybridRerank`), default off, with an `isSymbolQuery` skip.** Preserves existing hybrid behavior and the zero-config first-launch story; transparent downgrade when no rerank model is present; symbol queries bypass the reranker entirely.
