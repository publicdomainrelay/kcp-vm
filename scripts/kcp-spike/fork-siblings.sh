#!/usr/bin/env bash
# M0-KCP-SPIKE route A: build and test sibling modules (deno-kcp, kcp-libs) on kcp's fork without editing them,
# through a scratch go.work carrying the same replace set.
# Usage: fork-siblings.sh <scratch-dir> [org-root]
set -uo pipefail
REPO=$(cd "$(dirname "$0")/../.." && pwd)
SCRATCH=${1:?scratch dir}
ORG=${2:-$(cd "$REPO/.." && pwd)}
mkdir -p "$SCRATCH"
{
  printf 'go 1.26.6\n\nuse (\n\t%s/deno-kcp\n\t%s/kcp-libs\n)\n\n' "$ORG" "$ORG"
  "$REPO/scripts/kcp-spike/vendor-kcp.sh" --print-replaces "$REPO/third_party"
} >"$SCRATCH/go.work"
for m in deno-kcp kcp-libs; do
  echo "== $m $(git -C "$ORG/$m" rev-parse --short HEAD)"
  (cd "$ORG/$m" && GOWORK="$SCRATCH/go.work" go build ./... && echo "build ok" && GOWORK="$SCRATCH/go.work" go test ./... -count=1 2>&1 | grep -E '^(ok|FAIL)' | awk '{print $1}' | sort | uniq -c)
done
