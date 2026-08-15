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
GOFMT_DIRS := cmd internal mcp bench demos

.DEFAULT_GOAL := help

.PHONY: help build test vet fmt check clean clean-bench clean-all

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

fmt: ## format the tree in place (gofmt -w)
	gofmt -w $(GOFMT_DIRS)

gofmt-check: ## fail if any Go file under GOFMT_DIRS is unformatted (the CI gate)
	@out=$$(gofmt -l $(GOFMT_DIRS)); \
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: gofmt-check ## pre-push gate: gofmt-clean + vet + tests (run as `GOWORK=off make check` to mirror CI)
	go vet ./...
	go test ./...

clean: ## remove build products: the binaries, dist/, bin/, test scratch
	rm -f $(BINARIES)
	rm -rf dist bin
	rm -f *.test *.out *.err
	go clean ./...

clean-bench: ## remove heavy bench scratch (bench_out/ + bench results — can be tens of GB)
	rm -rf bench_out bench/semble/results bench/tokens/results

clean-all: clean clean-bench ## everything regeneratable above (leaves per-machine models/fixtures + go.work intact)
