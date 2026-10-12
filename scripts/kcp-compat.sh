#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <from-tag> <to-tag>   (kcp release tags, e.g. v0.33.0 v0.33.1)" >&2
  exit 2
fi
from="$1"
to="$2"
repo="${KCP_REPO:-https://github.com/kcp-dev/kcp}"
work="$(mktemp -d "${TMPDIR:-$HOME/.tmp}/kcp-compat.XXXXXX")"
trap 'rm -rf "$work"' EXIT

listing() {
  local tag="$1" dir="$work/$1"
  git clone -q --depth 1 --branch "$tag" --filter=blob:none --sparse "$repo" "$dir" 2>/dev/null
  git -C "$dir" sparse-checkout set config >/dev/null 2>&1
  local d
  for d in crds system-crds root-phase0 root shard; do
    [ -d "$dir/config/$d" ] || continue
    find "$dir/config/$d" -maxdepth 1 -name '*.yaml' -print0 | sort -z | while IFS= read -r -d '' f; do
      yq -N 'select(.kind == "CustomResourceDefinition") | .metadata.name as $n | .spec.versions[] | "crd " + $n + " " + .name + " served=" + (.served | tostring) + " storage=" + (.storage | tostring)' "$f" 2>/dev/null || true
      yq -N -o=json -I=0 'select(.kind == "APIResourceSchema")' "$f" 2>/dev/null | while IFS= read -r doc; do
        [ -n "$doc" ] || continue
        name="$(printf '%s' "$doc" | yq -p=json -N '.metadata.name')"
        sum="$(printf '%s' "$doc" | yq -p=json -o=json -I=0 '.spec' | sha256sum | cut -d' ' -f1)"
        echo "schema $d $name $sum"
      done || true
      yq -N 'select(.kind == "APIExport") | "export " + .metadata.name + " " + ((.spec.resources // []) | map(.schema) | sort | join(","))' "$f" 2>/dev/null || true
    done
  done | grep -v '^$' | sort -u
}

listing "$from" >"$work/from.txt"
listing "$to" >"$work/to.txt"
if [ ! -s "$work/from.txt" ] || [ ! -s "$work/to.txt" ]; then
  echo "kcp-compat: an empty listing for $from or $to; the kcp source layout changed and this script must follow it" >&2
  exit 1
fi
diff -u --label "$from" --label "$to" "$work/from.txt" "$work/to.txt" >"$work/diff.txt" || true
compatible=true
if grep -q '^[-+]crd \|^[-+]schema \|^[-+]export ' "$work/diff.txt"; then
  compatible=false
fi
sum="$(sha256sum "$work/diff.txt" | cut -d' ' -f1)"
if [ -n "${KCP_COMPAT_DIFF:-}" ]; then
  cp "$work/diff.txt" "$KCP_COMPAT_DIFF"
fi
printf '{"from": "%s", "to": "%s", "storageCompatible": %s, "diffSHA256": "%s"}\n' "$from" "$to" "$compatible" "$sum"
