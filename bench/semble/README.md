# bench/semble/

`run_ken.py` benchmarks **ken against semble on equal footing**: it imports
semble's own corpus loaders and NDCG metric directly, then scores ken's
retrieval with the identical harness semble scores itself with.

## Why this stays Python — permanently, by design

This is an **intentional exception** to ken's "tooling that decides something is
Go" rule (the `tools/` + `internal/devtools/` migration), not an
unconverted leftover. The entire point is **bit-for-bit reuse of semble's
reference implementation**: reimplementing semble's NDCG/loaders in Go would
risk a subtle mismatch that quietly invalidates every ken-vs-semble comparison
this produces. The external reference is the source of truth, so the benchmark
must call it, not re-derive it.

Same category as aikit's `scripts/oracle/` (which reuses the upstream Python
Model2Vec/tokenizer reference for parity testing). Please do **not** "port this
to Go" — the Python dependency is the feature.

The corpus-fetch bench scripts under `scripts/` (`bench_coir.py` etc.) are
Python for the adjacent reason: they pull standard benchmark corpora via
`huggingface_hub` + `pyarrow`.
