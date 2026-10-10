#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dist="${MESHD_DIST:-$here/dist}"
export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$HOME/.cache/socialweb-meshd-target}"
export TMPDIR="${TMPDIR:-$HOME/.cache/scratch-tmp}"
mkdir -p "$TMPDIR" "$dist"

usage() {
  echo "usage: $0 [x86_64] [aarch64]" >&2
  echo "  builds static musl socialweb-meshd binaries into $dist" >&2
  echo "  writes socialweb-meshd-<arch> and socialweb-meshd-<arch>.sha256 (sha256sum format)" >&2
  echo "  env: MESHD_DIST (output dir), MESHD_CC (C compiler for ring, default clang)," >&2
  echo "       CARGO_TARGET_DIR (default ~/.cache/socialweb-meshd-target)" >&2
  exit 2
}

arches=("$@")
if [ "${#arches[@]}" -eq 0 ]; then
  arches=(x86_64 aarch64)
fi
for arch in "${arches[@]}"; do
  case "$arch" in
    x86_64|aarch64) ;;
    *) usage ;;
  esac
done

cc="${MESHD_CC:-clang}"
command -v "$cc" >/dev/null || { echo "build.sh: $cc not found; ring's C and assembly need a compiler that takes --target" >&2; exit 1; }
command -v ar >/dev/null || { echo "build.sh: ar not found" >&2; exit 1; }
sysroot="$(rustc --print sysroot)"
host="$(rustc -vV | sed -n 's/^host: //p')"
lld="$sysroot/lib/rustlib/$host/bin/rust-lld"
[ -x "$lld" ] || { echo "build.sh: $lld not found" >&2; exit 1; }

registry="${CARGO_HOME:-$HOME/.cargo}/registry/src"
export RUSTFLAGS="--remap-path-prefix=$here=/socialweb-meshd --remap-path-prefix=$registry=/cargo-registry --remap-path-prefix=$sysroot=/rust-sysroot"

echo "build.sh: $(rustc --version) $(cargo --version)"
for arch in "${arches[@]}"; do
  triple="$arch-unknown-linux-musl"
  if [ ! -d "$sysroot/lib/rustlib/$triple" ]; then
    echo "build.sh: target $triple is not installed; run: rustup target add $triple" >&2
    exit 1
  fi
  lower="${triple//-/_}"
  upper="$(echo "$lower" | tr '[:lower:]' '[:upper:]')"
  env \
    "CC_$lower=$cc" \
    "CFLAGS_$lower=--target=$triple" \
    "AR_$lower=ar" \
    "CARGO_TARGET_${upper}_LINKER=$lld" \
    cargo build --release --locked --target "$triple" -p socialweb-meshd --manifest-path "$here/Cargo.toml"

  built="$CARGO_TARGET_DIR/$triple/release/socialweb-meshd"
  if readelf -lW "$built" | grep -q 'INTERP'; then
    echo "build.sh: $built asks for a dynamic loader; it must be static" >&2
    exit 1
  fi
  out="socialweb-meshd-$arch"
  install -m 0755 "$built" "$dist/$out"
  (cd "$dist" && sha256sum "$out" > "$out.sha256")
  echo "build.sh: $dist/$out $(stat -c %s "$dist/$out") bytes sha256 $(cut -d' ' -f1 "$dist/$out.sha256")"
done
