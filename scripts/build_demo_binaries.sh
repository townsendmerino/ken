#!/usr/bin/env bash
# build_demo_binaries.sh — cross-compile every demo binary for every
# target platform after the per-demo index.bin + model/ are in place.
# Used to prep a demos/<version> release.
#
# Run from ken repo root:
#   scripts/build_demo_binaries.sh [output_dir]
#
# output_dir defaults to /tmp/ken-demo-binaries-<date>. Produces:
#   ken-demo-<name>-<goos>-<goarch>.tar.gz  per (demo × platform)
#   SHA256SUMS                              one file across all archives
#
# Platforms match demos/v0.1.0: darwin/{arm64,amd64}, linux/{amd64,arm64}.
# CGO disabled (matches the existing demo builds' static-binary contract).

set -euo pipefail

DEMOS=(go-stdlib kubernetes postgres)
TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/amd64"
  "linux/arm64"
)

OUT="${1:-/tmp/ken-demo-binaries-$(date +%Y%m%d)}"
mkdir -p "$OUT"

echo "build_demo_binaries.sh — output: $OUT"
echo "demos: ${DEMOS[*]}"
echo "targets: ${TARGETS[*]}"
echo

# Sanity: every demo needs index.bin + model/ in place before this runs.
for demo in "${DEMOS[@]}"; do
  for needed in "demos/$demo/index.bin" "demos/$demo/model/model.safetensors"; do
    if [[ ! -e "$needed" ]]; then
      echo "ERROR: missing $needed — re-run ken build-index + copy model/ first" >&2
      exit 1
    fi
  done
done

for demo in "${DEMOS[@]}"; do
  for target in "${TARGETS[@]}"; do
    goos="${target%%/*}"
    goarch="${target##*/}"
    binname="ken-demo-$demo-$goos-$goarch"
    archivename="$binname.tar.gz"
    binary_in_archive="ken-demo-$demo"

    echo "-> $binname"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -tags=kendemo -o "$OUT/$binary_in_archive" "./demos/$demo"
    # Pack into a tar.gz under a clean unprefixed name so extraction
    # gives the user `ken-demo-<demo>` to drop on $PATH.
    tar -C "$OUT" -czf "$OUT/$archivename" "$binary_in_archive"
    rm "$OUT/$binary_in_archive"

    size=$(stat -f%z "$OUT/$archivename" 2>/dev/null || stat -c%s "$OUT/$archivename")
    printf "   %-50s %d bytes\n" "$archivename" "$size"
  done
done

echo
echo "Computing SHA256SUMS..."
(cd "$OUT" && shasum -a 256 *.tar.gz > SHA256SUMS)
cat "$OUT/SHA256SUMS"

echo
echo "Done. Upload contents of $OUT to the GitHub release."
