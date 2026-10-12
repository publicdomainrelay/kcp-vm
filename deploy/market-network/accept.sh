#!/usr/bin/env bash
set -euo pipefail

# Live acceptance for the AT Protocol market example, as one market network of
# possibly several on this host.
#
# A market network is a fully isolated atproto network: its own dev instance
# (socialweb-computer dev up: kine, kcp and the core, whose in-process deno
# runtime reconciles every DenoPod), its own OpenBao, a fake PLC, a relay, the
# dispatcher and its TLS front, two PDSes, MARKET_NETWORK_BIDDERS firecracker
# bidders and a host-side requester that rents a guest through the RFP flow
# (runComputeContract -> RFP -> bid -> accept -> cloud-init). It exits 0 only
# when every check passed.
#
# Nothing a market network starts has a fixed port. kcp, kine and OpenBao bind 0 and are
# read back; every service binds 0 and is read back from its DenoPod's
# status.listeners and status.address. Everything a market network owns hangs off MARKET_NETWORK_ID:
#
#   MARKET_NETWORK_DIR       $MARKET_NETWORK_STATE/market-networks/$MARKET_NETWORK_ID  (the dev instance
#                 under dev/, OpenBao, keys, guest CA, per-pod state, market-network.json)
#   market-network.json      every bound port and pid, the kcp/kine/OpenBao endpoints, the
#                 pinned runner image, the bidders' DIDs, timings and readings
#   FC_WORK_ROOT  /var/tmp/swc-$MARKET_NETWORK_ID (short: a guest's API socket stays under
#                 the 107-byte unix socket limit), symlinked as MARKET_NETWORK_DIR/guests
#   guests        docker label swc.market-network=$MARKET_NETWORK_ID; teardown sweeps by that label
#   slot          flock on $MARKET_NETWORK_SLOT_DIR/<i>.lock, i < SWC_MARKET_NETWORK_SLOTS, held for
#                 the market network's lifetime, refused below the free-memory budget
#
# MARKET_NETWORK_ID            [a-z0-9]{1,12} (default 8 random hex)
# MARKET_NETWORK_STATE         default $TASK_STATE_DIR, else $XDG_STATE_HOME/swc-market-networks
# MARKET_NETWORK_BIDDERS       bidders in this market network (default 1)
# MARKET_NETWORK_GUESTS        guests this market network may hold at once, for the memory budget (default 1)
# SWC_MARKET_NETWORK_SLOTS     market networks this host runs at once (default 4)
# MARKET_NETWORK_SLOT_DIR      default /var/tmp/socialweb-market-network-slots
# MARKET_NETWORK_SLOT          a slot the caller already holds (MARKET_NETWORK_SLOT_HELD=1): skip taking one
# MARKET_NETWORK_BASE_MIB      memory budget of a market network without guests (default 3072)
# MARKET_NETWORK_GUEST_MIB     memory budget of one guest (default 2560)
# MARKET_NETWORK_RECORD_OUT    copy the final market-network.json here before the market network directory goes
# KEEP_ACCEPT_ROOT  1 keeps MARKET_NETWORK_DIR (logs, run directories) after the run
# FC_RUNNER_IMAGE   runner image to pin by its image id; unset builds one with
#                   ts/hono-compute-provider/hono-firecracker-standalone/build.sh
# SOCIALWEB_KCP_PATH, SOCIALWEB_KINE_PATH, SOCIALWEB_DENO_PATH  the kcp, kine and
#                   pinned deno the dev instance runs (default: found on PATH)
# FC_VMRUNTIME_COMMAND JSON argv of `socialweb-computer internal vm {verb} ...`:
#                   guests are VM units of the Linux VM runtime (swc-userns-stage
#                   -> pasta -> firecracker, pinned guest image), not runner
#                   containers; no runner image is built (0007 M2-VM-RUNTIME)
#
# OpenBao starts first, so the core's deno runtime is given its address when
# dev up places the core; every DenoPod's serving certificate comes from it.

REPO=$(cd "$(dirname "$0")/../.." && pwd)
DENOKCP_DIR=$REPO/deploy/denokcp
MARKET_DIR=$(cd "$(dirname "$0")" && pwd)
MARKET_NETWORK_STATE=${MARKET_NETWORK_STATE:-${TASK_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/swc-market-networks}}
MARKET_NETWORK_ID=${MARKET_NETWORK_ID:-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')}
if ! [[ "$MARKET_NETWORK_ID" =~ ^[a-z0-9]{1,12}$ ]]; then
  echo "MARKET_NETWORK_ID '$MARKET_NETWORK_ID' is not 1-12 characters of [a-z0-9]" >&2
  exit 2
fi
MARKET_NETWORK_DIR=${ACCEPT_ROOT:-$MARKET_NETWORK_STATE/market-networks/$MARKET_NETWORK_ID}
ACCEPT_ROOT=$MARKET_NETWORK_DIR
MARKET_NETWORK_JSON=$MARKET_NETWORK_DIR/market-network.json
MARKET_NETWORK_BIDDERS=${MARKET_NETWORK_BIDDERS:-1}
MARKET_NETWORK_GUESTS=${MARKET_NETWORK_GUESTS:-1}
SWC_MARKET_NETWORK_SLOTS=${SWC_MARKET_NETWORK_SLOTS:-4}
MARKET_NETWORK_SLOT_DIR=${MARKET_NETWORK_SLOT_DIR:-/var/tmp/socialweb-market-network-slots}
MARKET_NETWORK_BASE_MIB=${MARKET_NETWORK_BASE_MIB:-3072}
MARKET_NETWORK_GUEST_MIB=${MARKET_NETWORK_GUEST_MIB:-2560}
MARKET_NETWORK_RECORD_OUT=${MARKET_NETWORK_RECORD_OUT:-}
KUBECTL=${KUBECTL:-kubectl}
WAIT_SECONDS=${WAIT_SECONDS:-240}
SETTLE_SECONDS=${SETTLE_SECONDS:-10}
# How long the EXIT trap waits for the deleted DenoPods to go away before it
# takes the dev instance down.
DELETE_SECONDS=${DELETE_SECONDS:-60}
KEEP_ACCEPT_ROOT=${KEEP_ACCEPT_ROOT:-0}
FC_WORK_ROOT=${FC_WORK_ROOT:-/var/tmp/swc-$MARKET_NETWORK_ID}
FC_RUNNER_IMAGE=${FC_RUNNER_IMAGE:-}
RUNNER_REPO=${RUNNER_REPO:-localhost/socialweb-firecracker-runner}
# How long the requester waits for the guest to report onNetwork, how long the
# whole contract may take, and how long a released guest's container may
# take to go away.
VM_READY_SECONDS=${VM_READY_SECONDS:-900}
MARKET_SECONDS=${MARKET_SECONDS:-1500}
GUEST_GONE_SECONDS=${GUEST_GONE_SECONDS:-180}
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$MARKET_NETWORK_DIR/guest-tls}

WORKSPACES="global relay alice bob"
BIDDERS=$(seq -f 'bidder-%g' 1 "$MARKET_NETWORK_BIDDERS" | tr '\n' ' ')
BIDDERS=${BIDDERS% }

DEV_STATE=$MARKET_NETWORK_DIR/dev
INSTANCE=market-network-$MARKET_NETWORK_ID
KUBECONFIG_PATH=""
RUNS_DIR=""
CORE_LOG=""
DEVSUP_PID=""
KCACHE=$MARKET_NETWORK_DIR/.kubectl-cache
OPENBAO_DIR=$MARKET_NETWORK_DIR/openbao
MARKET_NETWORK_OWNED=0

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# market-network.json is rewritten whole on every change (write, then rename), so a reader
# never sees half a record. Each argument is key.path=value; a value that
# parses as JSON is stored as that, anything else as a string.
network_set() {
  python3 - "$MARKET_NETWORK_JSON" "$@" <<'PY'
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

network_get() {
  python3 - "$MARKET_NETWORK_JSON" "$1" <<'PY'
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

if [ -s "$MARKET_NETWORK_JSON" ]; then
  other=$(network_get pid)
  if [ -n "$other" ] && kill -0 "$other" 2>/dev/null; then
    echo "market network $MARKET_NETWORK_ID is already up (pid $other, $MARKET_NETWORK_DIR)" >&2
    exit 2
  fi
fi

# The slot comes before anything is started, and so does the memory check: a
# market network refused here leaves nothing behind.
need_mib=$(( MARKET_NETWORK_BASE_MIB + MARKET_NETWORK_GUESTS * MARKET_NETWORK_GUEST_MIB ))
avail_mib=$(mem_available_mib)
if [ "$avail_mib" -lt "$need_mib" ]; then
  echo "refused: MemAvailable is ${avail_mib} MiB and market network $MARKET_NETWORK_ID needs ${need_mib} MiB (${MARKET_NETWORK_BASE_MIB} MiB + ${MARKET_NETWORK_GUESTS} guest(s) x ${MARKET_NETWORK_GUEST_MIB} MiB)" >&2
  exit 3
fi
if [ "${MARKET_NETWORK_SLOT_HELD:-0}" = "1" ]; then
  SLOT=${MARKET_NETWORK_SLOT:?MARKET_NETWORK_SLOT_HELD=1 needs MARKET_NETWORK_SLOT}
else
  mkdir -p "$MARKET_NETWORK_SLOT_DIR"
  chmod 0777 "$MARKET_NETWORK_SLOT_DIR" 2>/dev/null || true
  SLOT=""
  start=$(( $$ % SWC_MARKET_NETWORK_SLOTS ))
  for n in $(seq 0 $(( SWC_MARKET_NETWORK_SLOTS - 1 ))); do
    i=$(( (start + n) % SWC_MARKET_NETWORK_SLOTS ))
    exec 9>>"$MARKET_NETWORK_SLOT_DIR/$i.lock"
    if flock -n 9; then
      SLOT=$i
      : >"$MARKET_NETWORK_SLOT_DIR/$i.lock"
      echo "$MARKET_NETWORK_ID $$ $(now)" >>"$MARKET_NETWORK_SLOT_DIR/$i.lock"
      break
    fi
    exec 9>&-
  done
  if [ -z "$SLOT" ]; then
    echo "refused: every one of the $SWC_MARKET_NETWORK_SLOTS market network slots under $MARKET_NETWORK_SLOT_DIR is held:" >&2
    cat "$MARKET_NETWORK_SLOT_DIR"/*.lock >&2 2>/dev/null || true
    exit 3
  fi
fi

mkdir -p "$MARKET_NETWORK_DIR" "$KCACHE" "$FC_WORK_ROOT"
MARKET_NETWORK_OWNED=1
rm -f "$MARKET_NETWORK_JSON"
ln -sfn "$FC_WORK_ROOT" "$MARKET_NETWORK_DIR/guests"
fc_sock="$FC_WORK_ROOT/fc-000000000000-00000000/fc.sock"
if [ "${#fc_sock}" -gt 107 ]; then
  echo "FC_WORK_ROOT $FC_WORK_ROOT is too long for a guest's API socket (${#fc_sock} bytes)" >&2
  exit 2
fi
network_set "networkId=\"$MARKET_NETWORK_ID\"" "slot=$SLOT" "pid=$$" "startedAt=\"$(now)\"" "networkDir=\"$MARKET_NETWORK_DIR\"" \
  "sourceRoot=\"$REPO\"" "fcWorkRoot=\"$FC_WORK_ROOT\"" "kubeconfig=\"$KUBECONFIG_PATH\"" \
  "readings.memAvailableMiBAtStart=$avail_mib" "readings.budgetMiB=$need_mib"

KC() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" "$@"; }

exe_path() {
  local p
  p=$(command -v "$1" 2>/dev/null || true)
  [ -n "$p" ] && readlink -f "$p"
}

DEV_UP_STARTED=0
dev_up() {
  local report=$MARKET_NETWORK_DIR/dev-up.json
  DEV_UP_STARTED=1
  SOCIALWEB_KCP_PATH=${SOCIALWEB_KCP_PATH:-$(exe_path kcp)} \
  SOCIALWEB_KINE_PATH=${SOCIALWEB_KINE_PATH:-$(exe_path kine)} \
  SOCIALWEB_DENO_PATH=${SOCIALWEB_DENO_PATH:-$(exe_path deno)} \
  SOCIALWEB_COMPUTER_SOURCE_ROOT="$REPO" \
    "$SOCIALWEB_COMPUTER" dev up --instance "$INSTANCE" --state-dir "$DEV_STATE" --profile quick \
      --host-backend proc --openbao-addr "http://$OPENBAO_ADDR" --openbao-token-file "$OPENBAO_DIR/token" \
      --ready-timeout "${WAIT_SECONDS}s" >"$report"
  KUBECONFIG_PATH=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["adminKubeconfig"])' "$report")
  local state
  state=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["stateDir"])' "$report")
  DEVSUP_PID=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["devsup"]["pid"])' "$report")
  RUNS_DIR=$state/denokcp/runs
  CORE_LOG=$state/logs/core.log
  network_set "kubeconfig=\"$KUBECONFIG_PATH\"" "devInstance=\"$INSTANCE\"" "devStateDir=\"$state\"" \
    "processes.devsup.pid=$DEVSUP_PID" \
    "processes.kcp.address=\"$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["kcpURL"])' "$report")\"" \
    "processes.kine.address=\"$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["kineURL"])' "$report")\""
}

dev_down() {
  [ "$DEV_UP_STARTED" = "1" ] || return 0
  "$SOCIALWEB_COMPUTER" dev down --instance "$INSTANCE" --state-dir "$DEV_STATE" >"$MARKET_NETWORK_DIR/dev-down.json" 2>&1 || {
    echo "dev down of $INSTANCE failed:" >&2
    tail -n 20 "$MARKET_NETWORK_DIR/dev-down.json" >&2 || true
  }
  DEV_UP_STARTED=0
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

# Delete every DenoPod in every workspace through kcp first -- the
# denopod.deno.computer/run finalizer, which the deno runtime puts on every
# DenoPod, stops each process, and it only runs while the core is up -- wait,
# bounded, for the objects to disappear, and only then take the dev instance
# down (which also kills whatever is left of the core's deno processes). This
# does nothing when the cluster was never reached, so it never touches a kcp it
# did not start.
delete_workloads() {
  [ -n "${SERVER:-}" ] || return 0
  local ws deadline
  for ws in $WORKSPACES; do
    KC --server="$SERVER/clusters/root:$ws" delete denopods --all --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
  deadline=$(( $(date +%s) + DELETE_SECONDS ))
  while denopod_exists_any; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "workloads did not disappear within ${DELETE_SECONDS}s; taking the dev instance down anyway" >&2
      return 0
    fi
    sleep 2
  done
}

# Guests carry the label swc.market-network=$MARKET_NETWORK_ID, so this finds every container this
# market network's bidders started and nothing any other market network (or the production bidder)
# did.
own_guest_containers() {
  docker ps -a -q --filter "label=swc.market-network=$MARKET_NETWORK_ID" 2>/dev/null || true
}

own_guest_dirs() {
  [ -d "$FC_WORK_ROOT" ] || return 0
  find "$FC_WORK_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'fc-*' -printf '%f\n' | sort
}

stop_own_guests() {
  local c
  for c in $(own_guest_containers); do
    echo "removing guest container $c (label swc.market-network=$MARKET_NETWORK_ID), left behind by this market network" >&2
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  [ -n "${FC_VMRUNTIME_COMMAND:-}" ] || return 0
  local b n
  for b in $BIDDERS; do
    for n in $(vmruntime list "$b.bob.test" 2>/dev/null | jq -r '.[]?.name' 2>/dev/null); do
      echo "stopping VM $n of $b, left behind by this bed" >&2
      vmruntime stop "$b.bob.test" "$n" >/dev/null 2>&1 || true
    done
  done
}

# FC_VMRUNTIME_COMMAND set: the bed reaches the VM runtime with the same argv
# its bidders use, {verb} replaced, so teardown sees the units they started.
vmruntime() {
  local verb=$1 handle=$2
  shift 2
  local argv
  mapfile -t argv < <(jq -r --arg v "$verb" '.[] | if . == "{verb}" then $v else . end' <<<"$FC_VMRUNTIME_COMMAND")
  "${argv[@]}" --handle "$handle" "$@"
}

# Every pid in this market network: devsup and all its descendants (kine, kcp,
# the core, the pod scripts and the services they run) and OpenBao.
network_pids() {
  local roots=""
  [ -n "$DEVSUP_PID" ] && roots="$DEVSUP_PID"
  [ -s "$OPENBAO_DIR/bao.pid" ] && roots="$roots $(cat "$OPENBAO_DIR/bao.pid")"
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

network_rss_kib() {
  local pids
  pids=$(network_pids | tr '\n' ',')
  pids=${pids%,}
  [ -n "$pids" ] || { echo 0; return; }
  ps -o rss= -p "$pids" 2>/dev/null | awk '{ s += $1 } END { print s + 0 }'
}

guest_mem_kib() {
  local ids
  ids=$(docker ps -q --filter "label=swc.market-network=$MARKET_NETWORK_ID" 2>/dev/null || true)
  [ -n "$ids" ] || { echo 0; return; }
  docker stats --no-stream --format '{{.MemUsage}}' $ids 2>/dev/null | awk '
    { v = $1; u = v; gsub(/[0-9.]/, "", u); gsub(/[A-Za-z]/, "", v)
      m = 1; if (u == "KiB" || u == "kB") m = 1; else if (u == "MiB" || u == "MB") m = 1024; else if (u == "GiB" || u == "GB") m = 1048576; else if (u == "B") m = 1 / 1024
      s += v * m }
    END { printf "%d\n", s }'
}

SAMPLER_PID=""
start_sampler() {
  local out=$MARKET_NETWORK_DIR/rss.samples
  : >"$out"
  (
    while :; do
      echo "$(date +%s) $(network_rss_kib) $(guest_mem_kib)" >>"$out"
      { docker ps -a --filter "label=swc.market-network=$MARKET_NETWORK_ID" --format '{{.Names}}' 2>/dev/null; own_guest_dirs; } >>"$MARKET_NETWORK_DIR/guests.seen"
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
  if [ -s "$MARKET_NETWORK_DIR/rss.samples" ]; then
    read -r peak_network peak_guest <<<"$(awk '{ if ($2 > b) b = $2; if ($3 > g) g = $3 } END { print b + 0, g + 0 }' "$MARKET_NETWORK_DIR/rss.samples")"
    network_set "readings.rssPeakNetworkKiB=$peak_network" "readings.memPeakGuestKiB=$peak_guest"
  fi
}

listening() {
  [ -n "$(ss -Hltn "sport = :$1" 2>/dev/null)" ]
}

teardown_report() {
  local leftover_pids leftover_containers leftover_ports="" p
  leftover_pids=$(
    {
      pgrep -f -- "$MARKET_NETWORK_DIR" 2>/dev/null || true
      pgrep -f -- "$FC_WORK_ROOT" 2>/dev/null || true
      for p in $(network_get readings.pids); do
        kill -0 "$p" 2>/dev/null && echo "$p"
      done
    } | grep -vx "$$" | sort -u | tr '\n' ' '
  )
  leftover_containers=$(own_guest_containers | tr '\n' ' ')
  for p in $(python3 - "$MARKET_NETWORK_JSON" <<'PY'
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
  network_set "teardown.at=\"$(now)\"" "teardown.processes=\"${leftover_pids% }\"" \
    "teardown.containers=\"${leftover_containers% }\"" "teardown.ports=\"${leftover_ports# }\"" \
    "teardown.fcWorkRootLeft=$([ -e "$FC_WORK_ROOT" ] && echo true || echo false)"
  if [ -z "${leftover_pids// }" ] && [ -z "${leftover_containers// }" ] && [ -z "${leftover_ports// }" ] && [ ! -e "$FC_WORK_ROOT" ]; then
    echo "teardown: clean (no processes, containers, ports or guest work root of market network $MARKET_NETWORK_ID left)"
  else
    echo "teardown: LEFT processes=[${leftover_pids}] containers=[${leftover_containers}] ports=[${leftover_ports}] workroot=$([ -e "$FC_WORK_ROOT" ] && echo "$FC_WORK_ROOT")" >&2
  fi
}

cleanup() {
  local status=$?
  trap - EXIT
  if [ "$MARKET_NETWORK_OWNED" != "1" ]; then
    exit "$status"
  fi
  stop_sampler
  delete_workloads
  dev_down
  stop_own_guests
  if [ -s "$OPENBAO_DIR/bao.pid" ]; then
    local bao_pid
    bao_pid=$(cat "$OPENBAO_DIR/bao.pid")
    if [ -n "$bao_pid" ] && kill -0 "$bao_pid" 2>/dev/null; then
      kill "$bao_pid" 2>/dev/null || true
      for _ in $(seq 1 40); do kill -0 "$bao_pid" 2>/dev/null || break; sleep 0.25; done
    fi
  fi
  rm -rf "$FC_WORK_ROOT" 2>/dev/null || true
  if [ -e "$FC_WORK_ROOT" ]; then
    echo "guest work root $FC_WORK_ROOT could not be removed" >&2
  fi
  sleep 1
  teardown_report || true
  network_set "finishedAt=\"$(now)\"" "exitStatus=$status" || true
  if [ -n "$MARKET_NETWORK_RECORD_OUT" ]; then
    mkdir -p "$(dirname "$MARKET_NETWORK_RECORD_OUT")"
    cp "$MARKET_NETWORK_JSON" "$MARKET_NETWORK_RECORD_OUT" || true
  fi
  if [ "$KEEP_ACCEPT_ROOT" = "1" ]; then
    echo "kept $MARKET_NETWORK_DIR"
  else
    rm -rf "$MARKET_NETWORK_DIR"
  fi
  if [ "${MARKET_NETWORK_SLOT_HELD:-0}" != "1" ]; then
    : >"$MARKET_NETWORK_SLOT_DIR/$SLOT.lock" 2>/dev/null || true
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
# trapped signal until a foreground child returns: a market network told to stop while
# apply.sh or the requester runs would otherwise keep running until it does.
run_fg() {
  "$@" &
  FG_PID=$!
  local rc=0
  wait "$FG_PID" || rc=$?
  FG_PID=""
  return "$rc"
}

echo "market network: $MARKET_NETWORK_ID  slot: $SLOT  dir: $MARKET_NETWORK_DIR"
echo "guest work root: $FC_WORK_ROOT  bidders: $BIDDERS"
echo "memory: ${avail_mib} MiB available, market network budget ${need_mib} MiB"

echo
echo "== runner image =="
if [ -n "${FC_VMRUNTIME_COMMAND:-}" ]; then
  runner_id="none: VM runtime"
  pinned=""
elif [ -n "$FC_RUNNER_IMAGE" ]; then
  runner_id=$(docker image inspect -f '{{.Id}}' "$FC_RUNNER_IMAGE") || {
    echo "the firecracker runner image $FC_RUNNER_IMAGE is not in the local docker store" >&2
    exit 1
  }
  pinned="$RUNNER_REPO:sha-$(echo "${runner_id#sha256:}" | cut -c1-12)"
  docker tag "$FC_RUNNER_IMAGE" "$pinned"
else
  mkdir -p "$MARKET_NETWORK_STATE/build"
  pinned=$(
    flock 8
    RUNNER_BUILD_DIR="$MARKET_NETWORK_DIR/build/runner" NODEBOOT_SRC="${FC_REPO_DIR:?FC_REPO_DIR is required}" RUNNER_REPO="$RUNNER_REPO" \
      sh "$REPO/ts/hono-compute-provider/hono-firecracker-standalone/build.sh"
  ) 8>>"$MARKET_NETWORK_STATE/build/runner.lock"
  rm -rf "$MARKET_NETWORK_DIR/build/runner"
  runner_id=$(docker image inspect -f '{{.Id}}' "$pinned")
fi
FC_RUNNER_IMAGE=$pinned
network_set "runnerImage=\"$FC_RUNNER_IMAGE\"" "runnerImageId=\"$runner_id\""
echo "runner: $FC_RUNNER_IMAGE ($runner_id)"

echo
echo "== socialweb-computer =="
SOCIALWEB_COMPUTER=${SOCIALWEB_COMPUTER:-$MARKET_NETWORK_DIR/bin/socialweb-computer}
if [ ! -x "$SOCIALWEB_COMPUTER" ]; then
  mkdir -p "$(dirname "$SOCIALWEB_COMPUTER")"
  (cd "$REPO" && go build -o "$SOCIALWEB_COMPUTER" ./cmd/socialweb-computer)
fi
echo "binary: $SOCIALWEB_COMPUTER"

echo
echo "== openbao =="
OPENBAO_DIR="$OPENBAO_DIR" bash "$DENOKCP_DIR/start-openbao.sh"
OPENBAO_ADDR=$(cat "$OPENBAO_DIR/bao.addr")
(umask 077 && printf '%s\n' "${OPENBAO_TOKEN:-root}" >"$OPENBAO_DIR/token")
network_set "processes.openbao.pid=$(cat "$OPENBAO_DIR/bao.pid")" "processes.openbao.port=${OPENBAO_ADDR##*:}" \
  "processes.openbao.address=\"$OPENBAO_ADDR\""

echo
echo "== dev instance =="
dev_up
SERVER=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SERVER=${SERVER%%/clusters/*}
network_set "timings.kcpReady=\"$(now)\""
echo "instance: $INSTANCE  kcp: $SERVER  runs: $RUNS_DIR"

echo
echo "== apply =="
apply_status=0
KUBECONFIG="$KUBECONFIG_PATH" \
SOCIALWEB_COMPUTER="$SOCIALWEB_COMPUTER" \
MARKET_NETWORK_ID="$MARKET_NETWORK_ID" \
MARKET_NETWORK_DIR="$MARKET_NETWORK_DIR" \
MARKET_NETWORK_BIDDERS="$MARKET_NETWORK_BIDDERS" \
OPENBAO_DIR="$OPENBAO_DIR" \
START_OPENBAO=0 \
WAIT_SECONDS="$WAIT_SECONDS" \
OPENBAO_WAIT_SECONDS="${OPENBAO_WAIT_SECONDS:-60}" \
FC_WORK_ROOT="$FC_WORK_ROOT" \
FC_RUNNER_IMAGE="$FC_RUNNER_IMAGE" \
GUEST_TLS_DIR="$GUEST_TLS_DIR" \
  run_fg bash "$MARKET_DIR/apply.sh" || apply_status=$?

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
  if [ -n "$run" ] && [ -s "$RUNS_DIR/$run/state.json" ]; then
    pod_pid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid",""))' "$RUNS_DIR/$run/state.json")
    [ -n "$pod_pid" ] && child=$(pgrep -P "$pod_pid" 2>/dev/null | head -1 || true)
    child=${child:-$pod_pid}
  fi
  network_set "services.$ws/$name.address=\"$address\"" "services.$ws/$name.port=${address##*:}" \
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
  [ -s "$RUNS_DIR/$run/status.json" ] || return 0
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("did",""))' "$RUNS_DIR/$run/status.json" 2>/dev/null || true
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
network_set "bidders=$bidders_json"
HOST_IP=${HOST_IP:-$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')}
network_set "hostIp=\"$HOST_IP\""
PLC_PORT=$(network_get "services.global/plc.port")
DISPATCHER_PORT=$(network_get "services.relay/dispatcher.port")
GUEST_TLS_PORT=$(network_get "services.relay/dispatcher-tls.port")
BOB_PDS_PORT=$(network_get "services.bob/pds.port")
echo "plc=$PLC_PORT dispatcher=$DISPATCHER_PORT guest-tls=$GUEST_TLS_PORT bob-pds=$BOB_PDS_PORT bidders=$BIDDER_DIDS"
network_set "readings.rssSteadyNetworkKiB=$(network_rss_kib)" "readings.pids=\"$(network_pids | sort -n | tr '\n' ' ')\""

echo
echo "== checks =="

# The market CA -- the root every workspace's intermediate chains to -- read from
# the PKI mount in OpenBao's root namespace. A workload serves its intermediate
# alongside its leaf, so this one file is enough to verify any of the https
# listeners below.
CA_PEM=$MARKET_NETWORK_DIR/market-ca.pem
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
  network_set "checks.${1//[ .]/_}=\"$2\"" || true
  [ "$2" = "PASS" ] || FAILED=1
}

if [ "$apply_status" -eq 0 ]; then
  check "apply.sh" PASS "exit=$apply_status"
else
  check "apply.sh" FAIL "exit=$apply_status"
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
  listeners=$(KC --server="$SERVER/clusters/root:$ws" get denopod "$name" \
    -o jsonpath='{range .status.listeners[*]}{.name}={.kind}:{.address} {end}' 2>/dev/null || true)
  if [ -n "${listeners// /}" ]; then
    check "listeners root:$ws/$name" PASS "$listeners"
  else
    check "listeners root:$ws/$name" FAIL "status.listeners is empty"
  fi
  finalizers=$(KC --server="$SERVER/clusters/root:$ws" get denopod "$name" -o jsonpath='{.metadata.finalizers}' 2>/dev/null || true)
  case "$finalizers" in
    *denopod.deno.computer/run*) check "finalizer root:$ws/$name" PASS "denopod.deno.computer/run" ;;
    *) check "finalizer root:$ws/$name" FAIL "metadata.finalizers=${finalizers:-none}" ;;
  esac
done

vphase=$(pod_field alice verifier phase)
if [ "$vphase" = "Succeeded" ]; then
  check "denopod root:alice/verifier" PASS "phase=Succeeded"
else
  check "denopod root:alice/verifier" FAIL "phase=${vphase:-missing}"
fi

# Every port the market network recorded is non-zero and its own: a market network whose services
# reported nothing, or two of whose services claim one port, is not a market network.
record_problem=$(python3 - "$MARKET_NETWORK_JSON" <<'PY'
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
  check "market network record" PASS "every service reported its own port ($(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); print(len(r.get("services",{})))' "$MARKET_NETWORK_JSON") services)"
else
  check "market network record" FAIL "$record_problem"
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
  port=$(network_get "services.bob/$b.port")
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
  account=$(cat "$MARKET_NETWORK_DIR/state/bob-$b/account.did" 2>/dev/null || true)
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
  # A door held against this market network keeps each in-flight claim's secrets-server
  # key in the market network's own OpenBao, under a token that may touch nothing but
  # secret/socialweb-door/claims/*.
  DOOR_OPENBAO_TOKEN_FILE=$MARKET_NETWORK_DIR/door/openbao-token
  mkdir -p "$MARKET_NETWORK_DIR/door"
  door_bao() { BAO_ADDR="http://$OPENBAO_ADDR" BAO_TOKEN=root bao "$@"; }
  if ! door_bao secrets list -format=json 2>/dev/null | grep -q '"secret/"'; then
    door_bao secrets enable -path=secret kv-v2 >/dev/null
  fi
  door_bao policy write socialweb-door - >/dev/null <<'HCL'
path "secret/data/socialweb-door/claims/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "secret/metadata/socialweb-door/claims/*" {
  capabilities = ["read", "list", "delete"]
}
HCL
  door_bao token create -policy=socialweb-door -no-default-policy -ttl=24h -field=token >"$DOOR_OPENBAO_TOKEN_FILE.tmp"
  chmod 0644 "$DOOR_OPENBAO_TOKEN_FILE.tmp"
  mv "$DOOR_OPENBAO_TOKEN_FILE.tmp" "$DOOR_OPENBAO_TOKEN_FILE"
  {
    echo "ACCEPT_ROOT=$MARKET_NETWORK_DIR"
    echo "MARKET_NETWORK_ID=$MARKET_NETWORK_ID"
    echo "MARKET_NETWORK_JSON=$MARKET_NETWORK_JSON"
    echo "CA_PEM=$CA_PEM"
    echo "GUEST_TLS_DIR=$GUEST_TLS_DIR"
    echo "FC_WORK_ROOT=$FC_WORK_ROOT"
    echo "BIDDER_DID=${BIDDER_DIDS%%,*}"
    echo "BIDDER_DIDS=$BIDDER_DIDS"
    echo "BIDDER_STDOUT=$RUNS_DIR/$(pod_field bob bidder-1 runID)/stdout.txt"
    echo "PLC_PORT=$PLC_PORT"
    echo "DISPATCHER_PORT=$DISPATCHER_PORT"
    echo "GUEST_TLS_PORT=$GUEST_TLS_PORT"
    echo "ALICE_PDS_PORT=$(network_get "services.alice/pds.port")"
    echo "HOST_IP=$HOST_IP"
    echo "KUBECONFIG_PATH=$KUBECONFIG_PATH"
    echo "CORE_LOG=$CORE_LOG"
    echo "KCP_SERVER=$SERVER"
    echo "RUNS_DIR=$RUNS_DIR"
    echo "STATE_DIR=$MARKET_NETWORK_DIR/state"
    echo "OPENBAO_ADDR=$OPENBAO_ADDR"
    echo "DOOR_OPENBAO_TOKEN_FILE=$DOOR_OPENBAO_TOKEN_FILE"
    echo "CHECKS_FAILED=$FAILED"
  } >"$ACCEPT_HOLD_DIR/facts.env.tmp"
  mv "$ACCEPT_HOLD_DIR/facts.env.tmp" "$ACCEPT_HOLD_DIR/facts.env"
  echo "holding the market network up until $ACCEPT_HOLD_DIR/release exists"
  while [ ! -e "$ACCEPT_HOLD_DIR/release" ]; do
    sleep 2
  done
  echo "released after the hold"
fi

# socialweb-computer-kcp: one compute contract through the market network. A
# requester on the host (market-requester/mod.ts, runComputeContract) sends a
# signed RFP to the market network's bidders through the market network's dispatcher; they bid, the
# requester accepts one, and the winner boots a firecracker guest from the
# cloud-init user_data the requester composed. The guest reports onNetwork and
# its ssh host keys through the market network's relay; the requester reaches it over iroh
# with StrictHostKeyChecking=yes against the reported key, checks that a
# known_hosts holding another key is refused, releases the contract with
# vm.delete, and the guest's container and work directory must then be gone.
if [ -z "${ACCEPT_HOLD_DIR:-}" ]; then
echo
echo "== market contract =="
MARKET_RESULT=$MARKET_NETWORK_DIR/market-requester-result.json
MARKET_LOG=$MARKET_NETWORK_DIR/market-requester.log
guests_before=$(own_guest_dirs)
REQUESTER_LOCK=$MARKET_NETWORK_DIR/locks/requester.lock
mkdir -p "$MARKET_NETWORK_DIR/locks"
cp "$REPO/deno.lock" "$REQUESTER_LOCK"
echo "bidders: ${BIDDER_DIDS:-unknown}  guests reach the dispatcher at: ${HOST_IP:-unknown}"
market_status=0
start_sampler
if [ -n "$BIDDER_DIDS" ] && [ -n "$HOST_IP" ]; then
  market_started=$(date +%s)
  network_set "timings.requesterStarted=\"$(now)\""
  requester() {
  (cd "$REPO/ts/atproto-market" && DENO_CERT="$CA_PEM" KCP_DNS_TABLE_FILE="$RUNS_DIR/.kcpdns/table.json" \
    exec timeout "$MARKET_SECONDS" \
    deno run -A --node-modules-dir=none --config "$REPO/deno.json" --lock="$REQUESTER_LOCK" \
      --preload "$RUNS_DIR/.kcpdns/shim.ts" \
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
  network_set "timings.requesterFinished=\"$(now)\"" "readings.requesterSeconds=$(( $(date +%s) - market_started ))"
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
if [ "$(field sshVerified)" = "True" ]; then
  check "market guest ssh host key" PASS "StrictHostKeyChecking=yes against the reported key at $(field sshVerifiedAt): $(field hostKeyFingerprints)"
else
  check "market guest ssh host key" FAIL "reported=$(field hostKeyFingerprints) reason=$(field reason)"
fi
if [ "$(field mismatchRefused)" = "True" ]; then
  check "market guest ssh mismatch" PASS "a known_hosts with another key failed host-key verification at $(field mismatchRefusedAt)"
else
  check "market guest ssh mismatch" FAIL "reason=$(field reason)"
fi
network_set "contract.winnerDid=\"$(field winnerDid)\"" "contract.bids=\"$(field bids)\"" "contract.onNetworkMs=\"$(field onNetworkMs)\"" \
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
for g in $( { own_guest_dirs; cat "$MARKET_NETWORK_DIR/guests.seen" 2>/dev/null; } | sort -u); do
  case " $(echo $guests_before) $new_guests " in
    *" $g "*) ;;
    *) new_guests="$new_guests $g" ;;
  esac
done
new_guests=${new_guests# }
labelled=$(docker ps -a --filter "label=swc.market-network=$MARKET_NETWORK_ID" --format '{{.Names}}' 2>/dev/null | tr '\n' ' ')
gone_deadline=$(( $(date +%s) + GUEST_GONE_SECONDS ))
while :; do
  alive=""
  for c in $(docker ps -q --filter "label=swc.market-network=$MARKET_NETWORK_ID" 2>/dev/null); do alive="$alive $c"; done
  dirs=""
  for g in $new_guests; do [ -d "$FC_WORK_ROOT/$g" ] && dirs="$dirs $g"; done
  [ -z "$alive" ] && [ -z "$dirs" ] && break
  [ "$(date +%s)" -ge "$gone_deadline" ] && break
  sleep 2
done
stop_sampler
network_set "contract.guests=\"$new_guests\""
if [ -z "$new_guests" ]; then
  check "market guest gone" FAIL "no guest of market network $MARKET_NETWORK_ID was seen (labelled containers: ${labelled:-none})"
elif [ -z "$alive" ] && [ -z "$dirs" ]; then
  check "market guest gone" PASS "$new_guests: no running container labelled swc.market-network=$MARKET_NETWORK_ID and no work dir $(( GUEST_GONE_SECONDS - (gone_deadline - $(date +%s)) ))s after release"
else
  check "market guest gone" FAIL "still running:${alive:- none} work dirs left:${dirs:- none} after ${GUEST_GONE_SECONDS}s"
fi
if [ -n "${FC_VMRUNTIME_COMMAND:-}" ]; then
  left=""
  for b in $BIDDERS; do
    left="$left$(vmruntime list "$b.bob.test" 2>&1 | jq -r '.[]? | "\(.unit) loaded=\(.loaded) workDir=\(.workDir // "-")"' 2>/dev/null | tr '\n' ' ')"
  done
  if [ -z "${left// }" ]; then
    check "VM runtime left nothing" PASS "no VM unit and no runtime work dir of $BIDDERS"
  else
    check "VM runtime left nothing" FAIL "$left"
  fi
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
  echo "core log, tail of $CORE_LOG:"
  tail -n 40 "$CORE_LOG" || true
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
echo "market network record: $MARKET_NETWORK_JSON"
if [ "$FAILED" -eq 0 ]; then
  echo "accept: pass"
else
  echo "accept: fail"
fi
exit "$FAILED"
