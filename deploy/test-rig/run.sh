#!/bin/bash
# The privileged, ephemeral CI-only rig. It boots the product's own container image with
# /dev/kvm and the network privileges a VMM guest needs, then runs the live gate inside it.
# The shipped image, the shipped entrypoint and the strict control-plane proof are unchanged:
# this script only adds the flags on the docker run command line it issues, never in the image.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${KCP_RIG_IMAGE:-socialweb-control-plane-rig:local}"
NAME="${KCP_RIG_CONTAINER:-socialweb-rig}"
VOLUME="${KCP_RIG_STATE_VOLUME:-socialweb-rig-state}"
KCP_IMAGE_DIR="${KCP_IMAGE_DIR:-/home/johnandersen777/kcp-image}"
RUN_DIR="${KCP_RIG_RUN_DIR:-/var/tmp/socialweb-rig}"
TOOLCHAIN="${KCP_GO_TOOLCHAIN:-$(go env GOROOT)}"
MODCACHE="${KCP_GO_MODCACHE:-$(go env GOMODCACHE)}"
BUILDCACHE="${KCP_GO_BUILDCACHE:-$(go env GOCACHE)}"
EXTRA_FLAGS="${KCP_RIG_DOCKER_FLAGS:---device=/dev/kvm --device=/dev/net/tun --security-opt seccomp=unconfined --security-opt apparmor=unconfined}"

[ -e /dev/kvm ] || { echo >&2 "no /dev/kvm on this host: every VMM this rig boots is a KVM guest, so there is no rig without it"; exit 1; }
[ -e /dev/net/tun ] || { echo >&2 "no /dev/net/tun on this host: a guest's tap comes from it"; exit 1; }
for dir in "$TOOLCHAIN" "$MODCACHE" "$BUILDCACHE" "$KCP_IMAGE_DIR"; do
  [ -d "$dir" ] || { echo >&2 "no $dir on this host: the rig mounts it into the container, and it cannot invent it"; exit 1; }
done

dirty_files() { git -C "$REPO" status --porcelain | wc -l; }
if [ "$(dirty_files)" -ne 0 ] && [ "${KCP_RIG_ALLOW_DIRTY:-0}" != "1" ]; then
  echo >&2 "rig: $REPO has $(dirty_files) uncommitted paths. The gate runs against the repo as it is on disk, so a result from here names no commit and cannot be compared with one. Commit first, or set KCP_RIG_ALLOW_DIRTY=1 to run against the working tree deliberately."
  exit 1
fi
RIG_HEAD="$(git -C "$REPO" rev-parse HEAD)"
RIG_DIRTY_AT_START="$(dirty_files)"
echo "rig: the gate runs against $RIG_HEAD (${RIG_DIRTY_AT_START} uncommitted paths at start)"

cleanup() {
	if [ "${KCP_RIG_KEEP:-0}" = "1" ]; then
		echo "rig: KCP_RIG_KEEP=1, leaving container $NAME and volume $VOLUME for debugging"
		return 0
	fi
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

install -d -m 0755 "$RUN_DIR"
go build -o "$RUN_DIR/socialweb-rig-inputs" "$REPO/cmd/socialweb-rig-inputs"
echo "rig: built the input deriver $RUN_DIR/socialweb-rig-inputs"

KCP_CONTROLPLANE_IMAGE="$IMAGE" "$REPO/deploy/control-plane-container/build.sh" computer
echo "rig: built $IMAGE"

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
docker volume create "$VOLUME" >/dev/null

docker run -d --name "$NAME" \
  $EXTRA_FLAGS \
  -v "$VOLUME:/var/lib/socialweb" \
  -v "$REPO:/src:ro" \
  -v "$RUN_DIR/socialweb-rig-inputs:/usr/local/bin/socialweb-rig-inputs:ro" \
  -v "$TOOLCHAIN:/go/toolchain:ro" \
  -v "$MODCACHE:/go/pkg/mod" \
  -v "$BUILDCACHE:/go/cache" \
  "$IMAGE" >/dev/null
echo "rig: started $NAME with: $EXTRA_FLAGS"

[ -x "$REPO/deploy/wait-ready.sh" ] || {
  echo >&2 "rig: deploy/wait-ready.sh is missing, and the rig waits through it rather than polling in a sleep loop"
  exit 1
}
"$REPO/deploy/wait-ready.sh" "$NAME" "${KCP_RIG_READY_TIMEOUT:-600}"

docker exec -u 1000:1000 -w /src "$NAME" bash /src/deploy/test-rig/inside.sh "$@"
rig_rc=$?

RIG_AFTER="$(git -C "$REPO" rev-parse HEAD)"
if [ "$(dirty_files)" -ne "$RIG_DIRTY_AT_START" ] || [ "$RIG_AFTER" != "$RIG_HEAD" ]; then
  echo >&2 "rig: the repo MOVED while the gate ran ($RIG_HEAD -> $RIG_AFTER, $(dirty_files) uncommitted paths against $RIG_DIRTY_AT_START at start). The gate reads the repo from /src as it changes, so this result describes a tree that is not any commit: it is not a reading, and it must not be trusted in either direction. Re-run against a tree that holds still."
  exit 1
fi
echo "rig: the gate ran against $RIG_HEAD and the repo did not move"
exit "$rig_rc"
