#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
versions="$root/internal/versions/versions.json"

usage() {
  cat >&2 <<EOF
usage: $0 --target <hypervisor>-<arch> [--out DIR] [--work DIR] [--jobs N] [--resolve-kernel-config]

Builds one guest image (vmlinux, rootfs.ext4, image.json) from the pinned inputs in
internal/versions/versions.json .guestImages, on any Linux host with docker or podman, curl,
xz, jq, sha256sum and a Go toolchain. Nothing runs as root and nothing is mounted: the kernel
is compiled and the ext4 is laid down (mke2fs -d <tar>) inside the pinned builder image with
no network, as the calling uid.

  --target   firecracker-x86_64 | firecracker-aarch64 (hyperv-x86_64: plan 0011, vz-arm64: plan 0012)
  --out      where <target>/{vmlinux,rootfs.ext4,image.json} land (default WORK/out)
  --work     downloads, build trees and the composed tar (default \$GUEST_IMAGE_WORK or
             \$HOME/.tmp/guest-image-build; never /tmp, the build trees are several GB)
  --jobs     kernel make -j (default nproc)
  --resolve-kernel-config
             regenerate deploy/guest-kernel/<target>.config from firecracker's pinned configs and
             deploy/guest-kernel/socialweb.config, then stop

CONTAINER=podman selects podman (default docker). When versions.json records outputs for the
target, the build fails unless every output's sha256 equals its pin.
EOF
  exit 2
}

target=""
out=""
work="${GUEST_IMAGE_WORK:-$HOME/.tmp/guest-image-build}"
jobs="$(nproc)"
resolve=0
while [ $# -gt 0 ]; do
  case "$1" in
    --target) target="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --work) work="$2"; shift 2 ;;
    --jobs) jobs="$2"; shift 2 ;;
    --resolve-kernel-config) resolve=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[ -n "$target" ] || usage

die() { echo "build-guest-image: $*" >&2; exit 1; }
say() { echo "build-guest-image: $*" >&2; }

for tool in curl xz jq sha256sum go tar; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required on the build host"
done
container="${CONTAINER:-docker}"
command -v "$container" >/dev/null 2>&1 || die "$container is required on the build host (CONTAINER=podman to use podman)"

case "$work" in
  /tmp|/tmp/*) die "--work $work is under /tmp, which is tmpfs on build hosts: a kernel tree and a rootfs spill into swap" ;;
esac

pin() { jq -r "$1" "$versions"; }
t=".guestImages.targets[\"$target\"]"
if [ "$(pin "$t")" = "null" ]; then
  reserved="$(pin ".guestImages.reserved[\"$target\"] // empty")"
  [ -n "$reserved" ] && die "target $target is reserved for $reserved and has no inputs yet"
  die "unknown target $target; known: $(pin '.guestImages.targets | keys | join(", ")')"
fi
hypervisor="$(pin "$t.hypervisor")"
arch="$(pin "$t.arch")"
platform="$(pin "$t.platform")"
sde="$(pin "$t.sourceDateEpoch")"
kconfig="$root/$(pin "$t.kernelConfig")"
kver="$(pin '.guestImages.kernel.version')"
[ "$hypervisor" = "firecracker" ] || die "$target: only firecracker images are built here"
case "$arch" in
  x86_64) karch=x86_64; cross=""; kmake=vmlinux; kimage=vmlinux ;;
  aarch64) karch=arm64; cross=aarch64-linux-gnu-; kmake=Image; kimage=arch/arm64/boot/Image ;;
  *) die "$target: no kernel recipe for arch $arch" ;;
esac

[ -n "$out" ] || out="$work/out"
cache="$work/cache"
tw="$work/$target"
mkdir -p "$cache" "$tw" "$out/$target"
export TMPDIR="$work/tmp"
mkdir -p "$TMPDIR"

sha_of() { sha256sum "$1" | cut -d' ' -f1; }

fetch_pinned() {
  local url="$1" want="$2" file="$cache/$2"
  if [ -f "$file" ] && [ "$(sha_of "$file")" = "$want" ]; then
    echo "$file"
    return
  fi
  say "fetching $url"
  curl -fsSL --retry 3 -o "$file.part" "$url" || die "download failed: $url"
  local got
  got="$(sha_of "$file.part")"
  if [ "$got" != "$want" ]; then
    rm -f "$file.part"
    die "sha256 mismatch for $url: got $got, pinned $want"
  fi
  mv "$file.part" "$file"
  echo "$file"
}

dockerfile="$root/$(pin '.guestImages.builder.dockerfile')"
builder="localhost/socialweb-guest-builder:sha-$(sha_of "$dockerfile" | cut -c1-12)"
if ! "$container" image inspect "$builder" >/dev/null 2>&1; then
  say "building the builder image $builder"
  "$container" build -q -t "$builder" -f "$dockerfile" "$(dirname "$dockerfile")" >&2
fi

in_builder() {
  "$container" run --rm --network none --user "$(id -u):$(id -g)" --security-opt label=disable \
    -e HOME=/build -e SOURCE_DATE_EPOCH="$sde" "$@"
}

ksrc="$(fetch_pinned "$(pin '.guestImages.kernel.source.url')" "$(pin '.guestImages.kernel.source.sha256')")"

kbuild_env=(
  -e KBUILD_BUILD_TIMESTAMP="$(date -u -d "@$sde" '+%a %b %e %H:%M:%S UTC %Y')"
  -e KBUILD_BUILD_USER=socialweb -e KBUILD_BUILD_HOST=guest-image -e KBUILD_BUILD_VERSION=1
  -e ARCH="$karch" -e CROSS_COMPILE="$cross" -e KVER="$kver" -e JOBS="$jobs"
)

if [ "$resolve" -eq 1 ]; then
  fc="$(fetch_pinned "$(pin ".guestImages.kernel.config[\"$target\"].url")" "$(pin ".guestImages.kernel.config[\"$target\"].sha256")")"
  ci="$(fetch_pinned "$(pin '.guestImages.kernel.config.ci.url')" "$(pin '.guestImages.kernel.config.ci.sha256')")"
  rdir="$tw/resolve"
  rm -rf "$rdir"
  mkdir -p "$rdir"
  cat "$fc" "$ci" "$root/deploy/guest-kernel/socialweb.config" >"$rdir/merged.config"
  in_builder "${kbuild_env[@]}" -v "$ksrc:/in/linux.tar.xz:ro" -v "$rdir:/build" "$builder" sh -euc '
    mkdir /build/src && tar -xJf /in/linux.tar.xz -C /build/src
    cd /build/src/linux-$KVER
    cp /build/merged.config .config
    make -s olddefconfig
    cp .config /build/resolved.config'
  missing=""
  while IFS= read -r line; do
    case "$line" in
      CONFIG_*=n) want="# ${line%=n} is not set" ;;
      CONFIG_*) want="$line" ;;
      *) continue ;;
    esac
    grep -qxF "$want" "$rdir/resolved.config" || missing="$missing $line"
  done <"$root/deploy/guest-kernel/socialweb.config"
  cp "$rdir/resolved.config" "$kconfig"
  rm -rf "$rdir"
  say "wrote $kconfig"
  [ -z "$missing" ] || say "socialweb.config lines olddefconfig did not keep for $target:$missing"
  exit 0
fi

[ -f "$kconfig" ] || die "$kconfig is missing; run with --resolve-kernel-config"

say "$target: kernel $kver"
kdir="$tw/kernel"
rm -rf "$kdir"
mkdir -p "$kdir/out"
cp "$kconfig" "$kdir/config"
in_builder "${kbuild_env[@]}" -e KMAKE="$kmake" -e KIMAGE="$kimage" \
  -v "$ksrc:/in/linux.tar.xz:ro" -v "$kdir:/build" "$builder" sh -euc '
  mkdir /build/src && tar -xJf /in/linux.tar.xz -C /build/src
  cd /build/src/linux-$KVER
  cp /build/config .config
  make -s olddefconfig
  if ! cmp -s .config /build/config; then
    diff -u /build/config .config | head -40 >&2
    echo "the pinned kernel config is not a resolved config: run --resolve-kernel-config" >&2
    exit 1
  fi
  make -s -j"$JOBS" "$KMAKE"
  cp "$KIMAGE" /build/out/vmlinux'
mv "$kdir/out/vmlinux" "$out/$target/vmlinux"
rm -rf "$kdir"

say "$target: rootfs"
base_xz="$(fetch_pinned "$(pin "$t.base.url")" "$(pin "$t.base.sha256")")"
base_tar="$cache/$(pin "$t.base.sha256").tar"
if [ ! -s "$base_tar" ]; then
  xz -dc "$base_xz" >"$base_tar.part"
  mv "$base_tar.part" "$base_tar"
fi
dp_url="$(pin ".assets.dumbpipe.fetch[\"$platform\"].url")"
dp_sha="$(pin ".assets.dumbpipe.fetch[\"$platform\"].sha256")"
dp_member="$(pin ".assets.dumbpipe.fetch[\"$platform\"].members | to_entries[] | select(.value == \"dumbpipe\") | .key")"
dp_tgz="$(fetch_pinned "$dp_url" "$dp_sha")"
mkdir -p "$tw/assets"
tar -xzf "$dp_tgz" -C "$tw/assets" "$dp_member"
mv "$tw/assets/$dp_member" "$tw/assets/dumbpipe.bin"

nodeimage=(go run -C "$root" ./cmd/socialweb-nodeimage)
"${nodeimage[@]}" -guest-rootfs -build "$root/deploy/guest-image/rootfs.json" -base "$base_tar" \
  -asset "dumbpipe=$tw/assets/dumbpipe.bin" -source-date-epoch "$sde" \
  -out "$tw/rootfs.tar" -report "$tw/rootfs-report.json"
rm -rf "$tw/assets"

tar_sha="$(jq -r .tarSha256 "$tw/rootfs-report.json")"
data_bytes="$(jq -r .dataBytes "$tw/rootfs-report.json")"
size_mib=$(( (data_bytes * 11 / 10 / 1048576 + 320 + 63) / 64 * 64 ))
uuid="${tar_sha:0:8}-${tar_sha:8:4}-${tar_sha:12:4}-${tar_sha:16:4}-${tar_sha:20:12}"
mkdir -p "$tw/fs"
in_builder -e FS_UUID="$uuid" -e SIZE_MIB="$size_mib" -e E2FSPROGS_FAKE_TIME="$sde" \
  -v "$tw:/build" "$builder" sh -euc '
  rm -f /build/fs/rootfs.ext4
  mke2fs -q -F -t ext4 -b 4096 -I 256 -L cloudimg-rootfs -U "$FS_UUID" \
    -E hash_seed="$FS_UUID",root_owner=0:0,lazy_itable_init=0,lazy_journal_init=0 \
    -d /build/rootfs.tar /build/fs/rootfs.ext4 "${SIZE_MIB}M"
  e2fsck -fn /build/fs/rootfs.ext4 >/dev/null
  for key in $(debugfs -R "ls /etc/ssh" /build/fs/rootfs.ext4 2>/dev/null); do
    case "$key" in ssh_host_*) echo "the rootfs carries /etc/ssh/$key" >&2; exit 1 ;; esac
  done'
mv "$tw/fs/rootfs.ext4" "$out/$target/rootfs.ext4"
rm -rf "$tw/fs" "$tw/rootfs.tar"

"${nodeimage[@]}" -guest-manifest -target "$target" -dir "$out/$target" -report "$tw/rootfs-report.json"
rm -f "$tw/rootfs-report.json"

recorded="$(pin "$t.outputs | length")"
for f in vmlinux rootfs.ext4 image.json; do
  got="$(sha_of "$out/$target/$f")"
  echo "$target $f $got $(stat -c %s "$out/$target/$f")"
  if [ "$recorded" != "0" ]; then
    want="$(pin "$t.outputs[\"$f\"].sha256")"
    [ "$got" = "$want" ] || die "$target $f is $got and versions.json pins $want: the build is not reproducing its pinned output"
  fi
done
