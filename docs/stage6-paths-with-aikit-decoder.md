# Stage 6 paths now that aikit ships a decoder

Design analysis, no code. What aikit v0.2.0's `decoder` + `tokenizer`
+ GGUF support unlock for ken, ordered by likelihood-of-paying-off
× cheapness-to-try. Companion to
[`colbert-late-interaction-for-ken.md`](colbert-late-interaction-for-ken.md):
that doc analyzes the late-interaction lever (cheap probe ran
negative, expensive path gated on a code-specific MaxSim-trained
encoder); this doc analyzes the generative-LLM lever (cheap probe
hasn't been run; the foundation just landed in tree).

## The one insight that frames everything

Stage 6 was listed as deferred in [DESIGN.md §10](DESIGN.md#10-risk-register)
with three implementation options (wazero + llama.wasm, Hugot's ONNX
backend, or a hand-rolled transformer forward pass) and a single
hard constraint: **pure Go, no cgo, single static binary.** The
constraint ruled out the obvious paths (llama.cpp via cgo, Python
sidecar via gRPC) and made the project genuinely hard.

**Aikit v0.2.0 ships the hand-rolled transformer forward pass as a
finished product.** Pure Go, no cgo, HF-parity-validated across
Gemma 3 · Qwen3 · Qwen2.5 · Llama-2/3 · Mistral · GPT-2 · Mixtral
(sparse-MoE). Streaming `Generate`, `KVCache`, `Sampler` (temp /
top-k / top-p), the `Backend` seam (CPU + WebGPU), and a `GGUF`
loader for the laptop-friendly Q4_K_M quant that needs no sidecar
config. The single-static-binary contract is preserved: a GGUF lives
inside an `//go:embed corpus` exactly the way ken's pre-built
indices do today.

So: **the gate the trigger in §10 was waiting for ("a feasible Option
C") is no longer the gate.** What's left is product judgement about
*what* generative work to do, not platform engineering about *how*
to host the model.

## Where Stage 6 stood before this

The trigger in §10 reads: "Adoption of a Stage-5 reranker creates a
need for higher-level query understanding." Stage 5 (the
`CodeRankEmbed` neural reranker, M0–M11) shipped in v0.9.0. So the
trigger is *eligible* but hasn't *fired* — we haven't yet seen
production usage stack up enough query-understanding failure modes
to make the lift obviously worth paying. That keeps the doc-level
recommendation a "considered, not committed" rather than "next thing
to ship."

The companion memory entry, [project-rerank-cold-start-mitigations](https://github.com/townsendmerino/ken/blob/main/CLAUDE.md),
captures the closest live concrete failure mode: HyDE-style query
expansion was considered during Stage-7a M0 as a way to lower
cold-rerank-cache cost. That's the cheapest place to put a small
generative model to work, and it's the natural Stage 6 entry point.

## Update: aikit v0.3.0 (2026-06-03)

v0.3.0 doesn't change which paths are feasible — it specifically
strengthens **Path A**'s model menu and resolves **Path B**'s biggest
open question. The framing above (the "Three concrete paths" menu,
the trigger logic, the §10 deferred-item gate) is unchanged.

What v0.3.0 added that matters here:

- **Mellum2 end-to-end from a bare GGUF** — JetBrains'
  code-pretrained model, validated argmax + cosine vs its f32 oracle.
  Path A's open question #1 ("which model do we ship with?") cited
  generic Qwen2.5-0.5B / Gemma-2B class as the candidate set under
  the "small generators tolerate it" assumption. Mellum2 is a
  *code-trained* generator, which is materially better matched to a
  job whose entire purpose is "surface plausible identifier
  vocabulary in the project's domain." Same binary-size budget, much
  higher quality ceiling for the cheap-probe NDCG read.
- **Full quant ladder** (Q2_K / Q3_K / Q4_K_M / Q5_K / Q6_K + IQ2_S /
  IQ3_S / IQ4_NL / IQ4_XS, plus GPTQ + AWQ safetensors-resident
  int4). Path A's open question #2 ("Q8_0 / Q4_0 / Q4_K_M") had three
  options; the answer-space is now a continuum. Same Path A bench
  becomes a quant-sweep instead of a binary pick.
- **Parallel per-layer load** (Mellum2-12B Q4_K_M: ~2 min → ~20 s)
  plus the mmap'd GGUF reader. Open question #4 ("acceptable cold
  latency") gets meaningfully looser — the M2 LazyDecoder pattern
  now amortizes a 20 s cold cost rather than a 2 min one.
- **`constrain` package — structured / constrained decoding.** Path
  B's NEW work list named "JSON output shape decision (mirror the
  v0.9.0 JSON output mode)" as a real lift; that was code to parse
  free-form output and gracefully fail on malformed JSON. v0.3.0
  guarantees the model *cannot* emit malformed JSON. The cost of
  Path B's MCP-tool surface drops accordingly.

What v0.3.0 did NOT change:

- The §10 Stage-6 trigger ("Stage 5 reranker creates a need for
  higher-level query understanding") has not fired. Stage 6 stays
  parked post-1.0 per [road-to-1.0.md](road-to-1.0.md).
- The Path A cheap-probe recommendation is unchanged. If the probe
  is run, Mellum2 + Q4_K_M is now the obvious starting model — but
  the doc-level recommendation remains "only build production wiring
  if the bench moves," exactly as written below.
- The HyDE-on-rerank-on **kill verdict** from M0a
  ([outputs/m0-hyde-results.md](../outputs/m0-hyde-results.md))
  predates the v0.3.0 model menu. A code-trained generator could in
  principle re-open that probe; that's a *separate* probe with a
  *separate* trigger, not a 1.0 item.

The rest of this doc reads as-was — open questions #3 (binary-size
budget) and #5 (telemetry shape) are independent of the v0.3.0 delta
and still apply if Path A is taken up.

## Three concrete paths, ordered

Each path below names: what it would do, what aikit's new bits
deliver, what's NEW work, and what the trigger to start it would be.

### Path A — HyDE query expansion (cheapest, narrowest, most aligned)

**What it does.** Before retrieval, run the natural-language query
through a small LLM to generate a *hypothetical document* — a short
synthetic snippet of what the answer might look like in code, in the
project's vocabulary. Embed the hypothetical, fuse with the
dense-query embedding (mean, weighted, or as a second RRF list), and
let the existing hybrid pipeline retrieve. The expansion adds
identifier-level vocabulary the bare NL query was missing, which is
exactly what `internal/search/hybrid.go`'s α-routing already wants.

**Why this first.**
1. **Smallest behaviour change.** No new tool, no new MCP surface,
   no on-disk format. The fused query vector slots into
   `internal/search/index.go`'s existing `SearchWithQVecPredicted`
   path (already in tree from Stage 7a).
2. **Existing measurement harness.** Stage 7a's M0 bench infra
   already exists and was used to discuss HyDE specifically (see the
   `project-rerank-cold-start-mitigations` memory entry). The
   "did this help retrieval?" question reduces to running that
   harness with the LLM expansion path turned on.
3. **Tolerates small models.** HyDE works with surprisingly weak
   generators — the expansion just needs to surface plausible
   identifier vocabulary, not produce correct code. A Q4_K_M
   Qwen2.5-0.5B or Gemma-2B class model would likely suffice and
   keeps inference latency reasonable.

**What aikit v0.2.0 delivers for this.**
- `decoder.Model.Generate` with streaming + sampler — produces the
  hypothetical document text.
- `tokenizer.Tokenizer` — encodes the prompt and decodes the output
  bit-for-bit-identical to HF for every supported family.
- `GGUF` Q4_K_M loader (validated 0.9975 cosine vs the f32 oracle
  on TinyLlama) — keeps the model small enough to embed in a
  release binary.

**What's NEW work in ken.**
- Prompt template for the hypothetical-document generation (probably
  a one-line system prompt + the query, output capped at ~80
  tokens — short enough to keep latency low).
- Stage 7a's bench harness re-pointed at the real HyDE path
  (currently exercises an oracle baseline). The bench already exists
  in `bench/ndcg/`; the materializer is new code, the harness isn't.
- A `KEN_HYDE=on` env var + an MCP option to turn it on per-request,
  same shape as `KEN_ENRICH=off`.
- One careful decision: which model do we ship with? See "Open
  questions" below.

**Risks.**
- **Per-query latency.** Even a 0.5B model at Q4_K_M takes
  meaningful wall-clock to generate ~80 tokens. The Stage-5 rerank
  warm cache buys back ~65× the cold-rerank wall time
  ([baseline](../internal/search/neural_rerank_bench_test.go) on
  M1 Pro: 7.16 s cold → 110 ms warm); a 200–500 ms LLM expansion on
  top is acceptable for the cold path but a noticeable regression
  for the warm path. The right framing is probably "HyDE only fires
  when bm25 + dense returns weak signals" (low-confidence retrieval
  branch), not "every query."
- **Drift between the generator's vocabulary and the repo's.** A
  model that hallucinates `getUserById` for a repo that uses
  `find_user_by_id` actively hurts retrieval. The HyDE-research
  consensus is that this drift is real but small; the safer
  variants augment HyDE with a small number of BM25-found tokens
  from the bare query before generating, which keeps the expansion
  anchored to the actual corpus vocabulary.

**Trigger to start.** A measurable HyDE-on vs HyDE-off NDCG win on
ken's existing bench harness — run the cheap probe first, only
build out the production wiring (env vars, model packaging,
binary-size budget decisions) if the bench moves.

### Path B — Agentic query refinement (medium lift, broader product surface)

**What it does.** Expose a new MCP tool — `refine_query(text)` or
similar — that takes a vague NL question and returns a
ranked-by-confidence list of crisper queries to try against `search`.
The model gets the bare query plus a small amount of corpus context
(e.g., the top symbols from `symbols()`, recently-touched files
from `recently_changed()`, the file tree shape) and produces "you
probably mean one of these" alternatives. Agent clients call
`refine_query` when their own `search` results look weak, then
re-issue with the refined query.

**Why this second.**
The user-visible value is potentially larger than HyDE (agents
have a concrete, common pain point of "what should I search for?"
that this addresses), but the lift is bigger too: it's a new MCP
tool with its own response schema, its own model-loading lifecycle,
and a more complex prompt template that needs to consume the
project's own structural index. It's also more model-quality-
sensitive than HyDE — the output has to be coherent identifier-
shaped queries, not just plausible vocabulary.

**What aikit v0.2.0 delivers for this.**
- Same `decoder` + `tokenizer` + `GGUF` story as Path A, plus
  `decoder.Generate`'s streaming API is well-suited to
  agent-facing tools that may want progressive output.

**What's NEW work in ken.**
- An MCP tool definition (response schema with confidence ranks,
  per-alternative reasoning, etc.).
- A prompt template that pipes structural index context into the
  model's context window without blowing the budget. Probably a
  separate "context builder" component that picks
  the most relevant K identifiers from `symbols()` for the query.
- A model-loading lifecycle (probably the lazy-load pattern from
  M2 — `LazyDecoder` mirroring `LazyReranker` — so the tool only
  pays the load cost on first call).
- JSON output shape decision (mirror the v0.9.0 JSON output mode
  for the other tools).
- A budget for "how big can the model be before the binary becomes
  obnoxious?" — see "Open questions."

**Risks.**
- **Latency.** Agents are interactive; multi-second tool latencies
  are noticed. Cap output tokens hard and consider a smaller model
  than Path A's HyDE generator might tolerate.
- **Hallucinated identifiers.** Worse failure mode than HyDE
  because the agent then literally searches for them. Should
  cross-check the model's output against the corpus's actual
  symbols (`structural.Symbols()`) and discard any that don't exist.
- **Model selection drift.** Different families (Qwen / Gemma /
  Llama) produce qualitatively different refinement output; the
  choice locks in a vibe.

**Trigger to start.** Either (a) Path A ships and demonstrates
useful generative output on real corpora, OR (b) explicit feedback
from agent users that they're stuck on "what to search for"
patterns.

### Path C — `ken summarize <path>` and friends (broadest, biggest lift)

**What it does.** New MCP tools for the user-visible surfaces that
Stage 6 originally named: `summarize(path)` to produce a 2–3
sentence summary of a function/class/file; per-directory
"executive summaries" for orientation; possibly `explain(symbol)`
that takes a symbol name and produces prose context using
`outline` + `references` + `callers` results.

**Why this third.**
Highest product surface area, highest lift, highest model-quality
ceiling. None of the existing benchmarks (NDCG, structural-gate
precision) measure summarization quality, so we'd be optimizing
against vibes until we built a new eval. Also the model size needed
for credible summaries is larger than what HyDE or refinement
tolerates — likely Llama-3.1-8B-Instruct-class, which at Q4_K_M is
still ~5 GB and changes ken's binary-size story materially.

**What's NEW work in ken.**
- New MCP tools (probably 2–3), each with response schema + JSON
  output mode.
- A summarization eval (lots of judgment calls about what "good
  summary" means).
- A bigger binary, OR a "model lives on disk separately, ken-mcp
  loads it lazily" path that breaks the single-static-binary story.
- A licensing question — Llama-3.1 has terms that may or may not
  match how ken is distributed.

**Trigger to start.** Specific user demand. Without it, this is the
"because we can" path, which is exactly the kind of work to defer
past 1.0.

## What this means operationally

- **Single-static-binary preserved for Paths A and B.** A small
  GGUF (TinyLlama / Qwen-0.5B / Gemma-2B class) at Q4_K_M is
  small enough (~600 MB – ~1.5 GB) to embed in a release binary
  the way pre-built indices already do. The slim-binary contract
  (ADR-033) is unaffected — grammar subset tags are orthogonal.
  Path C's larger model probably breaks this contract.
- **Goreleaser config probably needs a `genmodel_embedded` build
  tag** if we want both "no LLM" and "small LLM included" release
  artifacts — Paths A/B don't *require* this, but it's the obvious
  way to let operators who don't want generative features avoid
  paying the binary size.
- **Lazy model load is mandatory.** The M2 [`LazyReranker`](../internal/search/lazy_reranker.go)
  pattern applies directly — generative model load is several
  hundred MB read off disk; we already deferred it once.
- **No new external dependencies.** `aikit/decoder` and
  `aikit/tokenizer` use only what aikit already pulls (golang.org/x/text,
  etc.). No `wazero`, no `onnxruntime` cgo.

## Open questions

These are the decisions that have to be made before Path A can ship,
in roughly the order they'd come up:

1. **Which model do we ship with?** The HF parity matrix in aikit's
   tested list (Gemma 3 · Qwen3 · Qwen2.5 · Llama-2/3 · Mistral ·
   GPT-2 · Mixtral) gives us a menu, but each has different size /
   license / quality tradeoffs. The HyDE literature suggests Qwen
   or Gemma-class models in the 0.5–3B range work well. The
   licensing constraint (no Llama-3+ for shipping in a static
   binary, unless we're OK with the Llama community license) is a
   real gate.
2. **Quantization choice.** aikit ships Q8_0, Q4_0, and Q4_K_M
   (0.99996 / 0.9944 / 0.9975 cosine on TinyLlama). Q4_K_M is the
   measured sweet spot. Q8_0 is the safe choice if Path A turns out
   to be quality-sensitive. Q4_0 is unlikely to be worth it given
   Q4_K_M's better quality/size ratio.
3. **Binary size budget.** ken-mcp is currently ~38 MB
   (slim build). Adding a 1.5 GB GGUF blows this to ~1.5 GB —
   technically fine for embedded distribution, but it's a different
   product than v0.9.0. Two artifacts (`ken-mcp` and
   `ken-mcp-generative`) may be the right answer.
4. **Latency budget.** What's the acceptable cold latency for
   "search with HyDE" vs "search without"? A 500 ms regression on
   warm queries is probably unacceptable; a 1 s regression on cold
   queries probably is. The lazy-load + gate-on-low-confidence
   pattern likely lands inside these bounds, but only the M0 bench
   tells us for sure.
5. **Telemetry.** Generative output is qualitatively different
   from retrieval output; the existing `*Telemetry` shape probably
   needs a `GenerationTime`, `GenerationTokens`, and (for HyDE)
   the actual hypothetical-document text so users can audit when
   the model went off the rails.

## What this is NOT

- **NOT a path to a Stage-5 reranker replacement.** The
  CodeRankEmbed neural reranker is the right model for what it does
  (encode + cosine over candidate docs); a generative LLM is the
  wrong tool for re-ranking and would regress measured NDCG. Stage
  5 stays as shipped.
- **NOT a ColBERT/late-interaction replacement.** See
  [`colbert-late-interaction-for-ken.md`](colbert-late-interaction-for-ken.md)
  for that analysis — it parks on a separate trigger (a
  code-specific MaxSim-trained encoder).
- **NOT a Python sidecar.** The pure-Go constraint stays. If
  someone wants Python-side inference, that's an out-of-process
  integration story that doesn't touch ken.

## Bottom line

Stage 6's blocker (Option C feasibility) just resolved. The cheapest
exploratory probe is Path A (HyDE) using the Stage 7a bench harness
that already exists. Two well-scoped decisions gate it (model
choice + binary-size budget); both are product decisions, not
engineering ones. If the HyDE probe doesn't move NDCG, that result
is itself useful — it tells us small generative models on top of
the current retrieval stack don't earn their keep on code-search
queries, and Stage 6 stays parked until something else (user
demand for summarization, an agentic-refinement use case) provides
the trigger.

If you do nothing, Stage 6 remains correctly parked and
[DESIGN.md §10's trigger](DESIGN.md#10-risk-register) keeps watch.
The only thing the v0.2.0 bump changed is that the right answer is
no longer "we'd have to port a transformer first."

---

Sources & references:
- aikit v0.2.0 CHANGELOG and release notes
- [`docs/colbert-late-interaction-for-ken.md`](colbert-late-interaction-for-ken.md) (companion late-interaction analysis)
- [`docs/DESIGN.md` §10](DESIGN.md#10-risk-register) (Stage 6 deferred-item entry)
- `project-rerank-cold-start-mitigations` memory entry (HyDE as cold-cache mitigation)
- Original HyDE paper: [Gao et al., "Precise Zero-Shot Dense Retrieval without Relevance Labels" (arXiv 2212.10496)](https://arxiv.org/abs/2212.10496)
