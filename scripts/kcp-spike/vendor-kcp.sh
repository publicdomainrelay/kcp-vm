#!/usr/bin/env bash
# M0-KCP-SPIKE route A: re-vendor kcp v0.33.0 (with staging/) and embeddedetcd into third_party/,
# then print the replace block that repeats kcp's go.mod replace set for this module.
# third_party/ is not committed on the spike branch; run this script to reproduce it.
# Usage: vendor-kcp.sh [--print-replaces]
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)
KCP_TAG=${KCP_TAG:-v0.33.0}
KCP_COMMIT=${KCP_COMMIT:-1a4cb725de077a081365b2ce40bb3e5e8006efa3}
EMBEDDEDETCD_VERSION=${EMBEDDEDETCD_VERSION:-v1.1.1-0.20260402110232-2cc5c5cce35e}
TP="$REPO/third_party"

print_replaces() {
  local prefix=${1:-./third_party}
  echo "replace ("
  awk '/^replace \($/{f=1;next} f&&/^\)$/{f=0} f&&/k8s.io\//{print}' "$TP/kcp/go.mod"
  echo ")"
  echo
  echo "replace ("
  echo "	github.com/kcp-dev/kcp => $prefix/kcp"
  for m in apimachinery/v2:apimachinery client-go:client-go code-generator/v3:code-generator sdk:sdk virtual-workspace-framework:virtual-workspace-framework; do
    echo "	github.com/kcp-dev/${m%%:*} => $prefix/kcp/staging/src/github.com/kcp-dev/${m##*:}"
  done
  echo "	github.com/kcp-dev/embeddedetcd => $prefix/embeddedetcd"
  echo ")"
}

if [ "${1:-}" = "--print-replaces" ]; then print_replaces "${2:-./third_party}"; exit 0; fi

mkdir -p "$TP"
if [ ! -f "$TP/kcp/go.mod" ]; then
  rm -rf "$TP/kcp"
  git clone --quiet --depth 1 --branch "$KCP_TAG" https://github.com/kcp-dev/kcp "$TP/kcp"
  got=$(git -C "$TP/kcp" rev-parse HEAD)
  [ "$got" = "$KCP_COMMIT" ] || { echo "kcp $KCP_TAG is $got, expected $KCP_COMMIT" >&2; exit 1; }
  rm -rf "$TP/kcp/.git"
fi

if [ ! -f "$TP/embeddedetcd/go.mod" ]; then
  dir=$(cd "$REPO" && GOFLAGS=-mod=mod go mod download -json "github.com/kcp-dev/embeddedetcd@$EMBEDDEDETCD_VERSION" | sed -n 's/^\s*"Dir": "\(.*\)",$/\1/p')
  rm -rf "$TP/embeddedetcd"
  cp -r "$dir" "$TP/embeddedetcd"
  chmod -R u+w "$TP/embeddedetcd"
fi

echo "kcp: $(du -sh "$TP/kcp" | cut -f1) $(find "$TP/kcp" -type f | wc -l) files"
echo "embeddedetcd: $(du -sh "$TP/embeddedetcd" | cut -f1) $(find "$TP/embeddedetcd" -type f | wc -l) files"
