#!/usr/bin/env bash
# Print the space-separated gotreesitter grammar_subset build tags for ken's
# slim build. Single source of truth is .goreleaser.yml's `tags:` list (the
# release builds use it directly); this script lets local builds + the CI
# compile-smoke reuse the exact same set without a second copy. ADR-033.
set -euo pipefail
cd "$(dirname "$0")/.."
# Only the YAML list items (`  - grammar_subset...`), never prose mentions
# of "grammar_subset" in the surrounding comment block.
grep -oE '^[[:space:]]*-[[:space:]]*grammar_subset[a-z_]*' .goreleaser.yml \
  | grep -oE 'grammar_subset[a-z_]*' | sort -u | paste -sd' ' -
