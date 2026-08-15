# scripts/

Developer tooling that lives outside the built binaries. Nothing here ships or
is imported by `ken` / `ken-mcp`; the Go files are all `//go:build ignore`
(run with `go run scripts/<name>.go`), the rest are Python/shell utilities.

Three buckets — **reproducible drivers** you may want to re-run, **dogfood /
build tools** wired into everyday workflows, and **historical diagnostics**
that fed investigations now closed (kept for provenance, not for re-running).

## Reproducible bench & perf drivers

Regenerate the numbers in [`docs/BENCH.md`](../docs/BENCH.md) and
[`docs/PERF-expectations.md`](../docs/PERF-expectations.md). The NDCG family
downloads/materializes a corpus once, then feeds `bench/ndcg`.

| Script | Purpose |
|---|---|
| `bench_coir.py` | Fetch CoIR-CSN-Python and materialize it on disk |
| `bench_csn_nl.py` | Derive an NL→code retrieval benchmark from the CoIR materials |
| `bench_csn_nl_stripped.py` | Leak-free (Phase A) variant of `csn-python-nl` |
| `bench_cosqa_heur.py` / `bench_csn_nl_stripped_heur.py` | Apply the Arm B heuristic enrichment to the bench corpus |
| `cosqa_to_bench.py` | Convert the CoSQA dev set into ken's bench format |
| `materialize_heur.go` | Write an Arm-B-enriched variant corpus for the heuristic bench |
| `merge_m0d.py` / `plot_token_budget.py` | Merge per-query CSVs / plot the token-budget results |
| `perf_collect.sh` · `perf_startup_m2.sh` · `rss_bench.sh` · `kernel_demo_bench.sh` | Startup-latency / resident-memory / kernel-scale perf drivers |
| `adversarial.txt` | Adversarial query set (data, consumed by the bench harness) |

## Dogfood & build tools

Everyday operational tooling; some is wired into CI/release.

| Script | Purpose |
|---|---|
| `subset-tags.sh` | Print the `grammar_subset` slim-build tags — **used by CI + `.goreleaser.yml`** (ADR-033); single source is the goreleaser `tags:` list |
| `build-subset.sh` · `build_demo_binaries.sh` · `build-docs-mcp.sh` | Slim/demo/docs binary build drivers |
| `regen_golden.sh` | Regenerate `testdata/golden.json` (idempotent; wraps `pin_inference.py`) |
| `gen_third_party_licenses.py` | Regenerate `THIRD_PARTY_LICENSES.md` |
| `dogfood_languages.go` · `dogfood_structural.go` | Run `structural.Build` over real cloned repos to surface extractor crashes / empty-Name bugs |
| `armb_drift_diff.go` | Arm B label-level drift gate (Stage 8) |
| `precision_sample_edges.go` | Precision edge-sampling (Stage 8 Gate 2) |
| `stdlib_demo_vet.go` · `stdlib_phase1_close.go` | Vet / close the stdlib-demo semantic-bridging phases |

## Historical diagnostics (investigations resolved)

Kept for provenance. The investigations they fed are closed elsewhere; you
should not need to re-run these.

| Script | Investigation (outcome) |
|---|---|
| `csharp_bisect.go` · `csharp_oom_diag.go` · `csharp_pprof.go` | C# gotreesitter grammar OOM — **resolved**: C# shipped once gotreesitter v0.20.2 fixed the blowup |
| `dart_survey.go` | Dart per-repo clean-parse survey — **resolved**: Dart shipped |
| `swift_survey.go` | Swift per-repo clean-parse survey — Swift **parked** (not shipped) |
| `probe_rust_empty_names.go` · `probe_rust_field_name.go` | Rust extractor `function_item` field-name probes — **resolved** |
| `maxsim_probe.go` | ColBERT/MaxSim late-interaction reranking probe — experiment |
| `m0_hyde.py` | HyDE snippet generation for the M0 ceiling experiment — closed |
| `perf_startup_m0.go` · `phase0_memory_probe.go` | Cold-start M0 / Phase-0 latency + `structural.Index` resident-memory baselines — superseded by the shipped cold-start campaign (ADR-039) |

> `__pycache__/` is Python bytecode cache and should not be committed —
> `.gitignore` it if it reappears.
