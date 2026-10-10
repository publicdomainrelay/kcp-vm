#!/bin/bash
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DOCKERFILE="$REPO/deploy/control-plane-container/Dockerfile"

IMAGE="${KCP_CONTROLPLANE_IMAGE:-socialweb-control-plane:local}"
NODEIMAGE_IMAGE="${KCP_NODEIMAGE_IMAGE:-socialweb-control-plane-nodeimage:local}"
KCP_IMAGE_DIR="${KCP_IMAGE_DIR:-/home/johnandersen777/kcp-image}"
BINARIES="${KCP_BINARIES_CONTEXT:-/var/tmp/socialweb-control-plane-binaries}"
TOOLCHAIN="${KCP_GO_TOOLCHAIN:-$(go env GOROOT)}"
MODCACHE="${KCP_GO_MODCACHE:-$(go env GOMODCACHE)}"
BUILDCACHE="${KCP_GO_BUILDCACHE:-$(go env GOCACHE)}"
EXPORT_DIR="${KCP_EXPORT_DIR:-}"
BOOT="${KCP_BOOT_CONTEXT:-/var/tmp/socialweb-control-plane-boot}"

target="${1:-controlplane}"
ORG_ROOT="$(cd "$REPO/.." && pwd)"
DOOR="$REPO/socialweb-computer-k8s"
K8S_TREE="${KCP_K8S_TREE_CONTEXT:-/var/tmp/socialweb-k8s-tree}"

stage_binaries() {
  install -d -m 0755 "$BINARIES"
  for name in k3s kine kcp kubectl bao; do
    path="$(command -v "$name" || true)"
    [ -n "$path" ] || { echo >&2 "no $name on PATH: this container carries the binaries the host has, and a missing one is a container that cannot run the control plane"; exit 1; }
    install -m 0755 "$path" "$BINARIES/$name"
    echo "$name <- $path"
  done
  BAO_VERSION="v2.6.2"
  BAO_SHA256="${KCP_BAO_SHA256:-8d18052337908a74f0d7dfacc8da7a1bff5f8a4ab6a2ad136fbf5ffeae243b00}"
  bao_version="$("$BINARIES/bao" version 2>/dev/null | head -1)"
  case "$bao_version" in
    *"$BAO_VERSION"*) ;;
    *) echo >&2 "bao reports '$bao_version', not the pinned $BAO_VERSION: the root vault's config, the tls_disable listener and the deprecated file backend are all measured on that build"; exit 1 ;;
  esac
  bao_sha="$(sha256sum "$BINARIES/bao" | cut -d' ' -f1)"
  [ "$bao_sha" = "$BAO_SHA256" ] || { echo >&2 "bao sha256 is $bao_sha, not the pinned $BAO_SHA256 (override with KCP_BAO_SHA256): the image must carry the released $BAO_VERSION build"; exit 1; }
  echo "bao         $BAO_VERSION pinned, sha256 $bao_sha"
}

stage_door_tree() {
  command -v deno >/dev/null || { echo >&2 "no deno on PATH: the door's source tree is found by asking deno what it imports"; exit 1; }
  command -v rsync >/dev/null || { echo >&2 "no rsync on PATH: the door's source tree is staged with it"; exit 1; }
  repos="$(cd "$DOOR" && deno info --json hono-socialweb-computer-k8s/mod.ts | deno eval --ext=ts '
    const root = new URL(Deno.args[0] + "/", "file:///").href;
    const j = JSON.parse(await new Response(Deno.stdin.readable).text());
    const s = new Set<string>();
    for (const m of j.modules) if (m.specifier?.startsWith(root)) s.add(m.specifier.slice(root.length).split("/")[0]);
    console.log([...s].sort().join(" "));' "$ORG_ROOT")"
  [ -n "$repos" ] || { echo >&2 "deno reported no local imports for the door: refusing to build an image with no source"; exit 1; }
  install -d -m 0755 "$K8S_TREE"
  for repo in $repos; do
    if [ "$repo" = "$(basename "$REPO")" ]; then
      install -d -m 0755 "$K8S_TREE/$repo"
      rsync -a --delete --exclude node_modules --exclude web/downloads "$DOOR/" "$K8S_TREE/$repo/socialweb-computer-k8s/"
    else
      rsync -a --delete --exclude .git --exclude node_modules --exclude .claude "$ORG_ROOT/$repo/" "$K8S_TREE/$repo/"
    fi
    echo "door tree   $repo"
  done
  for stale in "$K8S_TREE"/*; do
    case " $repos " in *" $(basename "$stale") "*) ;; *) rm -rf "$stale" ;; esac
  done
}

if [ "$target" = "k8s" ]; then
  for dir in "$TOOLCHAIN" "$MODCACHE" "$BUILDCACHE"; do
    [ -d "$dir" ] || { echo >&2 "no $dir on this host: this build reads it as a named context and cannot invent it"; exit 1; }
  done
  stage_binaries
  stage_door_tree
  docker buildx build \
    --build-context "goroot=$TOOLCHAIN" \
    --build-context "gomod=$MODCACHE" \
    --build-context "gocache=$BUILDCACHE" \
    --build-context "binaries=$BINARIES" \
    --build-context "k8stree=$K8S_TREE" \
    --file "$DOCKERFILE" \
    --target k8s --tag "${KCP_K8S_IMAGE:-socialweb-k8s:local}" --load "$REPO"
  echo "built ${KCP_K8S_IMAGE:-socialweb-k8s:local}"
  exit 0
fi

for dir in "$TOOLCHAIN" "$MODCACHE" "$BUILDCACHE" "$KCP_IMAGE_DIR"; do
  [ -d "$dir" ] || { echo >&2 "no $dir on this host: this build reads it as a named context and cannot invent it"; exit 1; }
done

release="$(cat /proc/sys/kernel/osrelease)"
install -d -m 0755 "$BOOT"
for file in "vmlinuz-$release" "config-$release"; do
  [ -r "/boot/$file" ] || { echo >&2 "no /boot/$file on this host: the guest boots this host's own kernel and the config k3s's check-config reads, and neither can be invented"; exit 1; }
  install -m 0644 "/boot/$file" "$BOOT/$file"
done
echo "boot        $BOOT (vmlinuz-$release, config-$release: /boot as a context cannot be sent, it holds /boot/efi at mode 0700 and the CLI walks the whole tree)"

stage_binaries


echo "toolchain   $TOOLCHAIN"
echo "modulecache $MODCACHE"
echo "buildcache  $BUILDCACHE"
echo "kcp-image   $KCP_IMAGE_DIR"
echo "binaries    $BINARIES"
echo "target      $target"

build() {
  docker buildx build \
    --build-context "goroot=$TOOLCHAIN" \
    --build-context "gomod=$MODCACHE" \
    --build-context "gocache=$BUILDCACHE" \
    --build-context "kcpimage=$KCP_IMAGE_DIR" \
    --build-context "artifacts=$KCP_IMAGE_DIR/artifacts" \
    --build-context "bootdir=$BOOT" \
    --build-context "modulesdir=/lib/modules" \
    --build-context "binaries=$BINARIES" \
    --file "$DOCKERFILE" \
    "$@"
}

case "$target" in
  controlplane)
    build --target controlplane --tag "$IMAGE" --load "$REPO"
    echo "built $IMAGE"
    ;;
  computer)
    build --target computer --tag "$IMAGE" --load "$REPO"
    echo "built $IMAGE"
    ;;
  nodeimage)
    build --target nodeimage --progress=plain "$REPO"
    ;;
  nodeimage-image)
    build --target nodeimage --tag "$NODEIMAGE_IMAGE" --load "$REPO"
    echo "built $NODEIMAGE_IMAGE"
    ;;
  nodeimage-qemu)
    build --target nodeimage-qemu --progress=plain "$REPO"
    ;;
  nodeimages)
    build --target extract --progress=plain "$REPO"
    ;;
  extract)
    : "${EXPORT_DIR:?set KCP_EXPORT_DIR to the image store the extracted artifact lands in}"
    install -d -m 0755 "$EXPORT_DIR"
    build --target extract --progress=plain --output "type=local,dest=$EXPORT_DIR" "$REPO"
    echo "extracted into $EXPORT_DIR:"
    ls -la "$EXPORT_DIR"
    ;;
  *)
    echo >&2 "unknown target $target: controlplane, computer, nodeimage, nodeimage-image, nodeimage-qemu, nodeimages or extract"
    exit 1
    ;;
esac
