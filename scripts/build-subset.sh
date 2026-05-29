#!/usr/bin/env bash
# Build the slim ken + ken-mcp binaries — only the grammars ken's treesitter
# chunker dispatches, the same build .goreleaser.yml ships. ADR-033.
# Usage: scripts/build-subset.sh [output-dir]   (default: bin/)
set -euo pipefail
cd "$(dirname "$0")/.."
tags="$(scripts/subset-tags.sh)"
out="${1:-bin}"
mkdir -p "$out"
echo "subset tags: $tags"
for cmd in ken ken-mcp; do
  CGO_ENABLED=0 go build -tags "$tags" -o "$out/$cmd" "./cmd/$cmd"
  echo "  built $out/$cmd ($(du -h "$out/$cmd" | cut -f1))"
done
