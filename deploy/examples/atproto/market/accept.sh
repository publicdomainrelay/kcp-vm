#!/usr/bin/env bash
set -euo pipefail

# Live acceptance for the AT Protocol market example, as one market test bed of
# possibly several on this host.
#
# A bed is a fully isolated atproto network: its own kcp, kine and OpenBao, the
# deno-kcp provider, a fake PLC, a relay, the dispatcher and its TLS front, two
# PDSes, BED_BIDDERS firecracker bidders and a host-side requester that rents a
# guest through the RFP flow (runComputeContract -> RFP -> bid -> accept ->
# cloud-init). It exits 0 only when every check passed.
#
# Nothing a bed starts has a fixed port. kcp, kine and OpenBao bind 0 and are
# read back from their processes; every service binds 0 and is read back from
# its DenoPod's status.address. Everything a bed owns hangs off BED_ID:
#
#   BED_DIR       $BED_STATE/beds/$BED_ID  (kcp root, kine db, OpenBao, runs,
#                 keys, guest CA, per-pod state, logs, bed.json)
#   bed.json      every bound port and pid, the kcp/kine/OpenBao endpoints, the
#                 pinned runner image, the bidders' DIDs, timings and readings
#   FC_WORK_ROOT  /var/tmp/swc-$BED_ID (short: a guest's API socket stays under
#                 the 107-byte unix socket limit), symlinked as BED_DIR/guests
#   guests        docker label swc.bed=$BED_ID; teardown sweeps by that label
#   slot          flock on $BED_SLOT_DIR/<i>.lock, i < SWC_BED_SLOTS, held for
#                 the bed's lifetime, refused below the free-memory budget
#
# BED_ID            [a-z0-9]{1,12} (default 8 random hex)
# BED_STATE         default $TASK_STATE_DIR, else $XDG_STATE_HOME/swc-beds
# BED_BIDDERS       bidders in this bed (default 1)
# BED_GUESTS        guests this bed may hold at once, for the memory budget (default 1)
# SWC_BED_SLOTS     beds this host runs at once (default 4)
# BED_SLOT_DIR      default /var/tmp/socialweb-bedslots
# BED_SLOT          a slot the caller already holds (BED_SLOT_HELD=1): skip taking one
# BED_BASE_MIB      memory budget of a bed without guests (default 3072)
# BED_GUEST_MIB     memory budget of one guest (default 2560)
# BED_RECORD_OUT    copy the final bed.json here before the bed directory goes
# KEEP_ACCEPT_ROOT  1 keeps BED_DIR (logs, run directories) after the run
# FC_RUNNER_IMAGE   runner image to pin by its image id; unset builds one with
#                   hono-compute-provider/hono-firecracker-standalone/build.sh
#
# The provider is started here rather than by apply.sh, because a provider that
# comes up before its APIExport virtual workspace endpoints are published
# reconciles nothing and makes apply.sh fail on its first wait. accept.sh reads
# that state from the cluster and, while no workload exists yet, restarts the
# provider and runs apply.sh again, up to $MAX_ATTEMPTS times.

REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
DENOKCP_DIR=$REPO/deploy/denokcp
MARKET_DIR=$(cd "$(dirname "$0")" && pwd)
ORG_ROOT=${ORG_ROOT:-$(cd "$REPO/.." && pwd)}
BED_STATE=${BED_STATE:-${TASK_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/swc-beds}}
BED_ID=${BED_ID:-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')}
if ! [[ "$BED_ID" =~ ^[a-z0-9]{1,12}$ ]]; then
  echo "BED_ID '$BED_ID' is not 1-12 characters of [a-z0-9]" >&2
  exit 2
fi
BED_DIR=${ACCEPT_ROOT:-$BED_STATE/beds/$BED_ID}
ACCEPT_ROOT=$BED_DIR
BED_JSON=$BED_DIR/bed.json
BED_BIDDERS=${BED_BIDDERS:-1}
BED_GUESTS=${BED_GUESTS:-1}
SWC_BED_SLOTS=${SWC_BED_SLOTS:-4}
BED_SLOT_DIR=${BED_SLOT_DIR:-/var/tmp/socialweb-bedslots}
BED_BASE_MIB=${BED_BASE_MIB:-3072}
BED_GUEST_MIB=${BED_GUEST_MIB:-2560}
BED_RECORD_OUT=${BED_RECORD_OUT:-}
KUBECTL=${KUBECTL:-kubectl}
WAIT_SECONDS=${WAIT_SECONDS:-240}
SETTLE_SECONDS=${SETTLE_SECONDS:-10}
# How long to look at the cluster before calling a failing apply.sh stalled, and
# how many times apply.sh may be run at all while the provider reconciles
# nothing.
STALL_SECONDS=${STALL_SECONDS:-30}
MAX_ATTEMPTS=${MAX_ATTEMPTS:-3}
# How long the EXIT trap waits for the deleted DenoPods to go away before it
# stops the provider that owns their processes.
DELETE_SECONDS=${DELETE_SECONDS:-60}
DENO_KCP_PROVIDER=${DENO_KCP_PROVIDER:-}
KEEP_ACCEPT_ROOT=${KEEP_ACCEPT_ROOT:-0}
FC_WORK_ROOT=${FC_WORK_ROOT:-/var/tmp/swc-$BED_ID}
FC_RUNNER_IMAGE=${FC_RUNNER_IMAGE:-}
RUNNER_REPO=${RUNNER_REPO:-localhost/socialweb-firecracker-runner}
# How long the requester waits for the guest to report onNetwork, how long the
# whole contract may take, and how long a released guest's container may
# take to go away.
VM_READY_SECONDS=${VM_READY_SECONDS:-900}
MARKET_SECONDS=${MARKET_SECONDS:-1500}
GUEST_GONE_SECONDS=${GUEST_GONE_SECONDS:-180}
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$BED_DIR/guest-tls}
# The provider stops every deno process after --pod-timeout (5m by default),
# long-running DenoPods included, and restartPolicy Always then starts a fresh
# bidder that has forgotten every contract. A market contract outlives 5m, so
# the test bed's services are given a day.
POD_TIMEOUT=${POD_TIMEOUT:-24h}

WORKSPACES="global relay alice bob"
BIDDERS=$(seq -f 'bidder-%g' 1 "$BED_BIDDERS" | tr '\n' ' ')
BIDDERS=${BIDDERS% }

KUBECONFIG_PATH=$BED_DIR/kcp/admin.kubeconfig
KCACHE=$BED_DIR/.kubectl-cache
OPENBAO_DIR=$BED_DIR/openbao
PROVIDER_LOG=$BED_DIR/provider.log
PROVIDER_PID=""
ATTEMPT_STATUS=()
BED_OWNED=0

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# bed.json is rewritten whole on every change (write, then rename), so a reader
# never sees half a record. Each argument is key.path=value; a value that
# parses as JSON is stored as that, anything else as a string.
bed_set() {
  python3 - "$BED_JSON" "$@" <<'PY'
import json, os, sys
path = sys.argv[1]
try:
    with open(path) as f:
        record = json.load(f)
except Exception:
    record = {}
for pair in sys.argv[2:]:
    key, _, raw = pair.partition("=")
    try:
        value = json.loads(raw)
    except Exception:
        value = raw
    node = record
    parts = key.split(".")
    for part in parts[:-1]:
        node = node.setdefault(part, {})
    node[parts[-1]] = value
tmp = path + ".tmp"
with open(tmp, "w") as f:
    json.dump(record, f, indent=2, sort_keys=True)
    f.write("\n")
os.replace(tmp, path)
PY
}

bed_get() {
  python3 - "$BED_JSON" "$1" <<'PY'
import json, sys
try:
    node = json.load(open(sys.argv[1]))
    for part in sys.argv[2].split("."):
        node = node[part]
except Exception:
    node = ""
print(node if not isinstance(node, (dict, list)) else json.dumps(node))
PY
}

mem_available_mib() {
  awk '/^MemAvailable:/ { print int($2 / 1024) }' /proc/meminfo
}

if [ -s "$BED_JSON" ]; then
  other=$(bed_get pid)
  if [ -n "$other" ] && kill -0 "$other" 2>/dev/null; then
    echo "bed $BED_ID is already up (pid $other, $BED_DIR)" >&2
    exit 2
  fi
fi

# The slot comes before anything is started, and so does the memory check: a
# bed refused here leaves nothing behind.
need_mib=$(( BED_BASE_MIB + BED_GUESTS * BED_GUEST_MIB ))
avail_mib=$(mem_available_mib)
if [ "$avail_mib" -lt "$need_mib" ]; then
  echo "refused: MemAvailable is ${avail_mib} MiB and bed $BED_ID needs ${need_mib} MiB (${BED_BASE_MIB} MiB + ${BED_GUESTS} guest(s) x ${BED_GUEST_MIB} MiB)" >&2
  exit 3
fi
if [ "${BED_SLOT_HELD:-0}" = "1" ]; then
  SLOT=${BED_SLOT:?BED_SLOT_HELD=1 needs BED_SLOT}
else
  mkdir -p "$BED_SLOT_DIR"
  chmod 0777 "$BED_SLOT_DIR" 2>/dev/null || true
  SLOT=""
  start=$(( $$ % SWC_BED_SLOTS ))
  for n in $(seq 0 $(( SWC_BED_SLOTS - 1 ))); do
    i=$(( (start + n) % SWC_BED_SLOTS ))
    exec 9>>"$BED_SLOT_DIR/$i.lock"
    if flock -n 9; then
      SLOT=$i
      : >"$BED_SLOT_DIR/$i.lock"
      echo "$BED_ID $$ $(now)" >>"$BED_SLOT_DIR/$i.lock"
      break
    fi
    exec 9>&-
  done
  if [ -z "$SLOT" ]; then
    echo "refused: every one of the $SWC_BED_SLOTS market bed slots under $BED_SLOT_DIR is held:" >&2
    cat "$BED_SLOT_DIR"/*.lock >&2 2>/dev/null || true
    exit 3
  fi
fi

mkdir -p "$BED_DIR" "$KCACHE" "$FC_WORK_ROOT"
BED_OWNED=1
rm -f "$BED_JSON"
ln -sfn "$FC_WORK_ROOT" "$BED_DIR/guests"
fc_sock="$FC_WORK_ROOT/fc-000000000000-00000000/fc.sock"
if [ "${#fc_sock}" -gt 107 ]; then
  echo "FC_WORK_ROOT $FC_WORK_ROOT is too long for a guest's API socket (${#fc_sock} bytes)" >&2
  exit 2
fi
bed_set "bedId=\"$BED_ID\"" "slot=$SLOT" "pid=$$" "startedAt=\"$(now)\"" "bedDir=\"$BED_DIR\"" \
  "orgRoot=\"$ORG_ROOT\"" "fcWorkRoot=\"$FC_WORK_ROOT\"" "kubeconfig=\"$KUBECONFIG_PATH\"" \
  "readings.memAvailableMiBAtStart=$avail_mib" "readings.budgetMiB=$need_mib"

KC() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" "$@"; }

start_provider() {
  RUNS_DIR="$BED_DIR/runs" "$BED_DIR/deno-kcp-provider" \
    --kubeconfig="$KUBECONFIG_PATH" \
    --openbao-addr="http://$OPENBAO_ADDR" \
    --openbao-token=root \
    --pod-timeout="$POD_TIMEOUT" \
    >>"$PROVIDER_LOG" 2>&1 &
  PROVIDER_PID=$!
  bed_set "processes.provider.pid=$PROVIDER_PID"
}

stop_provider() {
  if [ -n "$PROVIDER_PID" ] && kill -0 "$PROVIDER_PID" 2>/dev/null; then
    kill "$PROVIDER_PID" 2>/dev/null || true
    wait "$PROVIDER_PID" 2>/dev/null || true
  fi
  PROVIDER_PID=""
}

denopod_exists_any() {
  local ws names
  for ws in $WORKSPACES; do
    names=$(KC --server="$SERVER/clusters/root:$ws" get denopods -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    if [ -n "$names" ]; then
      return 0
    fi
  done
  return 1
}

openbao_ready_any() {
  local ws
  for ws in $WORKSPACES; do
    if [ "$(KC --server="$SERVER/clusters/root:$ws" get openbao openbao -o jsonpath='{.status.ready}' 2>/dev/null || true)" = "true" ]; then
      return 0
    fi
  done
  return 1
}

# The DenoPod processes are children of the provider. Killing the provider
# first orphans them onto ports this bed recorded, and a run that leaks its
# workloads is not repeatable. Delete every DenoPod in every workspace through
# kcp first -- the denopod.deno.computer/run finalizer stops each process, and
# it only runs while the provider is up -- wait, bounded, for the objects to
# disappear, and only then stop the provider. This does nothing when the
# cluster was never reached, so it never touches a kcp it did not start.
delete_workloads() {
  [ -n "${SERVER:-}" ] || return 0
  local ws deadline
  for ws in $WORKSPACES; do
    KC --server="$SERVER/clusters/root:$ws" delete denopods --all --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
  deadline=$(( $(date +%s) + DELETE_SECONDS ))
  while denopod_exists_any; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "workloads did not disappear within ${DELETE_SECONDS}s; stopping the provider anyway" >&2
      return 0
    fi
    sleep 2
  done
}

# Guests carry the label swc.bed=$BED_ID, so this finds every container this
# bed's bidders started and nothing any other bed (or the production bidder)
# did.
own_guest_containers() {
  docker ps -a -q --filter "label=swc.bed=$BED_ID" 2>/dev/null || true
}

own_guest_dirs() {
  [ -d "$FC_WORK_ROOT" ] || return 0
  find "$FC_WORK_ROOT" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort
}

stop_own_guests() {
  local c
  for c in $(own_guest_containers); do
    echo "removing guest container $c (label swc.bed=$BED_ID), left behind by this bed" >&2
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
}

# Every pid in this bed: the provider and all its descendants (the pod scripts
# and the services they run), kcp, kine and OpenBao.
bed_pids() {
  local roots=""
  [ -n "$PROVIDER_PID" ] && roots="$PROVIDER_PID"
  for f in "$BED_DIR/kcp/kcp.pid" "$BED_DIR/kcp/kine.pid" "$OPENBAO_DIR/bao.pid"; do
    [ -s "$f" ] && roots="$roots $(cat "$f")"
  done
  ps -eo pid=,ppid= | awk -v roots="$roots" '
    BEGIN { n = split(roots, r, " "); for (i = 1; i <= n; i++) keep[r[i]] = 1 }
    { parent[$1] = $2; pids[NR] = $1 }
    END {
      changed = 1
      while (changed) {
        changed = 0
        for (i in pids) { p = pids[i]; if (!(p in keep) && (parent[p] in keep)) { keep[p] = 1; changed = 1 } }
      }
      for (p in keep) if (p != "") print p
    }'
}

bed_rss_kib() {
  local pids
  pids=$(bed_pids | tr '\n' ',')
  pids=${pids%,}
  [ -n "$pids" ] || { echo 0; return; }
  ps -o rss= -p "$pids" 2>/dev/null | awk '{ s += $1 } END { print s + 0 }'
}

guest_mem_kib() {
  local ids
  ids=$(docker ps -q --filter "label=swc.bed=$BED_ID" 2>/dev/null || true)
  [ -n "$ids" ] || { echo 0; return; }
  docker stats --no-stream --format '{{.MemUsage}}' $ids 2>/dev/null | awk '
    { v = $1; u = v; gsub(/[0-9.]/, "", u); gsub(/[A-Za-z]/, "", v)
      m = 1; if (u == "KiB" || u == "kB") m = 1; else if (u == "MiB" || u == "MB") m = 1024; else if (u == "GiB" || u == "GB") m = 1048576; else if (u == "B") m = 1 / 1024
      s += v * m }
    END { printf "%d\n", s }'
}

SAMPLER_PID=""
start_sampler() {
  local out=$BED_DIR/rss.samples
  : >"$out"
  (
    while :; do
      echo "$(date +%s) $(bed_rss_kib) $(guest_mem_kib)" >>"$out"
      { docker ps -a --filter "label=swc.bed=$BED_ID" --format '{{.Names}}' 2>/dev/null; own_guest_dirs; } >>"$BED_DIR/guests.seen"
      sleep 3
    done
  ) &
  SAMPLER_PID=$!
}

stop_sampler() {
  if [ -n "$SAMPLER_PID" ]; then
    kill "$SAMPLER_PID" 2>/dev/null || true
    wait "$SAMPLER_PID" 2>/dev/null || true
    SAMPLER_PID=""
  fi
  if [ -s "$BED_DIR/rss.samples" ]; then
    read -r peak_bed peak_guest <<<"$(awk '{ if ($2 > b) b = $2; if ($3 > g) g = $3 } END { print b + 0, g + 0 }' "$BED_DIR/rss.samples")"
    bed_set "readings.rssPeakBedKiB=$peak_bed" "readings.memPeakGuestKiB=$peak_guest"
  fi
}

listening() {
  [ -n "$(ss -Hltn "sport = :$1" 2>/dev/null)" ]
}

teardown_report() {
  local leftover_pids leftover_containers leftover_ports="" p
  leftover_pids=$(
    {
      pgrep -f -- "$BED_DIR" 2>/dev/null || true
      pgrep -f -- "$FC_WORK_ROOT" 2>/dev/null || true
      for p in $(bed_get readings.pids); do
        kill -0 "$p" 2>/dev/null && echo "$p"
      done
    } | grep -vx "$$" | sort -u | tr '\n' ' '
  )
  leftover_containers=$(own_guest_containers | tr '\n' ' ')
  for p in $(python3 - "$BED_JSON" <<'PY'
import json, sys
try:
    r = json.load(open(sys.argv[1]))
except Exception:
    r = {}
for group in ("processes", "services"):
    for v in (r.get(group) or {}).values():
        if isinstance(v, dict) and v.get("port"):
            print(v["port"])
PY
  ); do
    listening "$p" && leftover_ports="$leftover_ports $p"
  done
  bed_set "teardown.at=\"$(now)\"" "teardown.processes=\"${leftover_pids% }\"" \
    "teardown.containers=\"${leftover_containers% }\"" "teardown.ports=\"${leftover_ports# }\"" \
    "teardown.fcWorkRootLeft=$([ -e "$FC_WORK_ROOT" ] && echo true || echo false)"
  if [ -z "${leftover_pids// }" ] && [ -z "${leftover_containers// }" ] && [ -z "${leftover_ports// }" ] && [ ! -e "$FC_WORK_ROOT" ]; then
    echo "teardown: clean (no processes, containers, ports or guest work root of bed $BED_ID left)"
  else
    echo "teardown: LEFT processes=[${leftover_pids}] containers=[${leftover_containers}] ports=[${leftover_ports}] workroot=$([ -e "$FC_WORK_ROOT" ] && echo "$FC_WORK_ROOT")" >&2
  fi
}

cleanup() {
  local status=$?
  trap - EXIT
  if [ "$BED_OWNED" != "1" ]; then
    exit "$status"
  fi
  stop_sampler
  delete_workloads
  stop_provider
  stop_own_guests
  if [ -s "$OPENBAO_DIR/bao.pid" ]; then
    local bao_pid
    bao_pid=$(cat "$OPENBAO_DIR/bao.pid")
    if [ -n "$bao_pid" ] && kill -0 "$bao_pid" 2>/dev/null; then
      kill "$bao_pid" 2>/dev/null || true
      for _ in $(seq 1 40); do kill -0 "$bao_pid" 2>/dev/null || break; sleep 0.25; done
    fi
  fi
  ROOT="$BED_DIR/kcp" bash "$DENOKCP_DIR/stop-kcp.sh" >/dev/null || true
  rm -rf "$FC_WORK_ROOT" 2>/dev/null || true
  if [ -e "$FC_WORK_ROOT" ]; then
    echo "guest work root $FC_WORK_ROOT could not be removed" >&2
  fi
  sleep 1
  teardown_report || true
  bed_set "finishedAt=\"$(now)\"" "exitStatus=$status" || true
  if [ -n "$BED_RECORD_OUT" ]; then
    mkdir -p "$(dirname "$BED_RECORD_OUT")"
    cp "$BED_JSON" "$BED_RECORD_OUT" || true
  fi
  if [ "$KEEP_ACCEPT_ROOT" = "1" ]; then
    echo "kept $BED_DIR"
  else
    rm -rf "$BED_DIR"
  fi
  if [ "${BED_SLOT_HELD:-0}" != "1" ]; then
    : >"$BED_SLOT_DIR/$SLOT.lock" 2>/dev/null || true
    exec 9>&- 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT
FG_PID=""
on_signal() {
  [ -n "$FG_PID" ] && kill -TERM "$FG_PID" 2>/dev/null
  exit 130
}
trap on_signal INT TERM

# Long steps run as a background job the shell waits on, because bash defers a
# trapped signal until a foreground child returns: a bed told to stop while
# apply.sh or the requester runs would otherwise keep running until it does.
run_fg() {
  "$@" &
  FG_PID=$!
  local rc=0
  wait "$FG_PID" || rc=$?
  FG_PID=""
  return "$rc"
}

echo "bed: $BED_ID  slot: $SLOT  dir: $BED_DIR"
echo "guest work root: $FC_WORK_ROOT  bidders: $BIDDERS"
echo "memory: ${avail_mib} MiB available, bed budget ${need_mib} MiB"

echo
echo "== runner image =="
if [ -n "$FC_RUNNER_IMAGE" ]; then
  runner_id=$(docker image inspect -f '{{.Id}}' "$FC_RUNNER_IMAGE") || {
    echo "the firecracker runner image $FC_RUNNER_IMAGE is not in the local docker store" >&2
    exit 1
  }
  pinned="$RUNNER_REPO:sha-$(echo "${runner_id#sha256:}" | cut -c1-12)"
  docker tag "$FC_RUNNER_IMAGE" "$pinned"
else
  mkdir -p "$BED_STATE/build"
  pinned=$(
    flock 8
    RUNNER_BUILD_DIR="$BED_DIR/build/runner" NODEBOOT_SRC="${FC_REPO_DIR:?FC_REPO_DIR is required}" RUNNER_REPO="$RUNNER_REPO" \
      sh "$ORG_ROOT/hono-compute-provider/hono-firecracker-standalone/build.sh"
  ) 8>>"$BED_STATE/build/runner.lock"
  rm -rf "$BED_DIR/build/runner"
  runner_id=$(docker image inspect -f '{{.Id}}' "$pinned")
fi
FC_RUNNER_IMAGE=$pinned
bed_set "runnerImage=\"$FC_RUNNER_IMAGE\"" "runnerImageId=\"$runner_id\""
echo "runner: $FC_RUNNER_IMAGE ($runner_id)"

echo
echo "== kcp =="
ROOT="$BED_DIR/kcp" bash "$DENOKCP_DIR/start-kcp.sh"
KCP_PORT=$(cat "$BED_DIR/kcp/kcp.port")
KINE_ENDPOINT=$(cat "$BED_DIR/kcp/kine.endpoint")
bed_set "processes.kcp.pid=$(cat "$BED_DIR/kcp/kcp.pid")" "processes.kcp.port=$KCP_PORT" \
  "processes.kine.pid=$(cat "$BED_DIR/kcp/kine.pid")" "processes.kine.port=${KINE_ENDPOINT##*:}" \
  "processes.kine.address=\"${KINE_ENDPOINT#http://}\"" "timings.kcpReady=\"$(now)\""

echo
echo "== provider workspace and schemas =="
KUBECONFIG_PATH="$KUBECONFIG_PATH" bash "$DENOKCP_DIR/install-provider.sh"

echo
echo "== provider build =="
if [ -z "$DENO_KCP_PROVIDER" ]; then
  recorded() { sed -n "s/^ *\"$1\": *\"\([0-9a-f]*\)\".*/\1/p" "$DENOKCP_DIR/vendor.json" | head -1; }
  patches=$(cat "$DENOKCP_DIR"/kcp-libs-*.patch 2>/dev/null | sha256sum | cut -c1-12)
  DENO_KCP_PROVIDER="$BED_STATE/build/deno-kcp-provider-$(recorded commit | cut -c1-12)-$(recorded kcpLibsCommit | cut -c1-12)-$patches"
  mkdir -p "$BED_STATE/build"
  (
    flock 8
    if [ ! -x "$DENO_KCP_PROVIDER" ]; then
      ORG_ROOT="$ORG_ROOT" WORK_DIR="$BED_DIR/deno-kcp-src" \
        bash "$DENOKCP_DIR/build-provider.sh" "$DENO_KCP_PROVIDER.tmp"
      mv "$DENO_KCP_PROVIDER.tmp" "$DENO_KCP_PROVIDER"
      rm -rf "$BED_DIR/deno-kcp-src"
    fi
  ) 8>>"$BED_STATE/build/provider.lock"
fi
cp "$DENO_KCP_PROVIDER" "$BED_DIR/deno-kcp-provider"
echo "provider: $DENO_KCP_PROVIDER"

echo
echo "== openbao =="
OPENBAO_DIR="$OPENBAO_DIR" bash "$DENOKCP_DIR/start-openbao.sh"
OPENBAO_ADDR=$(cat "$OPENBAO_DIR/bao.addr")
bed_set "processes.openbao.pid=$(cat "$OPENBAO_DIR/bao.pid")" "processes.openbao.port=${OPENBAO_ADDR##*:}" \
  "processes.openbao.address=\"$OPENBAO_ADDR\""

# The provider reconciles nothing when it starts before its APIExports publish
# their virtual workspace endpoints, and the retry below only notices after a
# full WAIT_SECONDS. kcp publishes an export's endpoint only once a workspace
# binds it, so the market's workspaces are created here (apply.sh applies the
# same file again, unchanged) and the endpoints are waited for, bounded,
# before the provider starts.
echo
echo "== export endpoints =="
SERVER=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SERVER=${SERVER%%/clusters/*}
KC --server="$SERVER/clusters/root" apply --validate=false -f "$MARKET_DIR/00-workspaces.yaml"
urls=""
for _ in $(seq 1 120); do
  urls=$(KC --server="$SERVER/clusters/root:deno-provider" \
    get apiexportendpointslices -o jsonpath='{.items[*].status.endpoints[*].url}' 2>/dev/null || true)
  [ "$(echo "$urls" | wc -w)" -ge 2 ] && break
  sleep 1
done
echo "endpoints: ${urls:-none}"

echo
echo "== provider =="
start_provider

echo
echo "== apply =="
# The provider can come up before its APIExport virtual workspace endpoints are
# published. It then logs that it is waiting for them and reconciles nothing:
# every object apply.sh applies stays without a status and apply.sh fails on its
# first wait. That state is read from the cluster, not from one log line -- no
# OpenBao ready and no DenoPod anywhere, seen over a bounded window -- and only
# then is the provider stopped, started again and apply.sh run again. It is never
# restarted once a DenoPod exists, because the workloads are children of the
# provider and stopping it would orphan them.
apply_status=0
attempt=0
while :; do
  attempt=$((attempt + 1))
  echo "apply attempt $attempt of $MAX_ATTEMPTS"
  apply_status=0
  ORG_ROOT="$ORG_ROOT" \
  KUBECONFIG="$KUBECONFIG_PATH" \
  BED_ID="$BED_ID" \
  BED_DIR="$BED_DIR" \
  BED_BIDDERS="$BED_BIDDERS" \
  OPENBAO_DIR="$OPENBAO_DIR" \
  START_OPENBAO=0 \
  WAIT_SECONDS="$WAIT_SECONDS" \
  OPENBAO_WAIT_SECONDS="${OPENBAO_WAIT_SECONDS:-60}" \
  FC_WORK_ROOT="$FC_WORK_ROOT" \
  FC_RUNNER_IMAGE="$FC_RUNNER_IMAGE" \
  GUEST_TLS_DIR="$GUEST_TLS_DIR" \
    run_fg bash "$MARKET_DIR/apply.sh" || apply_status=$?
  ATTEMPT_STATUS+=("$apply_status")
  if [ "$apply_status" -eq 0 ]; then
    break
  fi

  stalled=1
  deadline=$(( $(date +%s) + STALL_SECONDS ))
  while :; do
    if openbao_ready_any || denopod_exists_any; then
      stalled=0
      break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      break
    fi
    sleep 2
  done

  if [ "$stalled" -eq 0 ]; then
    echo "apply.sh failed but the provider is reconciling; leaving it running" >&2
    break
  fi
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "apply.sh failed $attempt times with no OpenBao ready and no DenoPod in any workspace" >&2
    break
  fi
  echo "no workload exists and nothing reconciled within ${STALL_SECONDS}s; restarting the provider and applying again" >&2
  stop_provider
  start_provider
done
bed_set "timings.applied=\"$(now)\""

# apply.sh failing is not a reason to skip the checks: what came up and what did
# not is the evidence, and it is read the same way either way.
echo
echo "== record =="

pod_field() {
  KC --server="$SERVER/clusters/root:$1" get denopod "$2" -o jsonpath="{.status.$3}" 2>/dev/null || true
}

# A pod's run directory is named by its status.runID; its state.json holds the
# pid of the pod script, whose child is the service itself.
record_pod() {
  local ws=$1 name=$2 address run pod_pid child=""
  address=$(pod_field "$ws" "$name" address)
  run=$(pod_field "$ws" "$name" runID)
  pod_pid=""
  if [ -n "$run" ] && [ -s "$BED_DIR/runs/$run/state.json" ]; then
    pod_pid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid",""))' "$BED_DIR/runs/$run/state.json")
    [ -n "$pod_pid" ] && child=$(pgrep -P "$pod_pid" 2>/dev/null | head -1 || true)
    child=${child:-$pod_pid}
  fi
  bed_set "services.$ws/$name.address=\"$address\"" "services.$ws/$name.port=${address##*:}" \
    "services.$ws/$name.runId=\"$run\"" "services.$ws/$name.podPid=${pod_pid:-0}" "services.$ws/$name.pid=${child:-0}"
}

PODS=("global plc" "relay relay" "relay dispatcher" "relay dispatcher-tls" "alice pds" "bob pds")
for b in $BIDDERS; do PODS+=("bob $b"); done
for entry in "${PODS[@]}"; do
  set -- $entry
  record_pod "$1" "$2"
done

bidder_did() {
  local run did
  run=$(pod_field bob "$1" runID)
  [ -n "$run" ] || return 0
  grep -h '"event":"bidder_ready"' "$BED_DIR/runs/$run/stdout.txt" 2>/dev/null | grep -o 'did:plc:[a-z2-7]\{24\}' | head -1 || true
}

BIDDER_DIDS=""
bidders_json="["
for b in $BIDDERS; do
  did=""
  for _ in $(seq 1 60); do
    did=$(bidder_did "$b")
    [ -n "$did" ] && break
    sleep 2
  done
  address=$(pod_field bob "$b" address)
  BIDDER_DIDS="${BIDDER_DIDS:+$BIDDER_DIDS,}$did"
  bidders_json="$bidders_json{\"name\":\"$b\",\"did\":\"$did\",\"port\":${address##*:}},"
done
bidders_json="${bidders_json%,}]"
bed_set "bidders=$bidders_json"
HOST_IP=${HOST_IP:-$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')}
bed_set "hostIp=\"$HOST_IP\""
PLC_PORT=$(bed_get "services.global/plc.port")
DISPATCHER_PORT=$(bed_get "services.relay/dispatcher.port")
GUEST_TLS_PORT=$(bed_get "services.relay/dispatcher-tls.port")
BOB_PDS_PORT=$(bed_get "services.bob/pds.port")
echo "plc=$PLC_PORT dispatcher=$DISPATCHER_PORT guest-tls=$GUEST_TLS_PORT bob-pds=$BOB_PDS_PORT bidders=$BIDDER_DIDS"
bed_set "readings.rssSteadyBedKiB=$(bed_rss_kib)" "readings.pids=\"$(bed_pids | sort -n | tr '\n' ' ')\""

echo
echo "== checks =="

# The market CA -- the root every workspace's intermediate chains to -- read from
# the PKI mount in OpenBao's root namespace. A workload serves its intermediate
# alongside its leaf, so this one file is enough to verify any of the https
# listeners below.
CA_PEM=$BED_DIR/market-ca.pem
if ! curl -sS --fail --header "X-Vault-Token: root" \
    "http://$OPENBAO_ADDR/v1/pki/ca/pem" -o "$CA_PEM" 2>/dev/null || [ ! -s "$CA_PEM" ]; then
  echo "could not read the market CA from http://$OPENBAO_ADDR/v1/pki/ca/pem; the https host checks below cannot verify the leaf" >&2
fi

# The status curl printed, and 000 exactly once when the fetch failed.
# Verification stays on: the leaf is signed by the workspace's OpenBao
# authority for the service's cluster-local name, which does not resolve on the
# host, so the caller passes --cacert with the market CA and --resolve to the
# loopback address and port the pod reported.
http_code() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$@" 2>/dev/null) || true
  echo "${code:-000}"
}

RESULTS=()
FAILED=0
check() {
  RESULTS+=("$(printf '%-36s %-4s %s' "$1" "$2" "$3")")
  bed_set "checks.${1//[ .]/_}=\"$2\"" || true
  [ "$2" = "PASS" ] || FAILED=1
}

if [ "$apply_status" -eq 0 ]; then
  check "apply.sh" PASS "exit=$apply_status attempts=${ATTEMPT_STATUS[*]}"
else
  check "apply.sh" FAIL "exit=$apply_status attempts=${ATTEMPT_STATUS[*]}"
fi

for entry in "${PODS[@]}"; do
  set -- $entry
  ws=$1; name=$2
  phase=$(pod_field "$ws" "$name" phase)
  ready=$(pod_field "$ws" "$name" ready)
  if [ "$phase" = "Running" ] && [ "$ready" = "true" ]; then
    check "denopod root:$ws/$name" PASS "phase=Running ready=true address=$(pod_field "$ws" "$name" address)"
  else
    check "denopod root:$ws/$name" FAIL "phase=${phase:-missing} ready=${ready:-missing}"
  fi
done

vphase=$(pod_field alice verifier phase)
if [ "$vphase" = "Succeeded" ]; then
  check "denopod root:alice/verifier" PASS "phase=Succeeded"
else
  check "denopod root:alice/verifier" FAIL "phase=${vphase:-missing}"
fi

# Every port the bed recorded is non-zero and its own: a bed whose services
# reported nothing, or two of whose services claim one port, is not a bed.
record_problem=$(python3 - "$BED_JSON" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
owner = {}
for group in ("processes", "services"):
    for name, v in (r.get(group) or {}).items():
        port = int(v.get("port") or 0)
        if group == "services" and port <= 0:
            print(f"{name} reported no port"); sys.exit()
        if port and port in owner:
            print(f"{owner[port]} and {name} share port {port}"); sys.exit()
        if port:
            owner[port] = name
print("")
PY
)
if [ -z "$record_problem" ]; then
  check "bed record" PASS "every service reported its own port ($(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(len(r.get("services",{})))' "$BED_JSON") services)"
else
  check "bed record" FAIL "$record_problem"
fi

ready=$(pod_field bob pds ready)
if [ "$ready" = "true" ]; then
  check "bob pds on its name" PASS "ready=true probe=kcpdns pds.default.bob.svc.kcp.local /xrpc/_health"
else
  check "bob pds on its name" FAIL "ready=${ready:-missing} probe=kcpdns pds.default.bob.svc.kcp.local /xrpc/_health"
fi

code=$(http_code --cacert "$CA_PEM" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
  "https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health")
if [ "$code" = "200" ]; then
  check "bob pds on the host" PASS "GET https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health -> $code"
else
  check "bob pds on the host" FAIL "GET https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health -> $code"
fi

for b in $BIDDERS; do
  ready=$(pod_field bob "$b" ready)
  if [ "$ready" = "true" ]; then
    check "$b on its name" PASS "ready=true probe=kcpdns $b.default.bob.svc.kcp.local /oauth-client-metadata.json"
  else
    check "$b on its name" FAIL "ready=${ready:-missing} probe=kcpdns $b.default.bob.svc.kcp.local"
  fi
  port=$(bed_get "services.bob/$b.port")
  code=$(http_code "http://127.0.0.1:$port/oauth-client-metadata.json")
  if [ "$code" = "200" ]; then
    check "$b on the host" PASS "GET http://127.0.0.1:$port/oauth-client-metadata.json -> $code"
  else
    check "$b on the host" FAIL "GET http://127.0.0.1:$port/oauth-client-metadata.json -> $code"
  fi
done

# Each bidder signed in to the account apply.sh created for it on bob's PDS,
# and its offering is listed by that PDS rather than by a repo of its own.
bob_pds_get() {
  curl -sS --max-time 10 --cacert "$CA_PEM" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
    "https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/$1" 2>/dev/null || true
}
for b in $BIDDERS; do
  account=$(cat "$BED_DIR/state/bob-$b/account.did" 2>/dev/null || true)
  did=$(bidder_did "$b")
  offerings=$(bob_pds_get "com.atproto.repo.listRecords?repo=$did&collection=com.publicdomainrelay.temp.market.offering" \
    | python3 -c 'import json,sys
try:
    print(len(json.load(sys.stdin).get("records") or []))
except Exception:
    print(0)')
  if [ -n "$account" ] && [ "$did" = "$account" ] && [ "${offerings:-0}" -gt 0 ]; then
    check "$b account on bob pds" PASS "did=$did offerings on pds.default.bob=$offerings"
  else
    check "$b account on bob pds" FAIL "account=${account:-none} bidder=${did:-none} offerings on pds.default.bob=${offerings:-0}"
  fi
done

# A pod that dies after startup passes a single read. Reading the long-running
# pods again after a pause is what catches it.
echo "waiting ${SETTLE_SECONDS}s, then reading the long-running pods again"
sleep "$SETTLE_SECONDS"
for entry in "${PODS[@]}"; do
  set -- $entry
  ws=$1; name=$2
  phase=$(pod_field "$ws" "$name" phase)
  ready=$(pod_field "$ws" "$name" ready)
  if [ "$phase" = "Running" ] && [ "$ready" = "true" ]; then
    check "still up root:$ws/$name" PASS "phase=Running ready=true after ${SETTLE_SECONDS}s"
  else
    check "still up root:$ws/$name" FAIL "phase=${phase:-missing} ready=${ready:-missing} after ${SETTLE_SECONDS}s"
  fi
done

if [ -n "${ACCEPT_HOLD_DIR:-}" ]; then
  echo
  echo "== hold =="
  mkdir -p "$ACCEPT_HOLD_DIR"
  {
    echo "ACCEPT_ROOT=$BED_DIR"
    echo "BED_ID=$BED_ID"
    echo "BED_JSON=$BED_JSON"
    echo "CA_PEM=$CA_PEM"
    echo "GUEST_TLS_DIR=$GUEST_TLS_DIR"
    echo "FC_WORK_ROOT=$FC_WORK_ROOT"
    echo "BIDDER_DID=${BIDDER_DIDS%%,*}"
    echo "BIDDER_DIDS=$BIDDER_DIDS"
    echo "BIDDER_STDOUT=$BED_DIR/runs/$(pod_field bob bidder-1 runID)/stdout.txt"
    echo "PLC_PORT=$PLC_PORT"
    echo "DISPATCHER_PORT=$DISPATCHER_PORT"
    echo "GUEST_TLS_PORT=$GUEST_TLS_PORT"
    echo "ALICE_PDS_PORT=$(bed_get "services.alice/pds.port")"
    echo "HOST_IP=$HOST_IP"
    echo "KUBECONFIG_PATH=$KUBECONFIG_PATH"
    echo "PROVIDER_LOG=$PROVIDER_LOG"
    echo "CHECKS_FAILED=$FAILED"
  } >"$ACCEPT_HOLD_DIR/facts.env.tmp"
  mv "$ACCEPT_HOLD_DIR/facts.env.tmp" "$ACCEPT_HOLD_DIR/facts.env"
  echo "holding the test bed up until $ACCEPT_HOLD_DIR/release exists"
  while [ ! -e "$ACCEPT_HOLD_DIR/release" ]; do
    sleep 2
  done
  echo "released after the hold"
fi

# socialweb-computer-kcp: one compute contract through the test bed. A
# requester on the host (market-requester/mod.ts, runComputeContract) sends a
# signed RFP to the bed's bidders through the bed's dispatcher; they bid, the
# requester accepts one, and the winner boots a firecracker guest from the
# cloud-init user_data the requester composed. The guest reports onNetwork
# through the bed's relay, the requester releases the contract with vm.delete
# without ever opening a session to it, and the guest's container and work
# directory must then be gone.
if [ -z "${ACCEPT_HOLD_DIR:-}" ]; then
echo
echo "== market contract =="
MARKET_RESULT=$BED_DIR/market-requester-result.json
MARKET_LOG=$BED_DIR/market-requester.log
guests_before=$(own_guest_dirs)
echo "bidders: ${BIDDER_DIDS:-unknown}  guests reach the dispatcher at: ${HOST_IP:-unknown}"
market_status=0
start_sampler
if [ -n "$BIDDER_DIDS" ] && [ -n "$HOST_IP" ]; then
  market_started=$(date +%s)
  bed_set "timings.requesterStarted=\"$(now)\""
  requester() {
  (cd "$ORG_ROOT/atproto-market" && DENO_CERT="$CA_PEM" KCP_DNS_TABLE_FILE="$BED_DIR/runs/.kcpdns/table.json" \
    exec timeout "$MARKET_SECONDS" \
    deno run -A --node-modules-dir=none --config "$ORG_ROOT/atproto-market/deno.json" \
      --preload "$BED_DIR/runs/.kcpdns/shim.ts" \
      "$MARKET_DIR/market-requester/mod.ts" \
      --plc-directory-url "https://127.0.0.1:$PLC_PORT" \
      --ingress-proxy-host "relay.localhost:$DISPATCHER_PORT" \
      --bidder-dids "$BIDDER_DIDS" \
      --guest-host-aliases "$HOST_IP relay.localhost" \
      --guest-ca-file "$GUEST_TLS_DIR/ca.crt" \
      --guest-curl-home /root \
      --guest-journal-to-console \
      --guest-diagnose-url "https://relay.localhost:$GUEST_TLS_PORT/.well-known/did.json" \
      --vm-ready-timeout-sec "$VM_READY_SECONDS" \
      --result-file "$MARKET_RESULT") >"$MARKET_LOG" 2>&1
  }
  run_fg requester || market_status=$?
  echo "requester exit=$market_status after $(( $(date +%s) - market_started ))s, log $MARKET_LOG"
  bed_set "timings.requesterFinished=\"$(now)\"" "readings.requesterSeconds=$(( $(date +%s) - market_started ))"
fi
field() {
  python3 -c 'import json,sys
try:
    v = json.load(open(sys.argv[1])).get(sys.argv[2])
except Exception:
    v = None
print("" if v is None else v)' "$MARKET_RESULT" "$1"
}
on_network=$(field onNetwork)
if [ -n "$on_network" ]; then
  check "market guest onNetwork" PASS "after $(field onNetworkMs)ms: ${on_network:0:60} winner=$(field winnerDid) bids=$(field bids)"
else
  check "market guest onNetwork" FAIL "bidders=${BIDDER_DIDS:-unknown} exit=$market_status reason=$(field reason)"
fi
bed_set "contract.winnerDid=\"$(field winnerDid)\"" "contract.bids=\"$(field bids)\"" "contract.onNetworkMs=\"$(field onNetworkMs)\"" \
  "contract.onNetwork=\"$on_network\"" "contract.vmDeleteStatus=\"$(field vmDeleteStatus)\"" "contract.totalMs=\"$(field totalMs)\"" \
  "contract.startedAt=\"$(field startedAt)\"" "contract.finishedAt=\"$(field finishedAt)\""
receipt_uri=$(field receiptUri)
receipt_repo=$(printf '%s' "$receipt_uri" | sed -n 's|^at://\([^/]*\)/\([^/]*\)/\([^/]*\)$|\1|p')
receipt_rkey=$(printf '%s' "$receipt_uri" | sed -n 's|^at://\([^/]*\)/\([^/]*\)/\([^/]*\)$|\3|p')
receipt_code=$(http_code --cacert "$CA_PEM" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
  "https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/com.atproto.repo.getRecord?repo=$receipt_repo&collection=com.publicdomainrelay.temp.market.receipt&rkey=$receipt_rkey")
if [ -n "$receipt_rkey" ] && [ "$receipt_code" = "200" ]; then
  check "market receipt on bob pds" PASS "getRecord $receipt_uri -> $receipt_code"
else
  check "market receipt on bob pds" FAIL "getRecord ${receipt_uri:-<no receipt>} -> $receipt_code"
fi
if [ "$(field vmDeleteOk)" = "True" ] && [ "$market_status" -eq 0 ]; then
  check "market contract released" PASS "vm.delete status=$(field vmDeleteStatus) total=$(field totalMs)ms"
else
  check "market contract released" FAIL "vm.delete status=$(field vmDeleteStatus) reason=$(field reason)"
fi
new_guests=""
for g in $( { own_guest_dirs; cat "$BED_DIR/guests.seen" 2>/dev/null; } | sort -u); do
  case " $(echo $guests_before) $new_guests " in
    *" $g "*) ;;
    *) new_guests="$new_guests $g" ;;
  esac
done
new_guests=${new_guests# }
labelled=$(docker ps -a --filter "label=swc.bed=$BED_ID" --format '{{.Names}}' 2>/dev/null | tr '\n' ' ')
gone_deadline=$(( $(date +%s) + GUEST_GONE_SECONDS ))
while :; do
  alive=""
  for c in $(docker ps -q --filter "label=swc.bed=$BED_ID" 2>/dev/null); do alive="$alive $c"; done
  dirs=""
  for g in $new_guests; do [ -d "$FC_WORK_ROOT/$g" ] && dirs="$dirs $g"; done
  [ -z "$alive" ] && [ -z "$dirs" ] && break
  [ "$(date +%s)" -ge "$gone_deadline" ] && break
  sleep 2
done
stop_sampler
bed_set "contract.guests=\"$new_guests\""
if [ -z "$new_guests" ]; then
  check "market guest gone" FAIL "no guest of bed $BED_ID was seen (labelled containers: ${labelled:-none})"
elif [ -z "$alive" ] && [ -z "$dirs" ]; then
  check "market guest gone" PASS "$new_guests: no running container labelled swc.bed=$BED_ID and no work dir $(( GUEST_GONE_SECONDS - (gone_deadline - $(date +%s)) ))s after release"
else
  check "market guest gone" FAIL "still running:${alive:- none} work dirs left:${dirs:- none} after ${GUEST_GONE_SECONDS}s"
fi
fi

echo
echo "results:"
printf '  %-36s %-4s %s\n' "check" "result" "evidence"
for line in "${RESULTS[@]}"; do
  echo "  $line"
done

if [ "$FAILED" -ne 0 ]; then
  echo
  echo "provider log, tail of $PROVIDER_LOG:"
  tail -n 40 "$PROVIDER_LOG" || true
  echo
  echo "status of the pods that are not Running and ready:"
  for entry in "${PODS[@]}" "alice verifier"; do
    set -- $entry
    ws=$1; name=$2
    phase=$(pod_field "$ws" "$name" phase)
    ready=$(pod_field "$ws" "$name" ready)
    if [ "$phase" = "Running" ] && [ "$ready" = "true" ] || [ "$phase" = "Succeeded" ]; then
      continue
    fi
    echo "root:$ws $name:"
    KC --server="$SERVER/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status}' 2>&1 | sed 's/^/  /' || true
    echo
  done
fi

echo
echo "bed record: $BED_JSON"
if [ "$FAILED" -eq 0 ]; then
  echo "accept: pass"
else
  echo "accept: fail"
fi
exit "$FAILED"
