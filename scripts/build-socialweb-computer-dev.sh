#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 4 ]; then
  echo "usage: scripts/build-socialweb-computer.sh --dev [--platform P]... [--build-root DIR] [--publish DIR [--sync-only]]" >&2
  exit 2
fi
root="$1"
build_root="$2"
publish="$3"
sync_only="$4"
shift 4
platforms=("$@")

die() { echo "build-socialweb-computer --dev: $*" >&2; exit 1; }
say() { echo "build-socialweb-computer --dev: $(date -u +%Y-%m-%dT%H:%M:%SZ) $*" >&2; }
sha_of() { sha256sum "$1" | cut -d' ' -f1; }
started_s="$(date +%s.%N)"
elapsed() { awk -v a="$started_s" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1f", b - a }'; }

command -v rsync >/dev/null 2>&1 || die "rsync is required for --dev"
command -v flock >/dev/null 2>&1 || die "flock is required for --dev"

name="$(basename "$root")"
dev_root="$build_root/dev/$name"
versions="$root/internal/versions/versions.json"

case "$publish" in
  "") [ "$sync_only" = 0 ] || die "--sync-only needs --publish DIR" ;;
  /*) ;;
  *) die "--publish $publish is not an absolute path" ;;
esac

mirror() {
  local dst="$1/src" list
  mkdir -p "$dst" "$dev_root"
  list="$dev_root/mirror.include"
  git -C "$root" ls-files -z -co --exclude-standard \
    | tr '\0' '\n' \
    | awk '{ n = split($0, p, "/"); d = ""; for (i = 1; i < n; i++) { d = d "/" p[i]; if (!(d in seen)) { seen[d] = 1; print d "/" } } print "/" $0 }' \
    >"$list"
  rsync -a --delete --delete-excluded --include-from="$list" --exclude='*' "$root/" "$dst/"
  chmod -R a+rX "$1"
  say "mirrored $(grep -vc '/$' "$list") files of $root to $dst ($(elapsed)s)"
}

if [ "$sync_only" = 1 ]; then
  mirror "$publish"
  exit 0
fi

want_go="$(awk '$1=="toolchain"{print $2; exit}' "$root/go.mod")"
[ -n "$want_go" ] || want_go="go$(awk '$1=="go"{print $2; exit}' "$root/go.mod")"
cache="$build_root/cache"
mkdir -p "$cache/assets" "$cache/gomod" "$cache/gocache" "$build_root/tmp" "$dev_root"
export TMPDIR="$build_root/tmp"
export GOTMPDIR="$build_root/tmp"
export GOMODCACHE="$cache/gomod"
export GOCACHE="$cache/gocache"
export GOFLAGS=-mod=readonly
export CGO_ENABLED=0
unset GOOS GOARCH GOPATH GOROOT SOURCE_DATE_EPOCH
goroot="$(GOTOOLCHAIN="$want_go" go env GOROOT)" || die "cannot get the pinned toolchain $want_go"
export PATH="$goroot/bin:$PATH"
export GOTOOLCHAIN=local
have_go="$(go env GOVERSION)"
[ "$have_go" = "$want_go" ] || die "the toolchain at $goroot is $have_go; go.mod pins $want_go"

hostplat="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
if [ ${#platforms[@]} -eq 0 ]; then
  platforms=("$hostplat")
fi
for plat in "${platforms[@]}"; do
  [ "$(jq -r --arg p "$plat" '.platforms[$p].status // ""' "$versions")" = "release" ] || die "$plat is not a release platform of versions.json"
done
if [ -n "$publish" ] && [[ " ${platforms[*]} " != *" $hostplat "* ]]; then
  die "--publish copies the host binary; add --platform $hostplat"
fi
stage_plats=("${platforms[@]}")
[[ " ${stage_plats[*]} " == *" $hostplat "* ]] || stage_plats+=("$hostplat")

commit="$(git -C "$root" rev-parse HEAD)"
changed="$(git -C "$root" status --porcelain | grep -c . || true)"
dirty=false
[ "$changed" = 0 ] || dirty=true
say "worktree $root at $commit (dirty $dirty, $changed changed paths), platforms ${platforms[*]}, go $have_go"

assets_key="$(jq -cS '{assets, platforms}' "$versions" | sha256sum | cut -d' ' -f1)"
shared_blobs="$build_root/dev/assets/$assets_key"
own_blobs="$dev_root/blobs"
mkdir -p "$shared_blobs" "$own_blobs"
{
  flock 9
  for plat in "${stage_plats[@]}"; do
    key="${plat/\//_}"
    missing=()
    for asset in $(jq -r --arg p "$plat" '.platforms[$p].assets[]' "$versions"); do
      [ "$asset" = socialweb-vmexec ] && continue
      [ -f "$shared_blobs/$key/$asset.tar.zst" ] || missing+=(--asset "$asset")
    done
    if [ ${#missing[@]} -gt 0 ]; then
      say "stage ${missing[*]} for $plat into $shared_blobs (once per versions.json)"
      "$root/scripts/stage-assets.sh" --cache "$cache/assets" --blobs "$shared_blobs" --platform "$plat" "${missing[@]}"
    fi
  done
} 9>"$shared_blobs.lock"
for plat in "${platforms[@]}"; do
  if jq -e --arg p "$plat" '.platforms[$p].assets | index("socialweb-vmexec")' "$versions" >/dev/null; then
    "$root/scripts/stage-assets.sh" --cache "$cache/assets" --blobs "$own_blobs" --platform "$plat" \
      --asset socialweb-vmexec --print-pins >"$dev_root/vmexec-pins.json" 2>"$dev_root/vmexec-stage.log" \
      || die "staging socialweb-vmexec failed: $(tail -3 "$dev_root/vmexec-stage.log")"
    if grep -q 'versions.json pins' "$dev_root/vmexec-stage.log"; then
      say "warn: socialweb-vmexec of the working tree differs from its versions.json pin (a release build refuses it)"
    fi
  fi
done
say "assets staged ($(elapsed)s)"

hostkey="${hostplat/\//_}"
deno_blob="$shared_blobs/$hostkey/deno.tar.zst"
deno_dir="$dev_root/deno/$(sha_of "$deno_blob")"
if [ ! -x "$deno_dir/deno" ]; then
  rm -rf "$dev_root/deno"
  mkdir -p "$deno_dir"
  zstd -dc "$deno_blob" | tar -x -C "$deno_dir"
fi
[ -x "$deno_dir/deno" ] || die "the staged deno blob for $hostplat holds no deno"

(cd "$root" && go build -trimpath -o "$dev_root/bin/socialweb-tsbundle" ./cmd/socialweb-tsbundle)
ts_paths=(ts socialweb-computer-k8s deno.json deno.lock deploy/ts-entries.yaml)
ts_key="$(
  {
    sha_of "$dev_root/bin/socialweb-tsbundle"
    echo "$deno_dir"
    git -C "$root" ls-files -s -- "${ts_paths[@]}"
    git -C "$root" diff HEAD --binary -- "${ts_paths[@]}"
    (cd "$root" && git ls-files -o --exclude-standard -z -- "${ts_paths[@]}" | xargs -0 -r git hash-object --)
  } | sha256sum | cut -d' ' -f1
)"
if [ -f "$dev_root/closure/manifest.json.zst" ] && [ "$(cat "$dev_root/closure/key" 2>/dev/null)" = "$ts_key" ]; then
  say "closure tree $(jq -r .treeSha256 "$dev_root/tsbundle.json") reused: no TypeScript input changed ($(elapsed)s)"
else
  work="$dev_root/closure-work"
  for d in tree probe-bundle probe-state probe-home; do
    chmod -R u+w "$work/$d" 2>/dev/null || true
    rm -rf "${work:?}/$d"
  done
  mkdir -p "$work"
  rm -rf "$dev_root/closure"
  say "TypeScript closure of the working tree (pinned deno $hostplat, warm DENO_DIR in $work)"
  (cd "$root" && "$dev_root/bin/socialweb-tsbundle" -source-root "$root" -deno "$deno_dir/deno" -probe=false \
    -work "$work" -out "$dev_root/closure" >"$dev_root/tsbundle.json" 2>"$dev_root/tsbundle.log") \
    || die "the closure build failed: $(tail -5 "$dev_root/tsbundle.log")"
  echo "$ts_key" >"$dev_root/closure/key"
  say "closure tree $(jq -r .treeSha256 "$dev_root/tsbundle.json") ($(elapsed)s)"
fi

source_root="$root"
[ -z "$publish" ] || source_root="$publish/src"
stamp="github.com/johnandersen777/socialweb-computer-kcp/internal/buildstamp"
ldflags="-s -w -X $stamp.Kind=dev -X $stamp.SourceRoot=$source_root -X $stamp.Commit=$commit -X $stamp.Dirty=$dirty -X $stamp.ChangedPaths=$changed -X $stamp.BuiltUTC=$(date -u +%Y-%m-%dT%H:%M:%SZ)"

for plat in "${platforms[@]}"; do
  goos="${plat%/*}"
  goarch="${plat#*/}"
  key="${goos}_${goarch}"
  out="$dev_root/$key"
  mkdir -p "$out"
  overlay="$dev_root/overlay-$key.json"
  {
    for f in "$shared_blobs/$key"/*.tar.zst "$own_blobs/$key"/*.tar.zst; do
      [ -f "$f" ] || continue
      jq -n --arg k "$root/internal/assets/blobs/$key/$(basename "$f")" --arg v "$f" '{($k): $v}'
    done
    for f in closure.tar.zst manifest.json.zst; do
      jq -n --arg k "$root/internal/tsbundle/closure/$key/$f" --arg v "$dev_root/closure/$f" '{($k): $v}'
    done
  } | jq -s '{Replace: add}' >"$overlay"
  say "go build $plat"
  (cd "$root" && GOOS="$goos" GOARCH="$goarch" go build -tags release -trimpath -buildvcs=true -overlay "$overlay" \
    -ldflags="$ldflags" -o "$out/socialweb-computer.new" ./cmd/socialweb-computer)
  mv -f "$out/socialweb-computer.new" "$out/socialweb-computer"
  if [ "$plat" = "$hostplat" ]; then
    ln -sfn "$key/socialweb-computer" "$dev_root/socialweb-computer"
  fi
  say "built $out/socialweb-computer ($(elapsed)s)"
done

if [ -n "$publish" ]; then
  mkdir -p "$publish/bin"
  cp -f "$dev_root/$hostkey/socialweb-computer" "$publish/bin/socialweb-computer.new"
  mv -f "$publish/bin/socialweb-computer.new" "$publish/bin/socialweb-computer"
  mirror "$publish"
  say "published $publish/bin/socialweb-computer, TypeScript from $publish/src"
fi
if [[ " ${platforms[*]} " == *" $hostplat "* ]]; then
  "$dev_root/$hostkey/socialweb-computer" version | tee "$dev_root/$hostkey/version.txt"
fi
say "done in $(elapsed)s"
