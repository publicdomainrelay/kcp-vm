#!/usr/bin/env bash
set -euo pipefail

# Deploy a compute-market bidder whose compute provider is the Firecracker
# provider, onto a host that has never run one.
#
#   ./scripts/deploy-firecracker-bidder.sh --dry-run
#   ./scripts/deploy-firecracker-bidder.sh
#
# Every host- or service-specific value is a variable below; nothing further
# down names a particular host, path or port. The script is idempotent: every
# remote step either checks first or writes only what changed, so a second run
# against a converged host restarts nothing.
#
# What the bidder does once it is up: it watches the market for
# com.publicdomainrelay.temp.compute.vm records, bids on them, and on accept
# boots a Firecracker guest from the node image this repo builds. The guest is
# born from the requester's cloud-init user_data -- the provider refuses an RFP
# with no user_data (hono-compute-provider lib/compute-provider-firecracker/mod.ts:403)
# -- so nothing here hand-assembles a guest.
#
# WHY NOTHING HERE HAND-PLACES A GUEST
#
# The provider boots each guest inside a container (hono-compute-provider
# acd09b0). That container is the only thing that needs /dev/kvm, /dev/net/tun,
# pasta and the firecracker binary, so all of them ride inside the runner image
# rather than being installed on this host. The host needs one runtime: docker,
# which is what lib/container-backend-docker drives.
#
# WHY THE IMAGE STORE IS SHIPPED RATHER THAN BUILT HERE
#
# Building a node image needs a Go toolchain, the k3s binary, the base squashfs,
# a kernel tree and CAP_SYS_CHROOT, none of which belong on this host. The store
# is built on the build host and rsynced. --compute-provider-firecracker-reuse-image
# is what makes that work: an image's name is the hash of every input its build
# read, and one of those inputs is the BUILD HOST's /usr/bin/busybox
# (socialweb-computer-kcp f2c0f50, internal/nodefirecracker/spec.go:20), so a
# Fedora build and an Ubuntu host can never agree on a name. -reuse-stale is the
# switch that says "use the whole image this configuration could have produced,
# whatever it is called"; without it the provider would try to build here.
#
# WHY THE GUEST KERNEL DOES NOT COME FROM THIS HOST
#
# The image store carries its own vmlinux (layout <out_dir>/sha256-<fingerprint>/vmlinux,
# socialweb-computer-kcp internal/nodefirecracker/spec.go:15). nodeboot loads the
# manifest out of the image directory and boots filepath.Join(ImageDir, "vmlinux")
# (cmd/socialweb-nodeboot/main.go:584), and the provider hands it nothing but
# image_dir. So the build host's Fedora kernel boots the guest on an Ubuntu 6.8
# host; the host kernel is only asked for KVM.
#
# WHY A SEPARATE ORG-ROOT COPY
#
# This host already runs services out of /opt/org-root, and their deploys replace
# that tree wholesale. This one does not: it ships the import closure of
# atproto-market/hono-bidder into its own prefix, so a deploy here cannot change
# what any other service reads on its next restart.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ORG_ROOT="$(dirname "$PROJECT_DIR")"
BUILD_HOST_HOME="${HOME}"

# ── Host ──────────────────────────────────────────────────────────────────────
SSH_TARGET="${SSH_TARGET:-root@socialweb.computer}"
SSH_PORT="${SSH_PORT:-1997}"
SSH_KEY="${SSH_KEY:-${HOME}/.ssh/id_ed25519_no_password}"
SSH_CONTROL_DIR="${SSH_CONTROL_DIR:-${HOME}/.ssh}"
SSH_OPTS="-o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 -o BatchMode=yes -o IdentitiesOnly=yes -o ControlMaster=auto -o ControlPath=${SSH_CONTROL_DIR}/cm-swc-%r@%h:%p -o ControlPersist=10m -o ServerAliveInterval=15"

# ── Service ───────────────────────────────────────────────────────────────────
SERVICE_NAME="${SERVICE_NAME:-socialweb-computer-bidder-firecracker}"
SERVICE_USER="${SERVICE_USER:-swc-fc-bidder}"
PREFIX="${PREFIX:-/opt/${SERVICE_NAME}}"
STATE_DIR="${STATE_DIR:-/var/lib/${SERVICE_NAME}}"
# Named BIDDER_* rather than after the flags they feed: the environment this is
# run from has ATPROTO_HANDLE, ATPROTO_PDS and POLICY in it already, and a knob
# that silently takes its value from an unrelated variable is worse than one that
# has no override at all.
BIDDER_DENO="${BIDDER_DENO:-/usr/local/bin/deno}"
BIDDER_SERVE_ADDR="${BIDDER_SERVE_ADDR:-127.0.0.1}"
BIDDER_SERVE_PORT="${BIDDER_SERVE_PORT:-8792}"
BIDDER_INGRESS_PROXY_HOST="${BIDDER_INGRESS_PROXY_HOST:-xrpc.fedproxy.com}"
BIDDER_PLC_DIRECTORY_URL="${BIDDER_PLC_DIRECTORY_URL:-https://plc.directory}"
BIDDER_PDS_URL="${BIDDER_PDS_URL:-http://127.0.0.1:8789}"
BIDDER_HANDLE="${BIDDER_HANDLE:-bidder-firecracker.socialweb.computer}"
BIDDER_POLICY="${BIDDER_POLICY:-open}"
BIDDER_POLICY_ARGS="${BIDDER_POLICY_ARGS:-{\"bidWindowSec\":8,\"firstFree\":true\}}"

# Derived remote paths. All of them live under STATE_DIR or PREFIX, so the unit's
# ReadWritePaths covers exactly the writable set.
CODE_DIR="${PREFIX}/org-root"
BIN_DIR="${STATE_DIR}/bin"
STORE_DIR="${STATE_DIR}/images"
INPUTS_DIR="${STATE_DIR}/image-inputs"
GUESTS_DIR="${STATE_DIR}/guests"
STAGE_DIR="${STATE_DIR}/stage"
CACHE_DIR="${STATE_DIR}/cache"
CONFIG_PATH="${STATE_DIR}/nodeimage-firecracker.json"

# Paths as the RUNNER CONTAINER sees them. The provider only checks that the
# --compute-provider-firecracker-vmm and -nodeboot flags are non-empty
# (atproto-market hono-bidder/mod.ts:545-564); the VMM it actually execs is
# microvm-firecracker's DEFAULT_FIRECRACKER, /usr/local/bin/firecracker, resolved
# inside the container. Naming the in-container paths keeps the two honest about
# each other.
FIRECRACKER_IN_CONTAINER="${FIRECRACKER_IN_CONTAINER:-/usr/local/bin/firecracker}"
NODEBOOT_IN_CONTAINER="${NODEBOOT_IN_CONTAINER:-/usr/local/bin/socialweb-nodeboot}"

# ── Capacity ──────────────────────────────────────────────────────────────────
# The market has no capacity field: an offering carries only appliesTo and
# endpointUrl, and the bidder bids every compute.vm RFP its policy admits --
# there is no "does this fit" gate anywhere in lib/market-bidder. So capacity is
# not advertised to the market; it is enforced on the host, where the guests
# actually are. See the slice below.
#
# The ceiling is one guest by default and two at most: this host has 4 vCPU and
# 7 GB and is also running caddy, a PDS, a relay, a gateway, an appview and
# another bidder. Raise MAX_GUESTS to 2 only after checking free memory.
MAX_GUESTS="${MAX_GUESTS:-1}"
GUEST_VCPU="${GUEST_VCPU:-2}"
GUEST_MEM_MIB="${GUEST_MEM_MIB:-3072}"
# What a guest's container costs above the guest: the firecracker process, the
# fedora userland the entrypoint starts, and pasta.
RUNNER_OVERHEAD_MIB="${RUNNER_OVERHEAD_MIB:-1024}"
GUEST_SLICE="${GUEST_SLICE:-socialweb-guests.slice}"

# ── Build host inputs ─────────────────────────────────────────────────────────
# The image store and the exact bytes it was built from, all already present
# locally. Nothing below is built by this script except one Go host binary.
LOCAL_CONFIG="${LOCAL_CONFIG:-${BUILD_HOST_HOME}/kcp-image/nodeimage-market.json}"
LOCAL_IMAGE_STORE="${LOCAL_IMAGE_STORE:-${BUILD_HOST_HOME}/kcp-image/out-market}"
LOCAL_PREINSTALL="${LOCAL_PREINSTALL:-${BUILD_HOST_HOME}/kcp-image/preinstall-market.json}"
LOCAL_AGENT_BINARY="${LOCAL_AGENT_BINARY:-${BUILD_HOST_HOME}/kcp-image/kcpagent}"
LOCAL_AGENT_PACKAGE="${LOCAL_AGENT_PACKAGE:-${ORG_ROOT}/socialweb-computer-kcp/cmd/socialweb-nodeagent}"
LOCAL_NODEIMAGE_BIN="${LOCAL_NODEIMAGE_BIN:-${BUILD_HOST_HOME}/kcp-image/socialweb-nodeimage}"

RUNNER_IMAGE="${RUNNER_IMAGE:-localhost/socialweb-firecracker-runner:local}"
RANGE_BASE="${RANGE_BASE:-172.30.0.0}"

DRY_RUN=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    -h|--help) sed -n '2,52p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

say() { printf '\n=== %s ===\n' "$*"; }

# Every single remote command goes through here, so --dry-run is one branch
# rather than a flag threaded through forty steps.
remote() {
  if ${DRY_RUN}; then
    printf -- '--- [dry-run] ssh %s -p %s %s\n' "${SSH_TARGET}" "${SSH_PORT}" "$*"
  else
    ssh ${SSH_OPTS} -i "${SSH_KEY}" -p "${SSH_PORT}" "${SSH_TARGET}" "$@"
  fi
}

# The local values are handed to the remote script as shell assignments rather
# than as environment, because the script is the thing being previewed and it
# should read the way it will run.
REMOTE_VARS="$(cat <<EOF
SERVICE_NAME='${SERVICE_NAME}'
SERVICE_USER='${SERVICE_USER}'
PREFIX='${PREFIX}'
STATE_DIR='${STATE_DIR}'
CODE_DIR='${CODE_DIR}'
BIN_DIR='${BIN_DIR}'
STORE_DIR='${STORE_DIR}'
INPUTS_DIR='${INPUTS_DIR}'
GUESTS_DIR='${GUESTS_DIR}'
STAGE_DIR='${STAGE_DIR}'
CACHE_DIR='${CACHE_DIR}'
CONFIG_PATH='${CONFIG_PATH}'
DENO='${BIDDER_DENO}'
SERVE_ADDR='${BIDDER_SERVE_ADDR}'
SERVE_PORT='${BIDDER_SERVE_PORT}'
INGRESS_PROXY_HOST='${BIDDER_INGRESS_PROXY_HOST}'
PLC_DIRECTORY_URL='${BIDDER_PLC_DIRECTORY_URL}'
ATPROTO_PDS_URL='${BIDDER_PDS_URL}'
ATPROTO_HANDLE='${BIDDER_HANDLE}'
POLICY='${BIDDER_POLICY}'
POLICY_ARGS='${BIDDER_POLICY_ARGS}'
FIRECRACKER_IN_CONTAINER='${FIRECRACKER_IN_CONTAINER}'
NODEBOOT_IN_CONTAINER='${NODEBOOT_IN_CONTAINER}'
RUNNER_IMAGE='${RUNNER_IMAGE}'
RANGE_BASE='${RANGE_BASE}'
GUEST_SLICE='${GUEST_SLICE}'
MAX_GUESTS='${MAX_GUESTS}'
GUEST_VCPU='${GUEST_VCPU}'
GUEST_MEM_MIB='${GUEST_MEM_MIB}'
RUNNER_OVERHEAD_MIB='${RUNNER_OVERHEAD_MIB}'
EOF
)"

# A multi-line remote script, read from stdin.
remote_script() {
  local desc="$1"
  local body full
  body="$(cat)"
  full="${REMOTE_VARS}
${body}"
  if ${DRY_RUN}; then
    printf -- '--- [dry-run] ssh %s -p %s %s <<REMOTE\n%s\n--- end\n' \
      "${SSH_TARGET}" "${SSH_PORT}" "${desc}" "${full}"
  else
    printf '%s\n' "${full}" | ssh ${SSH_OPTS} -i "${SSH_KEY}" -p "${SSH_PORT}" \
      "${SSH_TARGET}" "bash -s"
  fi
}

RSYNC_SSH="ssh ${SSH_OPTS} -i ${SSH_KEY} -p ${SSH_PORT}"

rsync_to() {
  local desc="$1" src="$2" dest="$3"
  shift 3
  if ${DRY_RUN}; then
    printf -- '--- [dry-run] rsync (%s) %s -> %s:%s %s\n' "${desc}" "${src}" "${SSH_TARGET}" "${dest}" "$*"
  else
    rsync -a "$@" -e "${RSYNC_SSH}" "${src}" "${SSH_TARGET}:${dest}"
  fi
}

# The code the bidder runs. atproto-market/deno.json resolves every
# @publicdomainrelay/* import by relative path into a sibling repo, so the
# closure is the set of repos that map reaches -- computed rather than listed, so
# a new sibling import is shipped instead of failing at startup. The set is tens
# of MB; the whole org root is 12 GB, which is why it is not shipped wholesale.
code_closure() {
  python3 - "${ORG_ROOT}" <<'PY'
import json, os, sys
root = sys.argv[1]
seen, queue, dirs = set(), ["atproto-market/deno.json"], set()
while queue:
    cfg = queue.pop(0)
    if cfg in seen:
        continue
    seen.add(cfg)
    path = os.path.join(root, cfg)
    try:
        with open(path) as fh:
            doc = json.load(fh)
    except OSError:
        continue
    values = list((doc.get("imports") or {}).values()) + list(doc.get("workspace") or [])
    for value in values:
        if not isinstance(value, str) or not value.startswith("."):
            continue
        target = os.path.normpath(os.path.join(os.path.dirname(path), value))
        rel = os.path.relpath(target, root)
        if rel.startswith(".."):
            continue
        dirs.add(rel.split(os.sep)[0])
        nxt = os.path.join(target, "deno.json")
        if os.path.exists(nxt):
            queue.append(os.path.relpath(nxt, root))
print("\n".join(sorted(dirs)))
PY
}

# ── Local preflight ───────────────────────────────────────────────────────────
say "checking the build host"
command -v python3 >/dev/null 2>&1 || { echo "python3 is needed to rewrite the image configuration" >&2; exit 1; }

for required in "${LOCAL_CONFIG}" "${LOCAL_IMAGE_STORE}" "${LOCAL_PREINSTALL}" \
                "${LOCAL_AGENT_BINARY}" "${LOCAL_AGENT_PACKAGE}"; do
  [ -e "${required}" ] || { echo "missing on the build host: ${required}" >&2; exit 1; }
done

FINGERPRINT="$(basename "$(ls -1d "${LOCAL_IMAGE_STORE}"/sha256-* 2>/dev/null | head -1)" 2>/dev/null || true)"
if ! [[ "${FINGERPRINT}" =~ ^sha256-[0-9a-f]{64}$ ]]; then
  echo "no image store under ${LOCAL_IMAGE_STORE}: expected one sha256-<64 hex> directory" >&2
  exit 1
fi
LOCAL_STORE_IMAGE="${LOCAL_IMAGE_STORE}/${FINGERPRINT}"
echo "image        ${LOCAL_STORE_IMAGE}"

cfg_field() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])' "${LOCAL_CONFIG}" "$1"; }
LOCAL_KERNEL="$(cfg_field kernel_source)"
LOCAL_KERNEL_CONFIG="$(cfg_field kernel_config)"
LOCAL_MODULES="$(cfg_field module_source)"
LOCAL_BASE_IMAGE="$(cfg_field base_image)"
LOCAL_K3S="$(cfg_field k3s_binary)"
for required in "${LOCAL_KERNEL}" "${LOCAL_KERNEL_CONFIG}" "${LOCAL_MODULES}" \
                "${LOCAL_BASE_IMAGE}" "${LOCAL_K3S}"; do
  if [ ! -e "${required}" ]; then
    echo "the configuration at ${LOCAL_CONFIG} names ${required}, which does not exist here." >&2
    echo "the store cannot be reused from a configuration whose inputs are gone: the host" >&2
    echo "recomputes the fingerprint, and it reads every one of them." >&2
    exit 1
  fi
done

# The builder binary the provider shells out to (-ensure -json). It is a HOST
# binary: it decides whether the store already holds the image, and with
# -reuse-stale it does not build.
if [ ! -x "${LOCAL_NODEIMAGE_BIN}" ]; then
  say "building socialweb-nodeimage"
  if ${DRY_RUN}; then
    printf -- '--- [dry-run] CGO_ENABLED=0 go build -C %s -o %s ./cmd/socialweb-nodeimage\n' \
      "${ORG_ROOT}/socialweb-computer-kcp" "${LOCAL_NODEIMAGE_BIN}"
  else
    CGO_ENABLED=0 go build -C "${ORG_ROOT}/socialweb-computer-kcp" \
      -o "${LOCAL_NODEIMAGE_BIN}" ./cmd/socialweb-nodeimage
  fi
fi

# The runner image: fedora + passt + e2fsprogs + iproute + firecracker +
# socialweb-nodeboot (hono-compute-provider/hono-firecracker-standalone/Dockerfile),
# built by `deno task build:firecracker-runner` in hono-compute-provider.
LOCAL_RUNNER_ID=""
if ${DRY_RUN}; then
  LOCAL_RUNNER_ID="<current image id>"
elif docker image inspect "${RUNNER_IMAGE}" >/dev/null 2>&1; then
  LOCAL_RUNNER_ID="$(docker image inspect --format '{{json .RootFS.Layers}}' "${RUNNER_IMAGE}")"
else
  echo "the runner image ${RUNNER_IMAGE} is not present here. Build it with:" >&2
  echo "  cd ${ORG_ROOT}/hono-compute-provider && deno task build:firecracker-runner" >&2
  exit 1
fi
echo "runner image ${RUNNER_IMAGE}"

# ── The image builder's configuration, as this host will read it ──────────────
# Same fields, same bytes, same digests; only the paths move, and out_dir names
# the store that is about to be rsynced. The one input this cannot carry is the
# build host's /usr/bin/busybox, which is why the host runs with -reuse-stale
# instead of expecting the name to match.
say "rendering the host's copy of the image configuration"
python3 - "${LOCAL_CONFIG}" "${WORK}/nodeimage-firecracker.json" \
  "${STORE_DIR}" "${INPUTS_DIR}" "${STAGE_DIR}" "${CACHE_DIR}" \
  "${FIRECRACKER_IN_CONTAINER}" <<'PY'
import json, sys
src, dst, store, inputs, stage, cache, firecracker = sys.argv[1:8]
cfg = json.load(open(src))
cfg["firecracker"] = firecracker
cfg["kernel_source"] = inputs + "/kernel/vmlinuz"
cfg["kernel_config"] = inputs + "/kernel/config"
cfg["module_source"] = inputs + "/modules"
cfg["base_image"] = inputs + "/base/ubuntu-24.04.squashfs"
cfg["k3s_binary"] = inputs + "/k3s/k3s"
cfg["agent_binary"] = inputs + "/agent/kcpagent"
cfg["agent_package"] = inputs + "/agent-package"
cfg["stage_dir"] = stage
cfg["out_dir"] = store
cfg["image_cache_dir"] = cache
cfg.pop("preinstall", None)
with open(dst, "w") as fh:
    json.dump(cfg, fh, indent=2)
    fh.write("\n")
PY
if ${DRY_RUN}; then
  printf -- '--- [dry-run] the configuration shipped as %s:\n' "${CONFIG_PATH}"
  sed 's/^/    /' "${WORK}/nodeimage-firecracker.json"
fi

# Checksums of the store, verified on the far side afterwards. This is the only
# thing that says the gigabytes that arrived are the gigabytes that left: rsync
# --sparse rebuilds the holes, and a truncated transfer still looks whole in a
# directory listing.
say "checksumming the image store"
( cd "${LOCAL_IMAGE_STORE}" && find "${FINGERPRINT}" -type f -print0 \
    | sort -z | xargs -0 sha256sum ) > "${WORK}/image.sha256"
cat "${WORK}/image.sha256"

# ── Remote: account, directories, runtime ─────────────────────────────────────
say "creating the service account and its directories"
remote_script "account and directories" <<'REMOTE'
set -euo pipefail

# docker is the one runtime the provider's code calls: lib/container-backend-docker
# drives the docker CLI and starts the daemon itself if it is down. Ubuntu's
# docker.io is the same CLI and daemon. Idempotent.
if ! command -v docker >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq docker.io
fi
systemctl enable --now docker >/dev/null 2>&1 || true
# The daemon is a separate process that takes a moment; this is what says it came
# up, and it is the same call lib/container-backend-docker makes before it runs
# anything.
for _ in $(seq 1 30); do
  if docker info --format '{{.ServerVersion}}' 2>/dev/null; then
    break
  fi
  sleep 2
done
docker info --format '{{.ServerVersion}}' >/dev/null

# A guest's launcher is an unprivileged user of its own inside the container and
# pasta, which is what gives the guest its network, sandboxes itself: it clones
# into a user, pid and mount namespace of its own before it starts the VMM.
# Ubuntu 24.04 ships kernel.apparmor_restrict_unprivileged_userns=1, which pins
# that namespace to a profile carrying `userns,` -- and a container is unconfined,
# which that setting treats as unprivileged, so the clone fails with
# "Couldn't write to /proc/self/uid_map: Operation not permitted" and pasta exits
# before a guest exists. Measured on this host with the container privileged, which
# is the other half of what pasta needs: at 1 the launcher cannot create its
# namespace, at 0 it can. Nothing else on this host wants unprivileged user
# namespaces, so the drop-in is written here and read back, and the running value
# is applied without waiting for a reboot.
install -d -m 0755 /etc/sysctl.d
if ! grep -qs '^kernel\.apparmor_restrict_unprivileged_userns' /etc/sysctl.d/99-socialweb-guest-userns.conf; then
  printf '%s\n' \
    '# Read by the firecracker bidder: a guest launcher runs unprivileged and pasta' \
    '# clones into a user namespace of its own before it starts the VMM.' \
    'kernel.apparmor_restrict_unprivileged_userns = 0' \
    > /etc/sysctl.d/99-socialweb-guest-userns.conf
fi
sysctl -q -w kernel.apparmor_restrict_unprivileged_userns=0 2>/dev/null || true

# The bidder is not root: the unit drops it to this account and grants it nothing
# beyond the docker socket.
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --create-home \
    --home-dir "${STATE_DIR}" --shell /usr/sbin/nologin "${SERVICE_USER}"
fi
# Driving the daemon is what lets it start a guest's container. That membership
# is privileged by construction -- there is no narrower way to drive docker --
# and it is why this account exists for this service alone.
usermod -aG docker "${SERVICE_USER}"

install -d -m 755 -o root -g root "${PREFIX}"
install -d -m 755 -o "${SERVICE_USER}" -g "${SERVICE_USER}" \
  "${STATE_DIR}" "${BIN_DIR}" "${STORE_DIR}" "${GUESTS_DIR}" "${STAGE_DIR}" "${CACHE_DIR}"
install -d -m 755 -o root -g root "${INPUTS_DIR}" "${INPUTS_DIR}/kernel" \
  "${INPUTS_DIR}/base" "${INPUTS_DIR}/k3s" "${INPUTS_DIR}/agent" "${INPUTS_DIR}/agent-package"
echo "account ${SERVICE_USER} ready"
REMOTE

# ── Ship ──────────────────────────────────────────────────────────────────────
say "shipping the code the bidder imports"
CLOSURE="$(code_closure)"
echo "${CLOSURE}" | sed 's/^/  /'
if ! ${DRY_RUN}; then
  remote "install -d -m 755 '${CODE_DIR}'"
fi
while read -r repo; do
  [ -n "${repo}" ] || continue
  rsync_to "code ${repo}" "${ORG_ROOT}/${repo}/" "${CODE_DIR}/${repo}/" \
    --delete --exclude node_modules --exclude .git --exclude .codegraph
done <<< "${CLOSURE}"

say "shipping the image store (${FINGERPRINT})"
rsync_to "image store" "${LOCAL_STORE_IMAGE}/" "${STORE_DIR}/${FINGERPRINT}/" --sparse

say "shipping the bytes the image was built from"
# Not needed to boot -- the guest's copy is already inside the image -- but the
# configuration on the host names these paths, and the builder reads every one of
# them to work out which image the configuration asks for. Naming the real files
# keeps the host's record of provenance true, so a later reader sees what the
# image is instead of what some other host's kernel happened to be.
rsync_to "kernel" "${LOCAL_KERNEL}" "${INPUTS_DIR}/kernel/vmlinuz"
rsync_to "kernel config" "${LOCAL_KERNEL_CONFIG}" "${INPUTS_DIR}/kernel/config"
# The tree has dangling build/ and source/ symlinks pointing at a source tree
# that is not here; -a preserves them as symlinks, which is what the build host
# read and what the digest is taken over.
rsync_to "kernel modules" "${LOCAL_MODULES}/" "${INPUTS_DIR}/modules/" --delete
rsync_to "base image" "${LOCAL_BASE_IMAGE}" "${INPUTS_DIR}/base/ubuntu-24.04.squashfs"
rsync_to "k3s" "${LOCAL_K3S}" "${INPUTS_DIR}/k3s/k3s"
rsync_to "guest agent" "${LOCAL_AGENT_BINARY}" "${INPUTS_DIR}/agent/kcpagent"
rsync_to "agent package" "${LOCAL_AGENT_PACKAGE}/" "${INPUTS_DIR}/agent-package/" --delete
rsync_to "preinstall manifest" "${LOCAL_PREINSTALL}" "${INPUTS_DIR}/preinstall.json"
rsync_to "image configuration" "${WORK}/nodeimage-firecracker.json" "${CONFIG_PATH}"
rsync_to "image builder" "${LOCAL_NODEIMAGE_BIN}" "${BIN_DIR}/socialweb-nodeimage"
rsync_to "store checksums" "${WORK}/image.sha256" "${STORE_DIR}/.image.sha256"

say "verifying the store arrived intact"
remote_script "verify the store" <<'REMOTE'
set -euo pipefail
chown -R "${SERVICE_USER}:${SERVICE_USER}" "${STORE_DIR}" "${BIN_DIR}" \
  "${GUESTS_DIR}" "${STAGE_DIR}" "${CACHE_DIR}"
chown -R root:root "${INPUTS_DIR}"
chmod -R a+rX "${INPUTS_DIR}"
# The image store is read by the CONTAINER that boots a guest, under whatever
# user that container maps to, so it cannot be owner-only: measured on a real
# deploy, the manifest at <store>/<fingerprint>/image.json answered
# "permission denied" and every provisioning failed with "the container that
# boots guest ... exited before it reported a guest".
chmod -R a+rX "${STORE_DIR}"
# The container that boots a guest runs as a DIFFERENT user from the service
# that owns this directory, and it creates each guest's rootfs copy and identity
# disk inside it: measured, a 0755 guests directory owned by the service user
# answered "open .../rootfs.ext4: permission denied" and every provisioning
# failed. This is a scratch directory of per-boot guest files on a single-tenant
# host; it is the one place the door's own user cannot be the only writer.
chmod 0777 "${GUESTS_DIR}"
chmod 755 "${BIN_DIR}/socialweb-nodeimage"
cd "${STORE_DIR}"
sha256sum -c .image.sha256
REMOTE

# ── Capacity: the only place it can be enforced ───────────────────────────────
say "capping the guest budget"
remote_script "guest slice and docker's cgroup parent" <<'REMOTE'
set -euo pipefail

# Guests run as containers, so a cgroup that holds every container holds every
# guest. It is NOT docker.service: with the systemd cgroup driver each container
# becomes its own system.slice/docker-<id>.scope, a SIBLING of the daemon, so a
# limit on the daemon bounds nothing. The containers go into a slice of their
# own instead, and the budget goes on the slice.
#
# This is the honest place for it. The market carries no capacity field, so an
# oversized bid is not refused -- the container that would hold it is simply
# never allowed the memory, and the kernel kills it inside the slice instead of
# taking the PDS, the relay or caddy down with it.
budget_mib=$(( MAX_GUESTS * (GUEST_MEM_MIB + RUNNER_OVERHEAD_MIB) ))
cpus="$(nproc)"
quota=$(( MAX_GUESTS * GUEST_VCPU * 100 / cpus ))

# A real unit file and not a drop-in: a slice systemd has never seen has no
# fragment to drop into, and the budget would then be a file nothing reads.
slice_unit="/etc/systemd/system/${GUEST_SLICE}"
cat > "${slice_unit}.new" <<CONF
[Unit]
Description=Compute-market Firecracker guests

[Slice]
MemoryMax=${budget_mib}M
MemorySwapMax=${budget_mib}M
CPUQuota=${quota}%
CONF

slice_changed=no
if ! cmp -s "${slice_unit}.new" "${slice_unit}" 2>/dev/null; then
  mv "${slice_unit}.new" "${slice_unit}"
  slice_changed=yes
  echo "guest slice budget now ${MAX_GUESTS} x (${GUEST_MEM_MIB}+${RUNNER_OVERHEAD_MIB})MiB, ${quota}% cpu"
else
  rm -f "${slice_unit}.new"
  echo "guest slice budget unchanged: ${MAX_GUESTS} guests x ${GUEST_MEM_MIB}MiB, ${quota}% cpu"
fi

# Point the daemon at that slice. Written only when there is no daemon.json at
# all: this host had no docker before this script, so the file is either ours or
# an operator's, and merging JSON in shell is how an operator's file gets
# silently rewritten. If it is theirs, say exactly what to add.
install -d -m 755 /etc/docker
daemon_json=/etc/docker/daemon.json
docker_changed=no
if [ ! -f "${daemon_json}" ]; then
  cat > "${daemon_json}" <<JSON
{
  "cgroup-parent": "${GUEST_SLICE}"
}
JSON
  docker_changed=yes
elif grep -q "\"cgroup-parent\"" "${daemon_json}" \
     && grep -q "${GUEST_SLICE}" "${daemon_json}"; then
  echo "docker already parents containers at ${GUEST_SLICE}"
else
  echo "refusing: ${daemon_json} exists and does not name ${GUEST_SLICE}." >&2
  echo "Add \"cgroup-parent\": \"${GUEST_SLICE}\" to it by hand and run this again." >&2
  echo "The file was left exactly as it was." >&2
  exit 1
fi

systemctl daemon-reload
if [ "${slice_changed}" = yes ] && systemctl is-active --quiet "${GUEST_SLICE}"; then
  # Changing a slice's limits on a live slice is not something systemd does by
  # itself, and the guests under it are what the limits exist to bound.
  systemctl stop "${GUEST_SLICE}" 2>/dev/null || true
fi
if [ "${docker_changed}" = yes ] && systemctl is-active --quiet docker; then
  # Necessary, not incidental: the daemon reads cgroup-parent at startup.
  systemctl restart docker
fi
systemctl show -p MemoryMax -p CPUQuota "${GUEST_SLICE}" 2>/dev/null || true
REMOTE

say "shipping the runner image"
if ! ${DRY_RUN}; then
  if ! remote "docker image inspect --format '{{json .RootFS.Layers}}' '${RUNNER_IMAGE}'" 2>/dev/null \
      | grep -qxF "${LOCAL_RUNNER_ID}"; then
    docker save "${RUNNER_IMAGE}" \
      | ssh ${SSH_OPTS} -i "${SSH_KEY}" -p "${SSH_PORT}" "${SSH_TARGET}" "docker load"
    remote "docker image inspect --format '{{json .RootFS.Layers}}' '${RUNNER_IMAGE}'" \
      | grep -qxF "${LOCAL_RUNNER_ID}"
    echo "runner image ${RUNNER_IMAGE} loaded"
  else
    echo "runner image ${RUNNER_IMAGE} already current"
  fi
else
  printf -- '--- [dry-run] docker image inspect --format {{.Id}} %s   (load only if it differs)\n' "${RUNNER_IMAGE}"
  printf -- '--- [dry-run] docker save %s | ssh %s -p %s %s docker load\n' \
    "${RUNNER_IMAGE}" "${SSH_TARGET}" "${SSH_PORT}" "${SSH_TARGET}"
fi

# ── Identity and the unit ─────────────────────────────────────────────────────
say "writing the bidder's state, environment and unit"
remote_script "state, environment, unit" <<'REMOTE'
set -euo pipefail

# The undici preload. @atproto-labs/fetch-node's unicast guard reads
# process.versions.undici, which Deno does not define, so anything that builds an
# IdResolver throws "Unicast SSRF protection requires Node.js 20.6+" on Linux.
# The guard asks whether undici is patched; Deno's fetch is not undici, so the
# question does not apply. Same shim the sibling bidder preloads.
cat > "${STATE_DIR}/undici-shim.ts" <<'SHIM'
const versions = process.versions as Record<string, string>;
if (!versions.undici) versions.undici = "6.11.1";
SHIM
chown "${SERVICE_USER}:${SERVICE_USER}" "${STATE_DIR}/undici-shim.ts"

# The bidder's identity is the CLI's, not this script's. Given
# --private-key-hex-path it reads a hex secp256k1 key there, generates and writes
# one when the file is absent (atproto-market hono-bidder/mod.ts:41-58, 468-481),
# registers the did:plc, and keeps its repo in the PDS state directory. So the
# key and the DID appear on first start and every later run reuses them.
#
# The handle and password select the branch that does NOT print an association
# QR and wait (mod.ts:261-281); that branch builds a local PDS agent and never
# authenticates to a remote PDS with them. A real random secret is written
# rather than a placeholder, so nothing downstream can mistake it for one.
if [ ! -f "${STATE_DIR}/bidder.env" ]; then
  {
    echo "ATPROTO_HANDLE=${ATPROTO_HANDLE}"
    echo "ATPROTO_PASSWORD=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    echo "ATPROTO_PDS_URL=${ATPROTO_PDS_URL}"
  } > "${STATE_DIR}/bidder.env"
  chown "${SERVICE_USER}:${SERVICE_USER}" "${STATE_DIR}/bidder.env"
  chmod 600 "${STATE_DIR}/bidder.env"
fi

cat > "${STATE_DIR}/${SERVICE_NAME}.service.new" <<UNIT
[Unit]
Description=${SERVICE_NAME} — market bidder that places Firecracker guests
After=network.target network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${STATE_DIR}
EnvironmentFile=${STATE_DIR}/bidder.env
Environment=HOME=${STATE_DIR}
Environment=DENO_DIR=${STATE_DIR}/deno
# --node-modules-dir=none is what lets this run from a source tree that is never
# written: npm deps resolve out of DENO_DIR instead of a node_modules beside
# deno.json. --preload has to come before the entrypoint.
ExecStart=${DENO} run -A --node-modules-dir=none --unstable-kv --unstable-worker-options \\
  --preload ${STATE_DIR}/undici-shim.ts \\
  --config ${CODE_DIR}/atproto-market/deno.json \\
  ${CODE_DIR}/atproto-market/hono-bidder/mod.ts \\
  --service-name ${SERVICE_NAME} \\
  --private-key-hex-path ${STATE_DIR}/bidder-key.hex \\
  --pds-state-path ${STATE_DIR}/bidder-pds \\
  --atproto-handle \${ATPROTO_HANDLE} \\
  --atproto-password \${ATPROTO_PASSWORD} \\
  --atproto-pds-url \${ATPROTO_PDS_URL} \\
  --ingress-proxy-host ${INGRESS_PROXY_HOST} \\
  --plc-directory-url ${PLC_DIRECTORY_URL} \\
  --compute-provider-firecracker \\
  --compute-provider-firecracker-nodeimage ${BIN_DIR}/socialweb-nodeimage \\
  --compute-provider-firecracker-nodeboot ${NODEBOOT_IN_CONTAINER} \\
  --compute-provider-firecracker-config ${CONFIG_PATH} \\
  --compute-provider-firecracker-repo-dir ${STATE_DIR} \\
  --compute-provider-firecracker-preinstall ${INPUTS_DIR}/preinstall.json \\
  --compute-provider-firecracker-vmm ${FIRECRACKER_IN_CONTAINER} \\
  --compute-provider-firecracker-runner-image ${RUNNER_IMAGE} \\
  --compute-provider-firecracker-work-root ${GUESTS_DIR} \\
  --compute-provider-firecracker-range-base ${RANGE_BASE} \\
  --compute-provider-firecracker-reuse-image \\
  --serve-addr ${SERVE_ADDR} \\
  --serve-port ${SERVE_PORT} \\
  --policy ${POLICY} \\
  --policy-args '${POLICY_ARGS}'

Restart=always
RestartSec=5
LimitNOFILE=65536

# It needs the docker socket at /run/docker.sock. ProtectSystem=strict makes
# /run read-only too, and that is fine: connect()ing to a unix socket is not a
# write. Everything it may write is named below; nothing else is writable.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=${STATE_DIR}

[Install]
WantedBy=multi-user.target
UNIT

# Warm the module cache as the service user so the first start is not the thing
# that discovers a slow registry, and so a start that races Restart=always is
# racing a warm cache. Best effort: a network hiccup here must not fail a deploy
# that is otherwise converged.
install -d -m 700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${STATE_DIR}/deno"
runuser -u "${SERVICE_USER}" -- env HOME="${STATE_DIR}" DENO_DIR="${STATE_DIR}/deno" \
  "${DENO}" cache --node-modules-dir=none --unstable-kv --unstable-worker-options \
  --config "${CODE_DIR}/atproto-market/deno.json" \
  "${CODE_DIR}/atproto-market/hono-bidder/mod.ts" >/dev/null 2>&1 || true

changed=no
if cmp -s "${STATE_DIR}/${SERVICE_NAME}.service.new" \
          "/etc/systemd/system/${SERVICE_NAME}.service"; then
  rm -f "${STATE_DIR}/${SERVICE_NAME}.service.new"
else
  mv "${STATE_DIR}/${SERVICE_NAME}.service.new" \
     "/etc/systemd/system/${SERVICE_NAME}.service"
  chmod 644 "/etc/systemd/system/${SERVICE_NAME}.service"
  changed=yes
fi

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" >/dev/null
if [ "${changed}" = yes ]; then
  systemctl restart "${SERVICE_NAME}"
  echo "unit changed; restarted ${SERVICE_NAME}"
elif ! systemctl is-active --quiet "${SERVICE_NAME}"; then
  systemctl start "${SERVICE_NAME}"
  echo "unit unchanged; started ${SERVICE_NAME}"
else
  echo "unit unchanged and running; left alone"
fi
sleep 5
systemctl is-active "${SERVICE_NAME}" || true
REMOTE

# ── Prove the host resolves the image the way the unit says it will ───────────
say "asking the host which image its configuration names"
remote_script "ensure -reuse-stale" <<'REMOTE'
set -euo pipefail
# Runs as the service user, which is who will run it at boot, so this is the
# permission check as much as the resolution check. -reuse-stale against a store
# that already holds a whole image reads and returns; it does not build.
runuser -u "${SERVICE_USER}" -- env HOME="${STATE_DIR}" \
  "${BIN_DIR}/socialweb-nodeimage" -ensure -json -reuse-stale \
  -preinstall "${INPUTS_DIR}/preinstall.json" -config "${CONFIG_PATH}"
REMOTE

# ── Report ────────────────────────────────────────────────────────────────────
say "how to verify it is bidding"
cat <<REPORT

  service        systemctl status ${SERVICE_NAME}
  logs           journalctl -u ${SERVICE_NAME} -f

  These are the log lines the bidder emits when it bids:

    {"event":"bidder_ready",...}        startup: the DID and the relay ingress it is
                                        reachable at. This is the identity the market
                                        sees bidding.
    bidder offering refreshed           the offering record saying it is open for business
    bidder received VM RFP              an RFP arrived
    bidder created VM bid               the bid was written; bidUri names it
    bidder submitted bid to requester   the requester took it
    bidder provisioned VM               the accept path reached the provider and a guest
                                        is booting; metadata.ip is its address
    vm.onNetwork event created on PDS   the guest is up and reported its address

  Records it publishes, in its own repo on the PDS it runs in process:

    com.publicdomainrelay.temp.market.offering    once per offering-refresh interval
    com.publicdomainrelay.temp.market.bid         per RFP it bids on
    com.publicdomainrelay.temp.market.receipt     per contract it wins

  The identity is created on first start, not by this script:
    ${STATE_DIR}/bidder-key.hex   the secp256k1 key the CLI generated
    ${STATE_DIR}/bidder-pds       the repo and the did:plc it registered

  The image it boots from:
    ls ${STORE_DIR}/${FINGERPRINT}
    ${BIN_DIR}/socialweb-nodeimage -ensure -json -reuse-stale \\
      -preinstall ${INPUTS_DIR}/preinstall.json -config ${CONFIG_PATH}
  That answers {"state":"reused",...}. "present" is unreachable here: it would
  require this host's /usr/bin/busybox to hash to the build host's.

  A guest that is running:
    docker ps --filter name=fc-
    ls ${GUESTS_DIR}

REPORT
