#!/bin/bash
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${KCP_CONTROLPLANE_IMAGE:-socialweb-control-plane:local}"
VOLUME="${SOCIALWEB_STATE_VOLUME:-socialweb-computer-kcp-state}"
NAME="${SOCIALWEB_CONTAINER_NAME:-socialweb}"

[ -e /dev/kvm ] || {
  echo >&2 "no /dev/kvm on this host: the container boots firecracker guests, and a guest needs the host's KVM device. Enable virtualization (kvm_intel or kvm_amd) and load it: modprobe kvm_intel or modprobe kvm_amd."
  exit 1
}

[ -e /dev/net/tun ] || {
  echo >&2 "no /dev/net/tun on this host: a guest's network comes from a tap device, and a tap needs the host's tun device."
  exit 1
}

"$REPO/deploy/control-plane-container/build.sh" computer

docker rm -f "$NAME" >/dev/null 2>&1 || true

exec docker run -d --name "$NAME" \
  --device=/dev/kvm \
  --device=/dev/net/tun \
  --security-opt seccomp=unconfined \
  --cap-add NET_ADMIN \
  --cap-add SYS_ADMIN \
  -v "$VOLUME:/var/lib/socialweb" \
  -p 127.0.0.1:8443:8443 \
  "$IMAGE"
