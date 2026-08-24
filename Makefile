# ken — developer convenience Makefile.
#
# Thin wrappers around the canonical commands (also in CLAUDE.md /
# CONTRIBUTING.md). CI runs with the Go workspace disabled (GOWORK=off, the
# proxy-pinned dependency graph); to match it locally, prefix any target,
# e.g. `GOWORK=off make check`. See DEVELOPERS.md → "Local aikit development".

BINARIES := ken ken-mcp
# The single source of truth for gofmt scope — every top-level Go directory in
# ken/ (chunk moved to the aikit module per ADR-034). CI enforces this exact
# list via `make gofmt-check`, and CLAUDE.md / CONTRIBUTING.md point here rather
# than repeat it, so the four locations can't drift.
GOFMT_DIRS := cmd internal mcp bench demos tools

.DEFAULT_GOAL := help

.PHONY: help build test vet vet-bench fmt lint check hooks clean clean-bench clean-all

# golangci-lint is a REQUIRED CI job (see .github/workflows/ci.yml). Pin the
# same version here so `make lint` / `make check` mirror CI rather than silently
# skipping the linter — the gap that used to let a lint failure reach `main`.
# v2.13.0 is the first release with go 1.27 support (prebuilt binaries built
# with go 1.27); earlier versions' staticcheck crashes on the go 1.27 stdlib.
GOLANGCI_VERSION := v2.13.0

help: ## list targets
	@grep -hE '^[a-z][a-zA-Z0-9_-]*:.*##' $(MAKEFILE_LIST) \
	  | sort | awk -F':.*## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## build all packages + the ken / ken-mcp binaries into bin/
	go build ./...
	go build -o bin/ken ./cmd/ken
	go build -o bin/ken-mcp ./cmd/ken-mcp

test: ## run the full test suite
	go test ./...

vet: ## go vet ./...
	go vet ./...

# The bench harnesses are //go:build bench, so a plain `go vet ./...` skips
# them entirely — a compile error under bench/ can reach main unnoticed. This
# target compiles and vets them, and runs the cheap bench-tagged unit tests
# (the provenance helper + its schema mirror against bench/semble/run_ken.py).
# The heavy corpus-backed harnesses skip themselves when their corpora are
# absent, which is the case in CI.
vet-bench: ## go vet + cheap unit tests for the //go:build bench harnesses
	go vet -tags=bench ./bench/...
	go test -tags=bench ./bench/internal/... ./bench/tokens/ ./bench/chunkdiff/ ./bench/temporal/ -run 'TestSchema|TestCollect|TestInspectModel|TestRedact|TestKenEnv|TestDetect|TestCount|TestWriteRecords|TestKsLabel|TestAnalyze|TestContainedInSome|TestDefinitionSpans|TestTarget|TestPathMatches|TestApply|TestResolve|TestDisjoint'

fmt: ## format the tree in place (gofmt -w)
	gofmt -w $(GOFMT_DIRS)

gofmt-check: ## fail if any Go file under GOFMT_DIRS is unformatted (the CI gate)
	@out=$$(gofmt -l $(GOFMT_DIRS)); \
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: ## run golangci-lint (the exact linter CI requires, pinned $(GOLANGCI_VERSION))
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint not found — CI requires it (pinned $(GOLANGCI_VERSION))."; \
	  echo "install: https://golangci-lint.run/usage/install/  then re-run 'make lint'"; \
	  exit 1; }
	golangci-lint run ./...

check: gofmt-check lint vet-bench ## pre-push gate: gofmt-clean + golangci-lint + vet (incl. bench tag) + tests (run as `GOWORK=off make check` to mirror CI)
	go vet ./...
	go test ./...

hooks: ## install the repo git hooks (points core.hooksPath at scripts/git-hooks)
	git config core.hooksPath scripts/git-hooks
	@echo "git hooks installed: pre-push now runs 'GOWORK=off make check'."
	@echo "bypass a single push with 'git push --no-verify'."

clean: ## remove build products: the binaries, dist/, bin/, test scratch
	rm -f $(BINARIES)
	rm -rf dist bin
	rm -f *.test *.out *.err
	go clean ./...

clean-bench: ## remove heavy bench scratch (bench_out/ + bench results — can be tens of GB)
	rm -rf bench_out bench/semble/results bench/tokens/results bench/ndcg/results

clean-all: clean clean-bench ## everything regeneratable above (leaves per-machine models/fixtures + go.work intact)
