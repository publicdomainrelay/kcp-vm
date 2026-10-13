#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
  cat >&2 <<EOF
usage: $0 --out DIR

Builds the pinned downloads the socialweb-computer-k8s web app serves at /downloads/<sha256>/<name>
(the socialweb-computer-k8s-downloads asset of internal/versions/versions.json) into DIR:
  socialweb-meshd-x86_64, socialweb-meshd-aarch64       rust/build.sh (static musl)
  socialweb-credential-<os>-<arch>                      cmd/socialweb-credential, for linux and darwin, amd64 and arm64
each with a sha256sum-format <name>.sha256 sidecar. Every file is rebuilt from this checkout.
EOF
  exit 2
}

out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[ -n "$out" ] || usage
case "$out" in
  /tmp|/tmp/*) echo "build-web-app-downloads: --out $out is under /tmp" >&2; exit 1 ;;
esac
command -v cargo >/dev/null 2>&1 || { echo "build-web-app-downloads: cargo is required to build socialweb-meshd" >&2; exit 1; }
mkdir -p "$out"
out="$(cd "$out" && pwd -P)"

pids=()
MESHD_DIST="$out" "$root/rust/build.sh" x86_64 aarch64 >&2 &
pids+=("$!")
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  (
    name="socialweb-credential-${target%/*}-${target#*/}"
    cd "$root"
    CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build -trimpath -buildvcs=false \
      -ldflags='-s -w -buildid=' -o "$out/$name" ./cmd/socialweb-credential
    chmod 0755 "$out/$name"
    cd "$out" && sha256sum "$name" >"$name.sha256"
  ) &
  pids+=("$!")
done
for _ in "${pids[@]}"; do
  if ! wait -n; then
    for p in "${pids[@]}"; do
      pkill -TERM -P "$p" 2>/dev/null || true
      kill -TERM "$p" 2>/dev/null || true
    done
    wait || true
    echo "build-web-app-downloads: a build failed" >&2
    exit 1
  fi
done

(cd "$out" && sha256sum -c --quiet ./*.sha256) || { echo "build-web-app-downloads: a download does not match its sidecar" >&2; exit 1; }
for f in "$out"/*.sha256; do
  echo "build-web-app-downloads: $(cat "$f")" >&2
done
