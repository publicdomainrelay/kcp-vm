#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_root="$HOME/.tmp/socialweb-computer-build"
rev="HEAD"
platforms=()
dev=0
verify=0
publish=""
sync_only=0

usage() {
  cat >&2 <<EOF
usage: $0 [--platform linux/amd64|linux/arm64]... [--commit REV] [--build-root DIR] [--verify-reproducible]
       $0 --dev [--platform P]... [--build-root DIR] [--publish DIR [--sync-only]]

--dev builds ONE socialweb-computer of the working tree of this checkout (uncommitted changes
included) for the host platform (or each --platform), for live runs while iterating. It reuses the
build root's go, asset and deno caches, skips the clean checkouts and the second build, and is not
reproducible: 'socialweb-computer version' says "dev build, not reproducible" with the commit and the
tree's dirty state. It embeds every pinned asset (staged once per versions.json under
dev/assets/<goos>_<goarch>/<asset>-<sha256 of its versions.json entry>; the assets built from
this repository (fetch kind go, such as socialweb-vmexec) are built from the working tree each time) and a closure
cmd/socialweb-tsbundle builds from the working tree (its deno.lock included, unpinned to a commit;
its entries are not started here, the live run starts them).
At run time the binary runs the TypeScript from the source root it was built with, live, and seeds
each DENO_DIR from that closure: 'socialweb-computer dev restart <instance> [<app>/]<denopod>' picks
up a TypeScript change without a rebuild. Output: dev/<worktree>/<goos>_<goarch>/socialweb-computer
(and dev/<worktree>/socialweb-computer for the host platform).

  --publish DIR  also copy the host binary to DIR/bin/socialweb-computer and mirror the working tree
                 (tracked and untracked files git does not ignore) to DIR/src, readable by every
                 account (chmod a+rX), and build the binary to run the TypeScript from DIR/src: an
                 account such as test-0001 that cannot read this checkout runs from there
  --sync-only    with --publish: only mirror the working tree to DIR/src again (no build), before a
                 dev restart of a DenoPod

Builds the release socialweb-computer binary (plan 0006 M4-BUILD) for each release platform of
internal/versions/versions.json, once, from a clean checkout of one commit, with the build root's
go build cache and pinned downloads. --verify-reproducible (plan 0006 ACC6-REPRO) builds the commit
twice, from two clean checkouts, and refuses to publish a platform whose two builds differ by one
byte. Each build stages every pinned asset with
scripts/stage-assets.sh, builds the TypeScript closure once with cmd/socialweb-tsbundle -release and
the pinned deno of the build host (the closure is source plus the deno cache, the same bytes for every
platform; its entries are started on the build host), embeds it for every platform, then runs
go build -tags release with the go.mod toolchain, CGO_ENABLED=0, -trimpath, -buildvcs=true,
GOPROXY=off.

  --platform    a release platform; repeat for more (default: every release platform)
  --commit      the commit to build (default HEAD of this checkout; uncommitted changes are not built)
  --build-root  checkouts, caches and outputs, all kept between builds (default \$HOME/.tmp/socialweb-computer-build)

Layout under the build root:
  src/<n>                       the clean checkout of build n (1; 2 with --verify-reproducible)
  cache/{assets,gomod,gocache}  pinned downloads, the module cache, the go build cache
  work/<n>/closure              the pinned deno, socialweb-tsbundle and the closure staging of build n
  out/<commit>/<n>/<goos>_<goarch>  build n: socialweb-computer, its sha256, build.json, version.json
  release/<commit>              the agreed binaries socialweb-computer-<goos>-<goarch>, SHA256SUMS,
                                reproducible.json and version-<goos>-<goarch>.{json,txt}

The binary and its sha256 are what socialweb-computer install and upgrade --binary --sha256 take.
CONTAINER=podman selects podman for the swc-userns-stage build (default docker).
EOF
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --platform) platforms+=("$2"); shift 2 ;;
    --commit) rev="$2"; shift 2 ;;
    --build-root) build_root="$2"; shift 2 ;;
    --dev) dev=1; shift ;;
    --verify-reproducible) verify=1; shift ;;
    --publish) publish="$2"; shift 2 ;;
    --sync-only) sync_only=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

die() { echo "build-socialweb-computer: $*" >&2; exit 1; }
say() { echo "build-socialweb-computer: $(date -u +%Y-%m-%dT%H:%M:%SZ) $*" >&2; }
utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

for tool in git go jq zstd tar sha256sum unshare; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required on the build host"
done

mkdir -p "$build_root"
build_root="$(cd "$build_root" && pwd -P)"
case "$build_root" in
  /tmp|/tmp/*) die "--build-root $build_root is under /tmp" ;;
esac

versions="$root/internal/versions/versions.json"

if [ "$dev" = 1 ]; then
  exec "$root/scripts/build-socialweb-computer-dev.sh" "$root" "$build_root" "$publish" "$sync_only" "${platforms[@]}"
fi
if [ -n "$publish" ] || [ "$sync_only" = 1 ]; then
  die "--publish and --sync-only go with --dev"
fi

if [ ${#platforms[@]} -eq 0 ]; then
  mapfile -t platforms < <(jq -r '.platforms | to_entries[] | select(.value.status == "release") | .key' "$versions")
fi
for plat in "${platforms[@]}"; do
  [ "$(jq -r --arg p "$plat" '.platforms[$p].status // ""' "$versions")" = "release" ] || die "$plat is not a release platform of versions.json"
done

commit="$(git -C "$root" rev-parse --verify "$rev^{commit}")" || die "no commit $rev"
if [ "$rev" = "HEAD" ] && [ -n "$(git -C "$root" status --porcelain --untracked-files=no)" ]; then
  say "warn: $root has uncommitted changes; only commit $commit is built"
fi

want_go="$(git -C "$root" show "$commit:go.mod" | awk '$1=="toolchain"{print $2; exit}')"
if [ -z "$want_go" ]; then
  want_go="go$(git -C "$root" show "$commit:go.mod" | awk '$1=="go"{print $2; exit}')"
fi
cache="$build_root/cache"
mkdir -p "$cache/assets" "$cache/gomod" "$cache/gocache" "$build_root/tmp"
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
export GOENV=off
have_go="$(go env GOVERSION)"
[ "$have_go" = "$want_go" ] || die "the toolchain at $goroot is $have_go; commit $commit pins $want_go"

checkout() {
  local tree="$1"
  if [ ! -d "$tree/.git" ]; then
    rm -rf "$tree"
    git clone --quiet --shared --no-checkout "$root" "$tree"
  fi
  git -C "$tree" checkout --quiet --force --detach "$commit"
  git -C "$tree" clean -ffdxq
  [ "$(git -C "$tree" rev-parse HEAD)" = "$commit" ] || die "$tree is not at $commit"
}

say "commit $commit, platforms ${platforms[*]}, go $have_go, build root $build_root"

checkout "$build_root/src/1"
say "module download into $GOMODCACHE"
(cd "$build_root/src/1" && GOPROXY="$(GOENV= go env GOPROXY)" go mod download)
for module in $(jq -r '[.assets[].fetch // {} | .[] | select(.kind == "go") | .module // empty] | unique[]' "$build_root/src/1/internal/versions/versions.json"); do
  say "module download for asset module $module"
  (cd "$build_root/src/1/$module" && GOPROXY="$(GOENV= go env GOPROXY)" go mod download)
done
export GOPROXY=off
export GOSUMDB=off

build_once() {
  local n="$1" tree="$build_root/src/$1" plat goos goarch key work out started deno
  local hostplat closure_work
  hostplat="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
  checkout "$tree"
  say "build $n: stage assets"
  local stage_args=()
  for plat in "${platforms[@]}"; do stage_args+=(--platform "$plat"); done
  (cd "$tree" && ./scripts/stage-assets.sh --cache "$cache/assets" "${stage_args[@]}")
  if [ ! -f "$tree/internal/assets/blobs/${hostplat/\//_}/deno.tar.zst" ]; then
    (cd "$tree" && ./scripts/stage-assets.sh --cache "$cache/assets" --platform "$hostplat" --asset deno)
  fi
  closure_work="$build_root/work/$n/closure"
  chmod -R u+w "$closure_work" 2>/dev/null || true
  rm -rf "$closure_work"
  mkdir -p "$closure_work/deno"
  zstd -dc "$tree/internal/assets/blobs/${hostplat/\//_}/deno.tar.zst" | tar -x -C "$closure_work/deno"
  deno="$closure_work/deno/deno"
  [ -x "$deno" ] || die "the staged deno blob for $hostplat holds no deno"
  (cd "$tree" && go build -trimpath -o "$closure_work/socialweb-tsbundle" ./cmd/socialweb-tsbundle)
  say "build $n: TypeScript closure (pinned deno $hostplat)"
  (cd "$tree" && "$closure_work/socialweb-tsbundle" -source-root "$tree" -release -deno "$deno" \
    -work "$closure_work/tsbundle" -out "$closure_work/out" >"$closure_work/tsbundle.json")
  for plat in "${platforms[@]}"; do
    goos="${plat%/*}"
    goarch="${plat#*/}"
    key="${goos}_${goarch}"
    out="$build_root/out/$commit/$n/$key"
    started="$(utc)"
    rm -rf "$out"
    mkdir -p "$out"
    cp "$closure_work/out/"* "$tree/internal/tsbundle/closure/$key/"
    say "build $n $plat: go build"
    (cd "$tree" && GOOS="$goos" GOARCH="$goarch" go build -tags release -trimpath -buildvcs=true \
      -ldflags='-s -w -buildid=' -o "$out/socialweb-computer" ./cmd/socialweb-computer)
    [ -z "$(git -C "$tree" status --porcelain)" ] || die "build $n left $tree dirty"
    (cd "$out" && sha256sum socialweb-computer >socialweb-computer.sha256)
    "$out/socialweb-computer" version --json >"$out/version.json"
    "$out/socialweb-computer" version >"$out/version.txt"
    jq -n --arg commit "$commit" --arg platform "$plat" --argjson build "$n" --arg go "$have_go" \
      --arg started "$started" --arg finished "$(utc)" --arg sha256 "$(sha_of "$out/socialweb-computer")" \
      --argjson closure "$(jq --arg p "$hostplat" '{treeSha256, builtAndStartedOn: $p, entries: [.entries[] | {name, ok}]}' "$closure_work/tsbundle.json")" \
      '{commit: $commit, platform: $platform, build: $build, go: $go, startedUtc: $started, finishedUtc: $finished, sha256: $sha256, typescriptClosure: $closure}' \
      >"$out/build.json"
    say "build $n $plat: sha256 $(sha_of "$out/socialweb-computer")"
  done
}

build_once 1
if [ "$verify" = 1 ]; then
  build_once 2
fi

release="$build_root/release/$commit"
rm -rf "$release"
mkdir -p "$release"
failed=0
records=()
for plat in "${platforms[@]}"; do
  key="${plat/\//_}"
  name="socialweb-computer-${plat/\//-}"
  a="$build_root/out/$commit/1/$key"
  sa="$(sha_of "$a/socialweb-computer")"
  if [ "$verify" = 0 ]; then
    cp "$a/socialweb-computer" "$release/$name"
    cp "$a/version.json" "$release/version-${plat/\//-}.json"
    cp "$a/version.txt" "$release/version-${plat/\//-}.txt"
    (cd "$release" && sha256sum "$name" >>SHA256SUMS)
    say "published $plat sha256 $sa -> $release/$name (one build; --verify-reproducible checks reproducibility)"
    continue
  fi
  b="$build_root/out/$commit/2/$key"
  sb="$(sha_of "$b/socialweb-computer")"
  records+=("$(jq -n --arg p "$plat" --arg sa "$sa" --arg sb "$sb" --slurpfile ja "$a/build.json" --slurpfile jb "$b/build.json" \
    '{platform: $p, identical: ($sa == $sb), sha256: (if $sa == $sb then $sa else null end), builds: [$ja[0], $jb[0]]}')")
  if [ "$sa" != "$sb" ]; then
    say "NOT REPRODUCIBLE $plat: build 1 $sa, build 2 $sb ($a, $b)"
    failed=1
    continue
  fi
  cp "$a/socialweb-computer" "$release/$name"
  cp "$a/version.json" "$release/version-${plat/\//-}.json"
  cp "$a/version.txt" "$release/version-${plat/\//-}.txt"
  (cd "$release" && sha256sum "$name" >>SHA256SUMS)
  say "reproducible $plat sha256 $sa -> $release/$name"
done
if [ "$verify" = 1 ]; then
  printf '%s\n' "${records[@]}" | jq -s --arg commit "$commit" '{commit: $commit, platforms: .}' >"$release/reproducible.json"
  cat "$release/reproducible.json"
fi
exit "$failed"
