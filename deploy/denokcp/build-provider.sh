#!/usr/bin/env bash
set -euo pipefail

# Builds the deno-kcp provider the vendored market network runs, from the deno-kcp
# checkout at exactly the commit vendor.json records, against kcp-libs at the
# commit vendor.json records with this directory's kcp-libs-*.patch applied, so
# the manifests and the controller that reconciles them always come from one
# known tree.
#
#   build-provider.sh OUT
#
# DENO_KCP_DIR   the deno-kcp checkout (default a deno-kcp directory beside this repository)
# KCP_LIBS_DIR   the kcp-libs checkout (default the deno-kcp checkout's sibling)
# DENO_KCP_REF   build a different deno-kcp commit than the one recorded
# KCP_LIBS_REF   build against a different kcp-libs commit than the one recorded
# WORK_DIR       where the trees are extracted (default a temp dir)
#
# Both trees are extracted with git archive rather than checked out, so neither
# working copy, branch or index is ever touched. deno-kcp's go.mod replaces
# kcp-libs by relative path (../kcp-libs); in the extracted copy it is pointed
# at the extracted, patched kcp-libs instead.
#
# kcp-libs-no-default-issuer.patch: OpenBao 2.6 and 2.7 answer GET
# <mount>/cert/ca on an empty PKI mount with HTTP 400 "no default issuer
# currently configured", not 404, and kcp-libs f6911d2 reads only 404 as "no
# authority yet", so the provider never generates the root or any intermediate
# and no workload is ever issued a certificate. Drop the patch once kcp-libs
# carries the fix.

OUT=${1:?usage: build-provider.sh OUT}
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
DENO_KCP_DIR=${DENO_KCP_DIR:-$(cd "$REPO/.." && pwd)/deno-kcp}
KCP_LIBS_DIR=${KCP_LIBS_DIR:-$(cd "$DENO_KCP_DIR/.." && pwd)/kcp-libs}

recorded() {
  sed -n "s/^ *\"$1\": *\"\([0-9a-f]*\)\".*/\1/p" "$HERE/vendor.json" | head -1
}
DENO_KCP_REF=${DENO_KCP_REF:-$(recorded commit)}
KCP_LIBS_REF=${KCP_LIBS_REF:-$(recorded kcpLibsCommit)}

for pair in "deno-kcp:$DENO_KCP_DIR:$DENO_KCP_REF" "kcp-libs:$KCP_LIBS_DIR:$KCP_LIBS_REF"; do
  IFS=: read -r name dir ref <<<"$pair"
  if [ -z "$ref" ]; then
    echo "no $name commit recorded in $HERE/vendor.json" >&2
    exit 1
  fi
  if [ ! -f "$dir/go.mod" ]; then
    echo "no $name checkout at $dir; set DENO_KCP_DIR / KCP_LIBS_DIR, or DENO_KCP_PROVIDER to a built provider" >&2
    exit 1
  fi
  if ! git -C "$dir" cat-file -e "$ref^{commit}" 2>/dev/null; then
    echo "$name commit $ref is not in $dir; fetch it" >&2
    exit 1
  fi
done

CLEANUP=""
if [ -z "${WORK_DIR:-}" ]; then
  WORK_DIR=$(mktemp -d)
  CLEANUP=$WORK_DIR
fi
trap '[ -n "$CLEANUP" ] && rm -rf "$CLEANUP"' EXIT

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR/deno-kcp" "$WORK_DIR/kcp-libs"
git -C "$DENO_KCP_DIR" archive --format=tar "$DENO_KCP_REF" | tar -x -C "$WORK_DIR/deno-kcp"
git -C "$KCP_LIBS_DIR" archive --format=tar "$KCP_LIBS_REF" | tar -x -C "$WORK_DIR/kcp-libs"
for p in "$HERE"/kcp-libs-*.patch; do
  [ -e "$p" ] || continue
  patch -s -p1 -d "$WORK_DIR/kcp-libs" <"$p"
  echo "applied $(basename "$p") to kcp-libs $KCP_LIBS_REF"
done

SIBLINGS=$(cd "$DENO_KCP_DIR/.." && pwd)
sed -i -e "s|=> \.\./kcp-libs\$|=> $WORK_DIR/kcp-libs|" -e "s|=> \.\./|=> $SIBLINGS/|" "$WORK_DIR/deno-kcp/go.mod"

mkdir -p "$(dirname "$OUT")"
(cd "$WORK_DIR/deno-kcp" && go build -o "$OUT" ./cmd/deno-kcp-provider)
echo "built deno-kcp provider $DENO_KCP_REF (kcp-libs $KCP_LIBS_REF) -> $OUT"
