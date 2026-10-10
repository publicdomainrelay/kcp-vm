#!/bin/bash
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${KCP_K8S_IMAGE:-socialweb-k8s:local}"
VOLUME="${SOCIALWEB_STATE_VOLUME:-socialweb-computer-kcp-state}"
NAME="${SOCIALWEB_CONTAINER_NAME:-socialweb}"

KCP_K8S_IMAGE="$IMAGE" "$REPO/deploy/control-plane-container/build.sh" k8s
docker rm -f "$NAME" >/dev/null 2>&1 || true
exec docker run -d --name "$NAME" \
  -v "$VOLUME:/var/lib/socialweb" \
  -e SOCIALWEB_CONTAINER_BOOTSTRAP=0 \
  -e SOCIALWEB_CONTAINER_KCP_BIND=0.0.0.0 \
  -e SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market \
  ${SOCIALWEB_CONTAINER_ENV_FILE:+--env-file "$SOCIALWEB_CONTAINER_ENV_FILE"} \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:6443:6443 \
  -p 127.0.0.1:8788:8788 \
  "$IMAGE"
