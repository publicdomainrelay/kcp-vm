#!/usr/bin/env bash
# M0-KCP-SPIKE route A: build, vet and test this module on kcp's Kubernetes fork; record per-package breakage.
# Prereq: scripts/kcp-spike/vendor-kcp.sh has populated third_party/ and go.mod carries its replace block.
# Usage: route-a-build.sh <outdir>
set -uo pipefail
REPO=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${1:?outdir}
mkdir -p "$OUT"
cd "$REPO"
t=$(date +%s)
go build ./... >"$OUT/build.log" 2>&1; echo "build exit=$? secs=$(( $(date +%s) - t ))" >"$OUT/summary.txt"
t=$(date +%s)
go vet ./... >"$OUT/vet.log" 2>&1; echo "vet exit=$? secs=$(( $(date +%s) - t ))" >>"$OUT/summary.txt"
for os in darwin windows; do
  GOOS=$os go build ./... >"$OUT/build-$os.log" 2>&1; echo "build GOOS=$os exit=$?" >>"$OUT/summary.txt"
done
t=$(date +%s)
go test ./... -count=1 -json >"$OUT/test.json" 2>"$OUT/test.stderr"; echo "test exit=$? secs=$(( $(date +%s) - t ))" >>"$OUT/summary.txt"
for c in socialweb-gateway socialweb-clusterprovider socialweb-kcp-spike; do
  go build -o "$OUT/bin/$c" "./cmd/$c" >>"$OUT/bin.log" 2>&1 && echo "size $c $(stat -c %s "$OUT/bin/$c")" >>"$OUT/summary.txt"
done
cat "$OUT/summary.txt"
