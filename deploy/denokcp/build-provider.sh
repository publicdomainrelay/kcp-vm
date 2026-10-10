#!/usr/bin/env bash
set -euo pipefail

# Builds the deno-kcp provider the vendored test bed runs, from the deno-kcp
# checkout at exactly the commit vendor.json records, so the manifests and the
# controller that reconciles them always come from the same tree.
#
#   build-provider.sh OUT
#
# DENO_KCP_DIR  the deno-kcp checkout (default $ORG_ROOT/deno-kcp)
# DENO_KCP_REF  build a different commit than the one recorded (drift is then
#               on you; the vendored-manifest test still compares the record)
# WORK_DIR      where the commit's tree is extracted (default a temp dir)
#
# The tree is extracted with git archive rather than checked out, so the
# deno-kcp working copy, its branch and its index are never touched. deno-kcp's
# go.mod replaces sibling modules by relative path (../kcp-libs); those are
# rewritten to the checkout's real siblings in the extracted copy only.

OUT=${1:?usage: build-provider.sh OUT}
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
ORG_ROOT=${ORG_ROOT:-$(cd "$REPO/.." && pwd)}
DENO_KCP_DIR=${DENO_KCP_DIR:-$ORG_ROOT/deno-kcp}
RECORDED=$(sed -n 's/^ *"commit": *"\([0-9a-f]*\)".*/\1/p' "$HERE/vendor.json" | head -1)
DENO_KCP_REF=${DENO_KCP_REF:-$RECORDED}

if [ -z "$DENO_KCP_REF" ]; then
  echo "no commit recorded in $HERE/vendor.json and DENO_KCP_REF unset" >&2
  exit 1
fi
if [ ! -f "$DENO_KCP_DIR/go.mod" ]; then
  echo "no deno-kcp checkout at $DENO_KCP_DIR; set DENO_KCP_DIR, or DENO_KCP_PROVIDER to a built provider" >&2
  exit 1
fi
if ! git -C "$DENO_KCP_DIR" cat-file -e "$DENO_KCP_REF^{commit}" 2>/dev/null; then
  echo "deno-kcp commit $DENO_KCP_REF is not in $DENO_KCP_DIR; fetch it (vendor.json names the ref)" >&2
  exit 1
fi

CLEANUP=""
if [ -z "${WORK_DIR:-}" ]; then
  WORK_DIR=$(mktemp -d)
  CLEANUP=$WORK_DIR
fi
trap '[ -n "$CLEANUP" ] && rm -rf "$CLEANUP"' EXIT

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"
git -C "$DENO_KCP_DIR" archive --format=tar "$DENO_KCP_REF" | tar -x -C "$WORK_DIR"

SIBLINGS=$(cd "$DENO_KCP_DIR/.." && pwd)
sed -i "s|=> \.\./|=> $SIBLINGS/|" "$WORK_DIR/go.mod"

mkdir -p "$(dirname "$OUT")"
(cd "$WORK_DIR" && go build -o "$OUT" ./cmd/deno-kcp-provider)
echo "built deno-kcp provider $DENO_KCP_REF -> $OUT"
