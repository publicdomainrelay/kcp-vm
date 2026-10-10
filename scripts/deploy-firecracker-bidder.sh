#!/usr/bin/env bash
set -euo pipefail

# Prepare a host to run compute-market bidders whose compute provider is the
# Firecracker provider, one per atproto account, and install the systemd
# template that runs them.
#
#   ./scripts/deploy-firecracker-bidder.sh --dry-run           preview, over ssh
#   ./scripts/deploy-firecracker-bidder.sh                     over ssh to SSH_TARGET
#   ./scripts/deploy-firecracker-bidder.sh --local [--dry-run] this host, through sudo
#
# Then, on that host, once per account:
#
#   sudo systemctl enable --now socialweb-computer-bidder@<handle>.service
#   journalctl -u socialweb-computer-bidder@<handle> -f
#
# The journal shows a QR code and its https://qr.fedfork.com/#oauth=... URL;
# scanning it with <handle> signs the bidder in, and the session is kept in that
# instance's own state directory so a restart does not ask again.
#
# Every host- or service-specific value is a variable below; nothing further
# down names a particular host, path or port, except the ones the unit template
# itself names (its User= and its ReadWritePaths=, which this script reads back
# out of the template rather than restating). The script is idempotent: every
# remote step either checks first or writes only what changed, so a second run
# against a converged host restarts nothing.
#
# WHY NO ACCOUNT IS NAMED HERE
#
# The unit is a TEMPLATE, deploy/bidder/socialweb-computer-bidder@.service. Its
# instance name %i is the handle of the account the bidder runs as, and every
# file that belongs to that account lives under
# /var/lib/socialweb-computer-bidder/<handle>, created by systemd from
# StateDirectory=. The account signs in by QR from the journal, so this script
# never holds anything of the account's: it installs the template, the
# launcher (deploy/bidder/socialweb-computer-bidder-run, installed as
# /usr/local/libexec/socialweb-computer-bidder-run) and the host's shared
# settings in /etc/socialweb-computer-bidder/common.env, and it never enables an
# instance. An instance that is already running is restarted when one of those
# three files changes, and only then.
#
# What a bidder does once it is up: it watches the market for
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
# Which image is shipped is the build host's own answer to the same question
# (socialweb-nodeimage -ensure -json -reuse-stale), not the first directory in
# the store.
#
# WHY THE GUEST KERNEL DOES NOT COME FROM THIS HOST
#
# The image store carries its own vmlinux (layout <out_dir>/sha256-<fingerprint>/vmlinux,
# socialweb-computer-kcp internal/nodefirecracker/spec.go:15). nodeboot loads the
# manifest out of the image directory and boots filepath.Join(ImageDir, "vmlinux")
# (cmd/socialweb-nodeboot/main.go:584), and the provider hands it nothing but
# image_dir. So the build host's Fedora kernel boots the guest on an Ubuntu 6.8
# host; the host kernel is only asked for KVM. A configuration that says
# "discover" for its kernel is shipped with the paths the build host's ensure
# resolved them to.
#
# WHY A SEPARATE ORG-ROOT COPY, AND A DENO OF ITS OWN
#
# A host may already run services out of /opt/org-root, and their deploys
# replace that tree wholesale. This one does not: it ships the import closure of
# atproto-market/hono-bidder into its own prefix, and the build host's deno
# binary beside it, so a deploy here cannot change what any other service reads
# on its next restart, and the bidder runs on the deno the code was run with.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ORG_ROOT="${ORG_ROOT:-$(dirname "$PROJECT_DIR")}"
BUILD_HOST_HOME="${HOME}"

# -- Target --------------------------------------------------------------------
# --local runs every remote step on this host through sudo -n instead of ssh.
SSH_TARGET="${SSH_TARGET:-root@socialweb.computer}"
SSH_PORT="${SSH_PORT:-1997}"
SSH_KEY="${SSH_KEY:-${HOME}/.ssh/id_ed25519_no_password}"
SSH_CONTROL_DIR="${SSH_CONTROL_DIR:-${HOME}/.ssh}"
SSH_OPTS="-o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 -o BatchMode=yes -o IdentitiesOnly=yes -o ControlMaster=auto -o ControlPath=${SSH_CONTROL_DIR}/cm-swc-%r@%h:%p -o ControlPersist=10m -o ServerAliveInterval=15"

# -- What gets installed -------------------------------------------------------
UNIT_TEMPLATE_SRC="${PROJECT_DIR}/deploy/bidder/socialweb-computer-bidder@.service"
LAUNCHER_SRC="${PROJECT_DIR}/deploy/bidder/socialweb-computer-bidder-run"
UNIT_TEMPLATE_DEST="/etc/systemd/system/socialweb-computer-bidder@.service"
LAUNCHER_DEST="/usr/local/libexec/socialweb-computer-bidder-run"
ETC_DIR="/etc/socialweb-computer-bidder"
COMMON_ENV="/etc/socialweb-computer-bidder/common.env"
INSTANCE_GLOB="socialweb-computer-bidder@*.service"
PREFIX="${PREFIX:-/opt/socialweb-computer-bidder}"

[ -f "${UNIT_TEMPLATE_SRC}" ] || { echo "missing: ${UNIT_TEMPLATE_SRC}" >&2; exit 1; }
[ -f "${LAUNCHER_SRC}" ] || { echo "missing: ${LAUNCHER_SRC}" >&2; exit 1; }
SERVICE_USER="$(sed -n 's/^User=//p' "${UNIT_TEMPLATE_SRC}")"
HOST_DIR="$(sed -n 's/^ReadWritePaths=//p' "${UNIT_TEMPLATE_SRC}")"
if [ -z "${SERVICE_USER}" ] || [ -z "${HOST_DIR}" ]; then
  echo "${UNIT_TEMPLATE_SRC} names no User= or no ReadWritePaths=; this script reads both from it" >&2
  exit 1
fi
STATE_ROOT="$(dirname "${HOST_DIR}")"

# Named BIDDER_* rather than after the flags they feed: the bidder CLI reads
# SERVE_PORT, POLICY and friends straight from its environment, and a knob that
# silently takes its value from an unrelated variable is worse than one that has
# no override at all. A per-account override goes in
# /etc/socialweb-computer-bidder/<handle>.env under the same names.
BIDDER_SERVE_ADDR="${BIDDER_SERVE_ADDR:-127.0.0.1}"
BIDDER_SERVE_PORT="${BIDDER_SERVE_PORT:-0}"
BIDDER_INGRESS_PROXY_HOST="${BIDDER_INGRESS_PROXY_HOST:-xrpc.fedproxy.com}"
BIDDER_PLC_DIRECTORY_URL="${BIDDER_PLC_DIRECTORY_URL:-https://plc.directory}"
BIDDER_POLICY="${BIDDER_POLICY:-open}"
BIDDER_POLICY_ARGS="${BIDDER_POLICY_ARGS:-{\"bidWindowSec\":8,\"firstFree\":true\}}"

# Derived remote paths. Everything shared by every instance lives under
# HOST_DIR, which is the unit's one ReadWritePaths= beside its own StateDirectory.
CODE_DIR="${PREFIX}/org-root"
DIST_DIR="${PREFIX}/dist"
DENO_BIN="${PREFIX}/bin/deno"
BIN_DIR="${HOST_DIR}/bin"
STORE_DIR="${HOST_DIR}/images"
INPUTS_DIR="${HOST_DIR}/image-inputs"
STAGE_DIR="${HOST_DIR}/stage"
CACHE_DIR="${HOST_DIR}/cache"
DENO_DIR_REMOTE="${HOST_DIR}/deno"
CONFIG_PATH="${HOST_DIR}/nodeimage-firecracker.json"
SHIM_PATH="${HOST_DIR}/undici-shim.ts"

# Paths as the RUNNER CONTAINER sees them. The provider only checks that the
# --compute-provider-firecracker-vmm and -nodeboot flags are non-empty
# (atproto-market hono-bidder/mod.ts:545-564); the VMM it actually execs is
# microvm-firecracker's DEFAULT_FIRECRACKER, /usr/local/bin/firecracker, resolved
# inside the container. Naming the in-container paths keeps the two honest about
# each other.
FIRECRACKER_IN_CONTAINER="${FIRECRACKER_IN_CONTAINER:-/usr/local/bin/firecracker}"
NODEBOOT_IN_CONTAINER="${NODEBOOT_IN_CONTAINER:-/usr/local/bin/socialweb-nodeboot}"

# -- Capacity ------------------------------------------------------------------
# The market has no capacity field: an offering carries only appliesTo and
# endpointUrl, and the bidder bids every compute.vm RFP its policy admits --
# there is no "does this fit" gate anywhere in lib/market-bidder. So capacity is
# not advertised to the market; it is enforced on the host, where the guests
# actually are. See the slice below. The budget is the host's, shared by every
# instance.
MAX_GUESTS="${MAX_GUESTS:-1}"
GUEST_VCPU="${GUEST_VCPU:-2}"
GUEST_MEM_MIB="${GUEST_MEM_MIB:-3072}"
# What a guest's container costs above the guest: the firecracker process, the
# fedora userland the entrypoint starts, and pasta.
RUNNER_OVERHEAD_MIB="${RUNNER_OVERHEAD_MIB:-1024}"
GUEST_SLICE="${GUEST_SLICE:-socialweb-guests.slice}"
# yes: point docker's cgroup-parent at the slice (writes /etc/docker/daemon.json
# when there is none and restarts docker). no: write the slice and leave docker
# alone, for a host whose docker runs other people's containers.
MANAGE_DOCKER_DAEMON="${MANAGE_DOCKER_DAEMON:-yes}"

# -- Build host inputs ---------------------------------------------------------
LOCAL_CONFIG="${LOCAL_CONFIG:-${BUILD_HOST_HOME}/kcp-image/nodeimage-market.json}"
LOCAL_PREINSTALL="${LOCAL_PREINSTALL:-${BUILD_HOST_HOME}/kcp-image/preinstall-market.json}"
LOCAL_INIT_BINARY="${LOCAL_INIT_BINARY:-${BUILD_HOST_HOME}/kcp-image/kcpinit}"
LOCAL_INIT_PACKAGE="${LOCAL_INIT_PACKAGE:-${ORG_ROOT}/socialweb-computer-kcp/cmd/socialweb-nodeinit}"
LOCAL_NODEIMAGE_BIN="${LOCAL_NODEIMAGE_BIN:-${BUILD_HOST_HOME}/kcp-image/socialweb-nodeimage}"
LOCAL_DENO="${LOCAL_DENO:-$(command -v deno || true)}"

RUNNER_IMAGE="${RUNNER_IMAGE:-localhost/socialweb-firecracker-runner:local}"
RANGE_BASE="${RANGE_BASE:-172.30.0.0}"

DRY_RUN=false
LOCAL=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    --local) LOCAL=true ;;
    -h|--help) sed -n '2,89p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

if ${LOCAL}; then
  TARGET_NAME="this host (sudo)"
else
  TARGET_NAME="${SSH_TARGET} -p ${SSH_PORT}"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

say() { printf '\n=== %s ===\n' "$*"; }

# Every single remote command goes through here, so --dry-run and --local are
# one branch each rather than flags threaded through forty steps.
remote() {
  if ${DRY_RUN}; then
    printf -- '--- [dry-run] on %s: %s\n' "${TARGET_NAME}" "$*"
  elif ${LOCAL}; then
    sudo -n bash -c "$*"
  else
    ssh ${SSH_OPTS} -i "${SSH_KEY}" -p "${SSH_PORT}" "${SSH_TARGET}" "$@"
  fi
}

# The local values are handed to the remote script as shell assignments rather
# than as environment, because the script is the thing being previewed and it
# should read the way it will run.
REMOTE_VARS="$(cat <<EOF
SERVICE_USER='${SERVICE_USER}'
PREFIX='${PREFIX}'
STATE_ROOT='${STATE_ROOT}'
HOST_DIR='${HOST_DIR}'
CODE_DIR='${CODE_DIR}'
DIST_DIR='${DIST_DIR}'
DENO_BIN='${DENO_BIN}'
BIN_DIR='${BIN_DIR}'
STORE_DIR='${STORE_DIR}'
INPUTS_DIR='${INPUTS_DIR}'
STAGE_DIR='${STAGE_DIR}'
CACHE_DIR='${CACHE_DIR}'
DENO_DIR_REMOTE='${DENO_DIR_REMOTE}'
CONFIG_PATH='${CONFIG_PATH}'
SHIM_PATH='${SHIM_PATH}'
ETC_DIR='${ETC_DIR}'
COMMON_ENV='${COMMON_ENV}'
UNIT_TEMPLATE_DEST='${UNIT_TEMPLATE_DEST}'
LAUNCHER_DEST='${LAUNCHER_DEST}'
INSTANCE_GLOB='${INSTANCE_GLOB}'
RUNNER_IMAGE='${RUNNER_IMAGE}'
GUEST_SLICE='${GUEST_SLICE}'
MAX_GUESTS='${MAX_GUESTS}'
GUEST_VCPU='${GUEST_VCPU}'
GUEST_MEM_MIB='${GUEST_MEM_MIB}'
RUNNER_OVERHEAD_MIB='${RUNNER_OVERHEAD_MIB}'
MANAGE_DOCKER_DAEMON='${MANAGE_DOCKER_DAEMON}'
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
    printf -- '--- [dry-run] on %s: %s <<REMOTE\n%s\n--- end\n' \
      "${TARGET_NAME}" "${desc}" "${full}"
  elif ${LOCAL}; then
    printf '%s\n' "${full}" | sudo -n bash -s
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
    printf -- '--- [dry-run] rsync (%s) %s -> %s:%s %s\n' "${desc}" "${src}" "${TARGET_NAME}" "${dest}" "$*"
  elif ${LOCAL}; then
    sudo -n rsync -a "$@" "${src}" "${dest}"
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

# -- Local preflight -----------------------------------------------------------
say "checking the build host"
echo "target       ${TARGET_NAME}"
echo "service user ${SERVICE_USER}"
echo "shared dir   ${HOST_DIR}"
command -v python3 >/dev/null 2>&1 || { echo "python3 is needed to rewrite the image configuration" >&2; exit 1; }
if ${LOCAL} && ! ${DRY_RUN}; then
  sudo -n true 2>/dev/null || { echo "--local runs every step through sudo -n, and sudo here would prompt" >&2; exit 1; }
fi

[ -n "${LOCAL_DENO}" ] || { echo "no deno on the build host's PATH; set LOCAL_DENO" >&2; exit 1; }
LOCAL_DENO="$(readlink -f "${LOCAL_DENO}")"
for required in "${LOCAL_CONFIG}" "${LOCAL_PREINSTALL}" "${LOCAL_INIT_BINARY}" \
                "${LOCAL_INIT_PACKAGE}" "${LOCAL_DENO}" "${ORG_ROOT}/atproto-market/deno.json"; do
  [ -e "${required}" ] || { echo "missing on the build host: ${required}" >&2; exit 1; }
done

# The builder binary the provider shells out to (-ensure -json). It is a HOST
# binary: it decides whether the store already holds the image, and with
# -reuse-stale it does not build.
# Always rebuilt: a binary left from an older tree reads an older config schema
# and refuses the current one.
say "building socialweb-nodeimage"
CGO_ENABLED=0 go build -C "${ORG_ROOT}/socialweb-computer-kcp" \
  -o "${LOCAL_NODEIMAGE_BIN}" ./cmd/socialweb-nodeimage

# Which image to ship is the build host's own answer, asked the way the target
# will ask it. It is run from inside the init's module so a relative
# init_package resolves the way go resolves it.
ENSURE_JSON="$(cd "${LOCAL_INIT_PACKAGE}" && "${LOCAL_NODEIMAGE_BIN}" -ensure -json -reuse-stale \
  -preinstall "${LOCAL_PREINSTALL}" -config "${LOCAL_CONFIG}" | tail -1)"
ensure_field() { python3 -c 'import json,sys;print(json.loads(sys.argv[1]).get(sys.argv[2]) or "")' "${ENSURE_JSON}" "$1"; }
LOCAL_STORE_IMAGE="$(ensure_field dir)"
FINGERPRINT="$(basename "${LOCAL_STORE_IMAGE}")"
LOCAL_KERNEL="$(ensure_field kernelSource)"
LOCAL_KERNEL_CONFIG="$(ensure_field kernelConfig)"
LOCAL_MODULES="$(ensure_field moduleSource)"
if ! [[ "${FINGERPRINT}" =~ ^sha256-[0-9a-f]{64}$ ]] || [ ! -d "${LOCAL_STORE_IMAGE}" ]; then
  echo "the build host's ensure named no whole image: ${ENSURE_JSON}" >&2
  exit 1
fi
echo "image        ${LOCAL_STORE_IMAGE} ($(ensure_field state))"

cfg_field() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])' "${LOCAL_CONFIG}" "$1"; }
LOCAL_BASE_IMAGE="$(cfg_field base_image)"
LOCAL_K3S="$(cfg_field k3s_binary)"
for required in "${LOCAL_KERNEL}" "${LOCAL_KERNEL_CONFIG}" "${LOCAL_MODULES}" \
                "${LOCAL_BASE_IMAGE}" "${LOCAL_K3S}"; do
  if [ ! -e "${required}" ]; then
    echo "the configuration at ${LOCAL_CONFIG} reads ${required}, which does not exist here." >&2
    echo "the store cannot be reused from a configuration whose inputs are gone: the host" >&2
    echo "recomputes the fingerprint, and it reads every one of them." >&2
    exit 1
  fi
done

# The runner image: fedora + passt + e2fsprogs + iproute + firecracker +
# socialweb-nodeboot (hono-compute-provider/hono-firecracker-standalone/Dockerfile),
# built by `deno task build:firecracker-runner` in hono-compute-provider.
LOCAL_RUNNER_ID=""
if docker image inspect "${RUNNER_IMAGE}" >/dev/null 2>&1; then
  LOCAL_RUNNER_ID="$(docker image inspect --format '{{json .RootFS.Layers}}' "${RUNNER_IMAGE}")"
elif ${DRY_RUN}; then
  LOCAL_RUNNER_ID="<missing here>"
else
  echo "the runner image ${RUNNER_IMAGE} is not present here. Build it with:" >&2
  echo "  cd ${ORG_ROOT}/hono-compute-provider && deno task build:firecracker-runner" >&2
  exit 1
fi
echo "runner image ${RUNNER_IMAGE}"

# -- The image builder's configuration, as the target will read it -------------
# Same fields, same bytes, same digests; only the paths move, and out_dir names
# the store that is about to be rsynced. The one input this cannot carry is the
# build host's /usr/bin/busybox, which is why the target runs with -reuse-stale
# instead of expecting the name to match.
say "rendering the target's copy of the image configuration"
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
cfg["init_binary"] = inputs + "/init/kcpinit"
cfg["init_package"] = inputs + "/init-package"
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

# -- The host's shared settings, as every instance will read them --------------
# Loaded by the unit as an EnvironmentFile, so values are quoted the way systemd
# unquotes them. DENO_DIR is read by deno itself; everything else by the
# launcher. Nothing of any account is in it.
say "rendering ${COMMON_ENV}"
{
  printf 'DENO_DIR=%s\n' "${DENO_DIR_REMOTE}"
  printf 'BIDDER_DENO=%s\n' "${DENO_BIN}"
  printf 'BIDDER_CODE_DIR=%s\n' "${CODE_DIR}"
  printf 'BIDDER_HOST_DIR=%s\n' "${HOST_DIR}"
  printf 'BIDDER_PRELOAD=%s\n' "${SHIM_PATH}"
  printf 'BIDDER_SERVE_ADDR=%s\n' "${BIDDER_SERVE_ADDR}"
  printf 'BIDDER_SERVE_PORT=%s\n' "${BIDDER_SERVE_PORT}"
  printf 'BIDDER_INGRESS_PROXY_HOST=%s\n' "${BIDDER_INGRESS_PROXY_HOST}"
  printf 'BIDDER_PLC_DIRECTORY_URL=%s\n' "${BIDDER_PLC_DIRECTORY_URL}"
  printf 'BIDDER_POLICY=%s\n' "${BIDDER_POLICY}"
  printf "BIDDER_POLICY_ARGS='%s'\n" "${BIDDER_POLICY_ARGS}"
  printf 'BIDDER_FC_NODEIMAGE=%s\n' "${BIN_DIR}/socialweb-nodeimage"
  printf 'BIDDER_FC_NODEBOOT=%s\n' "${NODEBOOT_IN_CONTAINER}"
  printf 'BIDDER_FC_CONFIG=%s\n' "${CONFIG_PATH}"
  printf 'BIDDER_FC_REPO_DIR=%s\n' "${HOST_DIR}"
  printf 'BIDDER_FC_PREINSTALL=%s\n' "${INPUTS_DIR}/preinstall.json"
  printf 'BIDDER_FC_VMM=%s\n' "${FIRECRACKER_IN_CONTAINER}"
  printf 'BIDDER_FC_RUNNER_IMAGE=%s\n' "${RUNNER_IMAGE}"
  printf 'BIDDER_FC_RANGE_BASE=%s\n' "${RANGE_BASE}"
} > "${WORK}/common.env"
sed 's/^/    /' "${WORK}/common.env"

# The undici preload. @atproto-labs/fetch-node's unicast guard reads
# process.versions.undici, which Deno does not define, so anything that builds an
# IdResolver throws "Unicast SSRF protection requires Node.js 20.6+" on Linux.
# The guard asks whether undici is patched; Deno's fetch is not undici, so the
# question does not apply.
cat > "${WORK}/undici-shim.ts" <<'SHIM'
const versions = process.versions as Record<string, string>;
if (!versions.undici) versions.undici = "6.11.1";
SHIM

# Checksums of the store, verified on the far side afterwards. This is the only
# thing that says the gigabytes that arrived are the gigabytes that left: rsync
# --sparse rebuilds the holes, and a truncated transfer still looks whole in a
# directory listing.
say "checksumming the image store"
( cd "$(dirname "${LOCAL_STORE_IMAGE}")" && find "${FINGERPRINT}" -type f -print0 \
    | sort -z | xargs -0 sha256sum ) > "${WORK}/image.sha256"
cat "${WORK}/image.sha256"

# -- Remote: account, directories, runtime -------------------------------------
say "creating the service account and its directories"
remote_script "account and directories" <<'REMOTE'
set -euo pipefail

# docker is the one runtime the provider's code calls: lib/container-backend-docker
# drives the docker CLI and starts the daemon itself if it is down. Ubuntu's
# docker.io is the same CLI and daemon. Idempotent.
if ! command -v docker >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq docker.io
  else
    echo "no docker here and no apt-get to install it with; install docker and run this again" >&2
    exit 1
  fi
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
# before a guest exists. Measured on that host with the container privileged,
# which is the other half of what pasta needs: at 1 the launcher cannot create
# its namespace, at 0 it can. A kernel without the knob (Fedora) has no such
# restriction, and gets no drop-in naming a key it does not have.
if [ -e /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]; then
  install -d -m 0755 /etc/sysctl.d
  if ! grep -qs '^kernel\.apparmor_restrict_unprivileged_userns' /etc/sysctl.d/99-socialweb-guest-userns.conf; then
    printf '%s\n' \
      '# Read by the firecracker bidder: a guest launcher runs unprivileged and pasta' \
      '# clones into a user namespace of its own before it starts the VMM.' \
      'kernel.apparmor_restrict_unprivileged_userns = 0' \
      > /etc/sysctl.d/99-socialweb-guest-userns.conf
  fi
  sysctl -q -w kernel.apparmor_restrict_unprivileged_userns=0 2>/dev/null || true
fi

# The bidders are not root: the unit drops each to this account and grants it
# nothing beyond the docker socket. Every instance runs as it, so instances are
# separated by their state directories, not by uid.
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --no-create-home \
    --home-dir "${HOST_DIR}" --shell /usr/sbin/nologin "${SERVICE_USER}"
fi
# Driving the daemon is what lets it start a guest's container. That membership
# is privileged by construction -- there is no narrower way to drive docker --
# and it is why this account exists for this service alone.
usermod -aG docker "${SERVICE_USER}"

install -d -m 755 -o root -g root "${PREFIX}" "${PREFIX}/bin" "${DIST_DIR}" "${CODE_DIR}" \
  "${STATE_ROOT}" "${ETC_DIR}" "$(dirname "${LAUNCHER_DEST}")"
install -d -m 755 -o "${SERVICE_USER}" -g "${SERVICE_USER}" \
  "${HOST_DIR}" "${BIN_DIR}" "${STORE_DIR}" "${STAGE_DIR}" "${CACHE_DIR}"
install -d -m 700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${DENO_DIR_REMOTE}"
install -d -m 755 -o root -g root "${INPUTS_DIR}" "${INPUTS_DIR}/kernel" \
  "${INPUTS_DIR}/base" "${INPUTS_DIR}/k3s" "${INPUTS_DIR}/init" "${INPUTS_DIR}/init-package"
echo "account ${SERVICE_USER} ready"
REMOTE

# -- Ship ----------------------------------------------------------------------
say "shipping the code the bidder imports, and the deno that runs it"
CLOSURE="$(code_closure)"
echo "${CLOSURE}" | sed 's/^/  /'
while read -r repo; do
  [ -n "${repo}" ] || continue
  rsync_to "code ${repo}" "${ORG_ROOT}/${repo}/" "${CODE_DIR}/${repo}/" \
    --delete --chown=root:root --exclude node_modules --exclude .git --exclude .codegraph
done <<< "${CLOSURE}"
rsync_to "deno" "${LOCAL_DENO}" "${DENO_BIN}" --copy-links --chown=root:root --chmod=F755

say "shipping the image store (${FINGERPRINT})"
rsync_to "image store" "${LOCAL_STORE_IMAGE}/" "${STORE_DIR}/${FINGERPRINT}/" --sparse

say "shipping the bytes the image was built from"
# Not needed to boot -- the guest's copy is already inside the image -- but the
# configuration on the target names these paths, and the builder reads every one
# of them to work out which image the configuration asks for. Naming the real
# files keeps the target's record of provenance true, so a later reader sees what
# the image is instead of what some other host's kernel happened to be.
rsync_to "kernel" "${LOCAL_KERNEL}" "${INPUTS_DIR}/kernel/vmlinuz" --copy-links
rsync_to "kernel config" "${LOCAL_KERNEL_CONFIG}" "${INPUTS_DIR}/kernel/config" --copy-links
# The tree has dangling build/ and source/ symlinks pointing at a source tree
# that is not there; -a preserves them as symlinks, which is what the build host
# read and what the digest is taken over.
rsync_to "kernel modules" "${LOCAL_MODULES}/" "${INPUTS_DIR}/modules/" --delete
rsync_to "base image" "${LOCAL_BASE_IMAGE}" "${INPUTS_DIR}/base/ubuntu-24.04.squashfs" --copy-links
rsync_to "k3s" "${LOCAL_K3S}" "${INPUTS_DIR}/k3s/k3s" --copy-links
rsync_to "guest init" "${LOCAL_INIT_BINARY}" "${INPUTS_DIR}/init/kcpinit" --copy-links
rsync_to "init package" "${LOCAL_INIT_PACKAGE}/" "${INPUTS_DIR}/init-package/" --delete
rsync_to "preinstall manifest" "${LOCAL_PREINSTALL}" "${INPUTS_DIR}/preinstall.json" --copy-links
rsync_to "image configuration" "${WORK}/nodeimage-firecracker.json" "${CONFIG_PATH}"
rsync_to "image builder" "${LOCAL_NODEIMAGE_BIN}" "${BIN_DIR}/socialweb-nodeimage" --copy-links
rsync_to "store checksums" "${WORK}/image.sha256" "${STORE_DIR}/.image.sha256"
rsync_to "undici preload" "${WORK}/undici-shim.ts" "${SHIM_PATH}"
rsync_to "unit template" "${UNIT_TEMPLATE_SRC}" "${DIST_DIR}/socialweb-computer-bidder@.service"
rsync_to "launcher" "${LAUNCHER_SRC}" "${DIST_DIR}/socialweb-computer-bidder-run"
rsync_to "shared settings" "${WORK}/common.env" "${DIST_DIR}/common.env"

say "verifying the store arrived intact"
remote_script "verify the store" <<'REMOTE'
set -euo pipefail
chown -R "${SERVICE_USER}:${SERVICE_USER}" "${STORE_DIR}" "${BIN_DIR}" "${STAGE_DIR}" \
  "${CACHE_DIR}" "${CONFIG_PATH}" "${SHIM_PATH}"
chown -R root:root "${INPUTS_DIR}" "${DIST_DIR}"
chmod -R a+rX "${INPUTS_DIR}"
# The image store is read by the CONTAINER that boots a guest, under whatever
# user that container maps to, so it cannot be owner-only: measured on a real
# deploy, the manifest at <store>/<fingerprint>/image.json answered
# "permission denied" and every provisioning failed with "the container that
# boots guest ... exited before it reported a guest".
chmod -R a+rX "${STORE_DIR}"
chmod 755 "${BIN_DIR}/socialweb-nodeimage"
cd "${STORE_DIR}"
sha256sum -c --quiet .image.sha256
echo "store verified"
REMOTE

# -- Capacity: the only place it can be enforced -------------------------------
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
# all: the file is either ours or an operator's, and merging JSON in shell is how
# an operator's file gets silently rewritten. If it is theirs, say exactly what
# to add. MANAGE_DOCKER_DAEMON=no skips this for a host whose docker runs other
# containers that must not move into the guests' budget or be restarted.
daemon_json=/etc/docker/daemon.json
docker_changed=no
if [ "${MANAGE_DOCKER_DAEMON}" != yes ]; then
  echo "MANAGE_DOCKER_DAEMON=${MANAGE_DOCKER_DAEMON}: ${daemon_json} left alone; guests are NOT bounded by ${GUEST_SLICE}"
elif [ ! -f "${daemon_json}" ]; then
  install -d -m 755 /etc/docker
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
  echo "Add \"cgroup-parent\": \"${GUEST_SLICE}\" to it by hand and run this again," >&2
  echo "or run with MANAGE_DOCKER_DAEMON=no. The file was left exactly as it was." >&2
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
if ${DRY_RUN}; then
  printf -- '--- [dry-run] on %s: docker image inspect %s   (load only if its layers differ)\n' \
    "${TARGET_NAME}" "${RUNNER_IMAGE}"
elif remote "docker image inspect --format '{{json .RootFS.Layers}}' '${RUNNER_IMAGE}'" 2>/dev/null \
    | grep -qxF "${LOCAL_RUNNER_ID}"; then
  echo "runner image ${RUNNER_IMAGE} already current"
elif ${LOCAL}; then
  echo "the runner image on this host's docker differs from the one this script just read" >&2
  exit 1
else
  docker save "${RUNNER_IMAGE}" \
    | ssh ${SSH_OPTS} -i "${SSH_KEY}" -p "${SSH_PORT}" "${SSH_TARGET}" "docker load"
  remote "docker image inspect --format '{{json .RootFS.Layers}}' '${RUNNER_IMAGE}'" \
    | grep -qxF "${LOCAL_RUNNER_ID}"
  echo "runner image ${RUNNER_IMAGE} loaded"
fi

# -- The template, the launcher and the shared settings ------------------------
say "installing the unit template, its launcher and the shared settings"
remote_script "template, launcher, common.env" <<'REMOTE'
set -euo pipefail
cd "${HOST_DIR}"

changed=no
place() {
  local src="$1" dest="$2" mode="$3"
  if cmp -s "${src}" "${dest}"; then
    echo "unchanged ${dest}"
  else
    install -m "${mode}" -o root -g root "${src}" "${dest}"
    echo "installed ${dest}"
    changed=yes
  fi
}
place "${DIST_DIR}/socialweb-computer-bidder@.service" "${UNIT_TEMPLATE_DEST}" 0644
place "${DIST_DIR}/socialweb-computer-bidder-run" "${LAUNCHER_DEST}" 0755
place "${DIST_DIR}/common.env" "${COMMON_ENV}" 0644

# Warm the module cache as the service user so the first start is not the thing
# that discovers a slow registry, and so a start that races Restart=always is
# racing a warm cache. Best effort: a network hiccup here must not fail a deploy
# that is otherwise converged.
runuser -u "${SERVICE_USER}" -- env HOME="${HOST_DIR}" DENO_DIR="${DENO_DIR_REMOTE}" \
  "${DENO_BIN}" cache --node-modules-dir=none --unstable-kv --unstable-worker-options \
  --config "${CODE_DIR}/atproto-market/deno.json" \
  "${CODE_DIR}/atproto-market/hono-bidder/mod.ts" >/dev/null 2>&1 \
  && echo "module cache warm" || echo "module cache NOT warmed; the first start fetches"

systemctl daemon-reload
running="$(systemctl list-units --type=service --state=running --no-legend --plain "${INSTANCE_GLOB}" | awk '{print $1}')"
if [ "${changed}" = yes ] && [ -n "${running}" ]; then
  # shellcheck disable=SC2086
  systemctl restart ${running}
  echo "restarted, because what they run changed: ${running}"
fi
echo "instances enabled: $(systemctl list-unit-files --no-legend --plain "${INSTANCE_GLOB}" 2>/dev/null | awk '$2=="enabled"{print $1}' | tr '\n' ' ')"
echo "instances running: ${running:-none}"
REMOTE

# -- Prove the target resolves the image the way the launcher says it will -----
say "asking the target which image its configuration names"
remote_script "ensure -reuse-stale" <<'REMOTE'
set -euo pipefail
cd "${HOST_DIR}"
# Runs as the service user, which is who will run it at boot, so this is the
# permission check as much as the resolution check. -reuse-stale against a store
# that already holds a whole image reads and returns; it does not build.
runuser -u "${SERVICE_USER}" -- env HOME="${HOST_DIR}" \
  "${BIN_DIR}/socialweb-nodeimage" -ensure -json -reuse-stale \
  -preinstall "${INPUTS_DIR}/preinstall.json" -config "${CONFIG_PATH}"
REMOTE

# -- Report --------------------------------------------------------------------
say "how to run a bidder for an account"
cat <<REPORT

  enable         sudo systemctl enable --now socialweb-computer-bidder@<handle>.service
  sign in        journalctl -u socialweb-computer-bidder@<handle> -f
                 scan the QR (or open the qr.fedfork.com URL) with <handle>
  stop           sudo systemctl disable --now socialweb-computer-bidder@<handle>.service

  <handle> is the account's handle in lowercase, e.g. alice.bsky.social. A sign-in
  by any other account is refused: the launcher deletes that session and the unit
  restarts into a fresh QR.

  Per account, under /var/lib/socialweb-computer-bidder/<handle>:
    secrets/oauth-session.json   the session the QR sign-in saved
    secrets/bidder-key.hex       the bidder's key; its did:plc is derived from it
    guests/                      one directory per running guest

  Per-account overrides go in ${ETC_DIR}/<handle>.env, same names as
  ${COMMON_ENV}. A second instance on one host must set its own
  BIDDER_FC_RANGE_BASE there, or its guests are handed addresses the first
  instance's guests already hold.

  These are the log lines a bidder emits:

    oauth_qr_awaiting_session           the QR is above it; scan it
    oauth_qr_session_restored           the saved session was reused
    bidder_account_verified             the session's handle is the instance's
    {"event":"bidder_ready",...}        the DID and the relay ingress it is
                                        reachable at
    bidder received VM RFP              an RFP arrived
    bidder created VM bid               the bid was written; bidUri names it
    bidder provisioned VM               the accept path reached the provider and a
                                        guest is booting
    bidder_account_refused              a sign-in named another account

  The image every instance boots from:
    ls ${STORE_DIR}/${FINGERPRINT}

  A guest that is running:
    docker ps --filter name=fc-

REPORT
