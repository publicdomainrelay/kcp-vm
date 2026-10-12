#!/usr/bin/env bash
set -euo pipefail

# Live acceptance of one market network, of possibly several on this host.
#
# A market network is a MarketNetwork application: `socialweb-computer dev up
# --profile market-network` brings up its own dev instance (kine, kcp and the
# core) and applies its root object. The core's application controller runs the
# shared atproto services (PLC, relay, dispatcher and its TLS front, two PDSes,
# the verifier) as DenoPods of the network's own kcp, an OpenBao for their
# certificates as a HostProcess, and composes MARKET_NETWORK_BIDDERS MarketBidder
# child applications. Each child runs its own kcp (its bootstrap layer is
# HostProcess objects in the network's kcp, root:dev:<child>) and learns the
# network only from the MarketNetwork value the composer copies onto its root
# object. A host-side requester then rents a guest through the RFP flow
# (runComputeContract -> RFP -> bid -> accept -> cloud-init) and releases it.
# It exits 0 only when every check passed.
#
# Nothing a market network starts has a fixed port: every listener binds 0 and
# is read back from kcp status. Everything a market network owns hangs off
# MARKET_NETWORK_ID:
#
#   MARKET_NETWORK_DIR       $MARKET_NETWORK_STATE/market-networks/$MARKET_NETWORK_ID
#   market-network.json      every bound port and pid, the kcp endpoints, the
#                            children's kcps, the bidders' DIDs, timings, readings
#   FC_WORK_ROOT             /var/tmp/swc-$MARKET_NETWORK_ID
#   guests                   VM units of the bidders' handles (internal vm list); teardown stops them
#   slot                     flock on $MARKET_NETWORK_SLOT_DIR/<i>.lock
#
# MARKET_NETWORK_ID            [a-z0-9]{1,12} (default 8 random hex)
# MARKET_NETWORK_STATE         default $TASK_STATE_DIR, else $XDG_STATE_HOME/swc-market-networks
# MARKET_NETWORK_BIDDERS       bidder child applications (default 1)
# MARKET_NETWORK_GATEWAY       1 also composes a Gateway child application
# MARKET_NETWORK_GUESTS        guests this market network may hold at once, for the memory budget (default 1)
# SWC_MARKET_NETWORK_SLOTS     market networks this host runs at once (default 4)
# MARKET_NETWORK_SLOT_DIR      default /var/tmp/socialweb-market-network-slots
# MARKET_NETWORK_SLOT          a slot the caller already holds (MARKET_NETWORK_SLOT_HELD=1)
# MARKET_NETWORK_BASE_MIB      memory budget of the network without children (default 3072)
# MARKET_NETWORK_CHILD_MIB     memory budget of one child application (default 768)
# MARKET_NETWORK_GUEST_MIB     memory budget of one guest (default 2560)
# MARKET_NETWORK_RECORD_OUT    copy the final market-network.json here
# KEEP_ACCEPT_ROOT  1 keeps MARKET_NETWORK_DIR after the run
# SOCIALWEB_KCP_PATH, SOCIALWEB_KINE_PATH, SOCIALWEB_DENO_PATH, SOCIALWEB_BAO_PATH
#                   the kcp, kine, pinned deno and bao the instance runs (default: PATH)
# FC_VMRUNTIME_COMMAND required: JSON argv of `socialweb-computer internal vm {verb}` with its
#                   options; every guest is a VM unit of the Linux VM runtime

REPO=$(cd "$(dirname "$0")/../.." && pwd)
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
MARKET_NETWORK_GATEWAY=${MARKET_NETWORK_GATEWAY:-0}
MARKET_NETWORK_GUESTS=${MARKET_NETWORK_GUESTS:-1}
SWC_MARKET_NETWORK_SLOTS=${SWC_MARKET_NETWORK_SLOTS:-4}
MARKET_NETWORK_SLOT_DIR=${MARKET_NETWORK_SLOT_DIR:-/var/tmp/socialweb-market-network-slots}
MARKET_NETWORK_BASE_MIB=${MARKET_NETWORK_BASE_MIB:-3072}
MARKET_NETWORK_CHILD_MIB=${MARKET_NETWORK_CHILD_MIB:-768}
MARKET_NETWORK_GUEST_MIB=${MARKET_NETWORK_GUEST_MIB:-2560}
MARKET_NETWORK_RECORD_OUT=${MARKET_NETWORK_RECORD_OUT:-}
KUBECTL=${KUBECTL:-kubectl}
WAIT_SECONDS=${WAIT_SECONDS:-240}
NETWORK_SECONDS=${NETWORK_SECONDS:-900}
SETTLE_SECONDS=${SETTLE_SECONDS:-10}
KEEP_ACCEPT_ROOT=${KEEP_ACCEPT_ROOT:-0}
FC_WORK_ROOT=${FC_WORK_ROOT:-/var/tmp/swc-$MARKET_NETWORK_ID}
if [ -z "${FC_VMRUNTIME_COMMAND:-}" ]; then
  echo "FC_VMRUNTIME_COMMAND is required: the JSON argv of socialweb-computer internal vm {verb} with its options" >&2
  exit 2
fi
VM_READY_SECONDS=${VM_READY_SECONDS:-900}
MARKET_SECONDS=${MARKET_SECONDS:-1500}
GUEST_GONE_SECONDS=${GUEST_GONE_SECONDS:-180}

BIDDERS=$(seq -f 'bidder-%g' 1 "$MARKET_NETWORK_BIDDERS" | tr '\n' ' ')
BIDDERS=${BIDDERS% }

DEV_STATE=$MARKET_NETWORK_DIR/dev
INSTANCE=mn-$MARKET_NETWORK_ID
KUBECONFIG_PATH=""
NET_STATE=""
RUNS_DIR=""
CORE_LOG=""
DEVSUP_PID=""
SERVER=""
KCACHE=$MARKET_NETWORK_DIR/.kubectl-cache
STATUS_JSON=$MARKET_NETWORK_DIR/network-status.json
MARKET_NETWORK_OWNED=0

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

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

# One value out of the MarketNetwork object dev up reported and the checks
# re-read: a dotted path, list elements selected by name (apps.bidder-1.did).
status_get() {
  python3 - "$STATUS_JSON" "$1" <<'PY'
import json, sys
try:
    node = json.load(open(sys.argv[1]))
    for part in sys.argv[2].split("."):
        if isinstance(node, list):
            node = next(x for x in node if x.get("name") == part)
        else:
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

need_mib=$(( MARKET_NETWORK_BASE_MIB + MARKET_NETWORK_BIDDERS * MARKET_NETWORK_CHILD_MIB + MARKET_NETWORK_GUESTS * MARKET_NETWORK_GUEST_MIB ))
avail_mib=$(mem_available_mib)
if [ "$avail_mib" -lt "$need_mib" ]; then
  echo "refused: MemAvailable is ${avail_mib} MiB and market network $MARKET_NETWORK_ID needs ${need_mib} MiB" >&2
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
    echo "refused: every one of the $SWC_MARKET_NETWORK_SLOTS market network slots under $MARKET_NETWORK_SLOT_DIR is held" >&2
    exit 3
  fi
fi

mkdir -p "$MARKET_NETWORK_DIR" "$KCACHE" "$FC_WORK_ROOT"
MARKET_NETWORK_OWNED=1
rm -f "$MARKET_NETWORK_JSON"
ln -sfn "$FC_WORK_ROOT" "$MARKET_NETWORK_DIR/guests"
network_set "networkId=\"$MARKET_NETWORK_ID\"" "slot=$SLOT" "pid=$$" "startedAt=\"$(now)\"" "networkDir=\"$MARKET_NETWORK_DIR\"" \
  "sourceRoot=\"$REPO\"" "fcWorkRoot=\"$FC_WORK_ROOT\"" "devInstance=\"$INSTANCE\"" \
  "readings.memAvailableMiBAtStart=$avail_mib" "readings.budgetMiB=$need_mib"

KC() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" "$@"; }

exe_path() {
  local p
  p=$(command -v "$1" 2>/dev/null || true)
  [ -n "$p" ] && readlink -f "$p"
}

# The admin kubeconfig of a child application's own kcp: the network holds it
# because the network created the child; the checks below read the child's
# root object and its components through it.
child_kubeconfig() {
  echo "$NET_STATE/apps/dev/$(status_get "status.apps.$1.instance")/kcp/admin.kubeconfig"
}
CKC() {
  local app=$1
  shift
  "$KUBECTL" --kubeconfig="$(child_kubeconfig "$app")" --cache-dir="$KCACHE" "$@"
}

DEV_UP_STARTED=0
dev_up() {
  local report=$MARKET_NETWORK_DIR/dev-up.json gateway=()
  DEV_UP_STARTED=1
  [ "$MARKET_NETWORK_GATEWAY" = "1" ] && gateway=(--with-gateway)
  local rc=0
  SOCIALWEB_KCP_PATH=${SOCIALWEB_KCP_PATH:-$(exe_path kcp)} \
  SOCIALWEB_KINE_PATH=${SOCIALWEB_KINE_PATH:-$(exe_path kine)} \
  SOCIALWEB_DENO_PATH=${SOCIALWEB_DENO_PATH:-$(exe_path deno)} \
  SOCIALWEB_BAO_PATH=${SOCIALWEB_BAO_PATH:-$(exe_path bao)} \
  SOCIALWEB_COMPUTER_SOURCE_ROOT="$REPO" \
    "$SOCIALWEB_COMPUTER" dev up --instance "$INSTANCE" --state-dir "$DEV_STATE" --profile market-network \
      --bidders "$MARKET_NETWORK_BIDDERS" "${gateway[@]}" --app-values "$APP_VALUES" \
      --host-backend proc --ready-timeout "${WAIT_SECONDS}s" --network-timeout "${NETWORK_SECONDS}s" \
      >"$report" 2>"$MARKET_NETWORK_DIR/dev-up.log" || rc=$?
  local state
  state=$DEV_STATE/dev/$INSTANCE
  KUBECONFIG_PATH=$state/kcp/admin.kubeconfig
  NET_STATE=$state
  RUNS_DIR=$state/denokcp/runs
  CORE_LOG=$state/logs/core.log
  DEVSUP_PID=$(python3 -c 'import json,sys
try:
    print(json.load(open(sys.argv[1]))["devsup"]["pid"])
except Exception:
    print("")' "$report")
  if [ -z "$DEVSUP_PID" ]; then
    DEVSUP_PID=$(python3 -c 'import json,sys
try:
    print(json.load(open(sys.argv[1]))["pid"])
except Exception:
    print("")' "$state/sup/devsup.json" 2>/dev/null || true)
  fi
  network_set "kubeconfig=\"$KUBECONFIG_PATH\"" "devStateDir=\"$state\"" "processes.devsup.pid=${DEVSUP_PID:-0}" \
    "timings.devUpExit=$rc"
  return "$rc"
}

dev_down() {
  [ "$DEV_UP_STARTED" = "1" ] || return 0
  local keep=()
  [ "$KEEP_ACCEPT_ROOT" = "1" ] && keep=(--keep-state)
  "$SOCIALWEB_COMPUTER" dev down --instance "$INSTANCE" --state-dir "$DEV_STATE" "${keep[@]}" >"$MARKET_NETWORK_DIR/dev-down.json" 2>"$MARKET_NETWORK_DIR/dev-down.log" || {
    echo "dev down of $INSTANCE failed:" >&2
    tail -n 20 "$MARKET_NETWORK_DIR/dev-down.log" >&2 || true
  }
  DEV_UP_STARTED=0
}

own_vm_entries() {
  local b
  for b in $BIDDERS; do
    vmruntime list "$b.bob.test" 2>/dev/null | jq -r '.[]? | "\(.unit) loaded=\(.loaded) workDir=\(.workDir // "-")"' 2>/dev/null
  done | tr '\n' ' '
}

own_vm_pids() {
  local b
  for b in $BIDDERS; do
    vmruntime list "$b.bob.test" 2>/dev/null | jq -r '.[]? | select(.loaded) | .status.MainPID // empty' 2>/dev/null
  done | tr '\n' ' '
}

own_guest_dirs() {
  [ -d "$FC_WORK_ROOT" ] || return 0
  find "$FC_WORK_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'fc-*' -printf '%f\n' | sort
}

vmruntime() {
  local verb=$1 handle=$2
  shift 2
  local argv
  mapfile -t argv < <(jq -r --arg v "$verb" '.[] | if . == "{verb}" then $v else . end' <<<"$FC_VMRUNTIME_COMMAND")
  "${argv[@]}" --handle "$handle" "$@"
}

stop_own_guests() {
  local b n
  for b in $BIDDERS; do
    for n in $(vmruntime list "$b.bob.test" 2>/dev/null | jq -r '.[]?.name' 2>/dev/null); do
      vmruntime stop "$b.bob.test" "$n" >/dev/null 2>&1 || true
    done
  done
}

descendants_of() {
  ps -eo pid=,ppid= | awk -v roots="$1" '
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

network_pids() {
  [ -n "$DEVSUP_PID" ] || return 0
  descendants_of "$DEVSUP_PID"
}

rss_kib_of() {
  local pids
  pids=$(echo $1 | tr ' ' ',')
  [ -n "$pids" ] || { echo 0; return; }
  ps -o rss= -p "$pids" 2>/dev/null | awk '{ s += $1 } END { print s + 0 }'
}

network_rss_kib() {
  rss_kib_of "$(network_pids | tr '\n' ' ')"
}

guest_mem_kib() {
  local roots
  roots=$(own_vm_pids)
  [ -n "${roots// }" ] || { echo 0; return; }
  rss_kib_of "$(descendants_of "$roots" | tr '\n' ' ')"
}

SAMPLER_PID=""
start_sampler() {
  local out=$MARKET_NETWORK_DIR/rss.samples
  : >"$out"
  (
    while :; do
      echo "$(date +%s) $(network_rss_kib) $(guest_mem_kib)" >>"$out"
      own_guest_dirs >>"$MARKET_NETWORK_DIR/guests.seen"
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
  local leftover_pids leftover_vms leftover_ports="" p
  leftover_pids=$(
    {
      pgrep -f -- "$MARKET_NETWORK_DIR" 2>/dev/null || true
      pgrep -f -- "$FC_WORK_ROOT" 2>/dev/null || true
      for p in $(network_get readings.pids); do
        kill -0 "$p" 2>/dev/null && echo "$p"
      done
    } | grep -vx "$$" | sort -u | tr '\n' ' '
  )
  leftover_vms=$(own_vm_entries)
  for p in $(python3 - "$MARKET_NETWORK_JSON" <<'PY'
import json, sys
try:
    r = json.load(open(sys.argv[1]))
except Exception:
    r = {}
for group in ("processes", "services", "apps"):
    for v in (r.get(group) or {}).values():
        if isinstance(v, dict) and v.get("port"):
            print(v["port"])
PY
  ); do
    listening "$p" && leftover_ports="$leftover_ports $p"
  done
  network_set "teardown.at=\"$(now)\"" "teardown.processes=\"${leftover_pids% }\"" \
    "teardown.vms=\"${leftover_vms% }\"" "teardown.ports=\"${leftover_ports# }\"" \
    "teardown.fcWorkRootLeft=$([ -e "$FC_WORK_ROOT" ] && echo true || echo false)"
  if [ -z "${leftover_pids// }" ] && [ -z "${leftover_vms// }" ] && [ -z "${leftover_ports// }" ] && [ ! -e "$FC_WORK_ROOT" ]; then
    echo "teardown: clean (no processes, VM units, ports or guest work root of market network $MARKET_NETWORK_ID left)"
  else
    echo "teardown: LEFT processes=[${leftover_pids}] vms=[${leftover_vms}] ports=[${leftover_ports}] workroot=$([ -e "$FC_WORK_ROOT" ] && echo "$FC_WORK_ROOT")" >&2
  fi
}

cleanup() {
  local status=$?
  trap - EXIT
  if [ "$MARKET_NETWORK_OWNED" != "1" ]; then
    exit "$status"
  fi
  stop_sampler
  network_set "timings.downStarted=\"$(now)\"" || true
  dev_down
  network_set "timings.downFinished=\"$(now)\"" || true
  stop_own_guests
  rm -rf "$FC_WORK_ROOT" 2>/dev/null || true
  for _ in $(seq 1 20); do pgrep -f -- "$MARKET_NETWORK_DIR" >/dev/null 2>&1 || break; sleep 0.5; done
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
  [ -n "$FG_PID" ] && kill -TERM $(descendants_of "$FG_PID") 2>/dev/null
  exit 130
}
trap on_signal INT TERM

run_fg() {
  "$@" &
  FG_PID=$!
  local rc=0
  wait "$FG_PID" || rc=$?
  FG_PID=""
  return "$rc"
}

echo "market network: $MARKET_NETWORK_ID  instance: $INSTANCE  slot: $SLOT  dir: $MARKET_NETWORK_DIR"
echo "guest work root: $FC_WORK_ROOT  bidders: $BIDDERS  gateway: $MARKET_NETWORK_GATEWAY"
echo "memory: ${avail_mib} MiB available, market network budget ${need_mib} MiB"


# Every bidder child gets the same provider inputs: the VM runtime command and
# the guest work root. They reach each bidder only as fields of its MarketBidder
# root object, which the network composes from these values.
APP_VALUES=$MARKET_NETWORK_DIR/app-values.yaml
python3 - "$APP_VALUES" "$FC_WORK_ROOT" "$FC_VMRUNTIME_COMMAND" <<'PY'
import json, sys
out, work, vmruntime = sys.argv[1:]
with open(out, "w") as f:
    json.dump({"bidder": {"firecracker": {"workRoot": work, "vmruntimeCommand": json.loads(vmruntime)}}}, f, indent=2)
PY

echo
echo "== socialweb-computer =="
SOCIALWEB_COMPUTER=${SOCIALWEB_COMPUTER:-$MARKET_NETWORK_DIR/bin/socialweb-computer}
if [ ! -x "$SOCIALWEB_COMPUTER" ]; then
  mkdir -p "$(dirname "$SOCIALWEB_COMPUTER")"
  (cd "$REPO" && go build -o "$SOCIALWEB_COMPUTER" ./cmd/socialweb-computer)
fi
echo "binary: $SOCIALWEB_COMPUTER"

echo
echo "== dev up --profile market-network =="
network_set "timings.devUpStarted=\"$(now)\""
up_status=0
dev_up || up_status=$?
network_set "timings.devUpFinished=\"$(now)\""
SERVER=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || true)
SERVER=${SERVER%%/clusters/*}
KC get marketnetwork network -o json >"$STATUS_JSON" 2>/dev/null || echo '{}' >"$STATUS_JSON"
echo "dev up exit=$up_status  kcp: $SERVER  phase: $(status_get status.phase)  $(status_get status.message)"
tail -n 5 "$MARKET_NETWORK_DIR/dev-up.log" 2>/dev/null | cut -c1-300 || true

echo
echo "== record =="

pod_field() {
  KC --server="$SERVER/clusters/root:$1" get denopod "$2" -o jsonpath="{.status.$3}" 2>/dev/null || true
}

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
for entry in "${PODS[@]}"; do
  set -- $entry
  record_pod "$1" "$2"
done

APPS=$BIDDERS
[ "$MARKET_NETWORK_GATEWAY" = "1" ] && APPS="$APPS gateway"
BIDDER_DIDS=""
for app in $APPS; do
  kcp_url=$(status_get "status.apps.$app.kcpUrl")
  network_set "apps.$app.instance=\"$(status_get "status.apps.$app.instance")\"" "apps.$app.kcpUrl=\"$kcp_url\"" \
    "apps.$app.port=${kcp_url##*:}" "apps.$app.kcpRoot=\"$(status_get "status.apps.$app.kcpRoot")\"" \
    "apps.$app.phase=\"$(status_get "status.apps.$app.phase")\"" "apps.$app.did=\"$(status_get "status.apps.$app.did")\""
done
for b in $BIDDERS; do
  did=$(status_get "status.apps.$b.did")
  BIDDER_DIDS="${BIDDER_DIDS:+$BIDDER_DIDS,}$did"
done
network_set "network=$(status_get status.network)"
HOST_IP=${HOST_IP:-$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')}
network_set "hostIp=\"$HOST_IP\""
PLC_URL=$(status_get status.network.plcDirectoryUrl)
INGRESS_HOST=$(status_get status.network.ingressProxyHost)
GUEST_TLS_URL=$(status_get status.network.guestTlsUrl)
CA_BUNDLE=$(status_get status.network.caBundleRef)
PLC_PORT=${PLC_URL##*:}
DISPATCHER_PORT=${INGRESS_HOST##*:}
GUEST_TLS_PORT=${GUEST_TLS_URL##*:}
BOB_PDS_PORT=$(network_get "services.bob/pds.port")
echo "plc=$PLC_URL ingress=$INGRESS_HOST guest-tls=$GUEST_TLS_URL ca=$CA_BUNDLE bidders=$BIDDER_DIDS"
network_set "readings.rssSteadyNetworkKiB=$(network_rss_kib)" "readings.pids=\"$(network_pids | sort -n | tr '\n' ' ')\""

echo
echo "== checks =="

http_code() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$@" 2>/dev/null) || true
  echo "${code:-000}"
}

RESULTS=()
FAILED=0
check() {
  RESULTS+=("$(printf '%-40s %-4s %s' "$1" "$2" "$3")")
  network_set "checks.${1//[ .]/_}=\"$2\"" || true
  [ "$2" = "PASS" ] || FAILED=1
}

if [ "$up_status" -eq 0 ] && [ "$(status_get status.phase)" = "Ready" ]; then
  check "dev up --profile market-network" PASS "exit=0 MarketNetwork Ready: $(status_get status.message)"
else
  check "dev up --profile market-network" FAIL "exit=$up_status phase=$(status_get status.phase): $(status_get status.message)"
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

# No two applications share a kcp: the network and every child each report
# their own kcp front and their own slot root.
own_kcp=$(python3 - "$STATUS_JSON" "$SERVER" "$NET_STATE" <<'PY'
import json, sys
s = json.load(open(sys.argv[1])).get("status") or {}
fronts = {sys.argv[2]: "network"}
roots = {sys.argv[3] + "/kcp/slot-a": "network"}
for a in s.get("apps") or []:
    u, r = a.get("kcpUrl"), a.get("kcpRoot")
    if not u or not r:
        print(f"{a.get('name')} reports no kcp"); sys.exit()
    if u in fronts or r in roots:
        print(f"{a.get('name')} shares a kcp with {fronts.get(u) or roots.get(r)}"); sys.exit()
    fronts[u], roots[r] = a["name"], a["name"]
print("")
PY
)
if [ -z "$own_kcp" ]; then
  check "every application its own kcp" PASS "network $SERVER and $(echo $APPS | wc -w) children, distinct fronts and slot roots"
else
  check "every application its own kcp" FAIL "$own_kcp"
fi

record_problem=$(python3 - "$MARKET_NETWORK_JSON" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
owner = {}
for group in ("processes", "services", "apps"):
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
  check "market network record" PASS "every service and child reported its own port"
else
  check "market network record" FAIL "$record_problem"
fi

code=$(http_code --cacert "$CA_BUNDLE" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
  "https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health")
if [ "$code" = "200" ]; then
  check "bob pds on the host" PASS "GET https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health -> $code"
else
  check "bob pds on the host" FAIL "GET https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/_health -> $code"
fi

bidder_pod_field() {
  CKC "$1" --server="$(status_get "status.apps.$1.kcpUrl")/clusters/root:app" get denopod bidder -o jsonpath="{$2}" 2>/dev/null || true
}

for b in $BIDDERS; do
  rphase=$(CKC "$b" get marketbidder app -o jsonpath='{.status.phase}' 2>/dev/null || true)
  rdid=$(CKC "$b" get marketbidder app -o jsonpath='{.status.did}' 2>/dev/null || true)
  if [ "$rphase" = "Ready" ] && [ -n "$rdid" ]; then
    check "$b root object" PASS "MarketBidder app in its own kcp: phase=Ready did=$rdid"
  else
    check "$b root object" FAIL "phase=${rphase:-missing} did=${rdid:-none}"
  fi
  root_plc=$(CKC "$b" get marketbidder app -o jsonpath='{.spec.network.plcDirectoryUrl}' 2>/dev/null || true)
  pod_plc=$(bidder_pod_field "$b" .spec.env.SERVICE_ARGS | python3 -c 'import json,sys
try:
    a = json.loads(sys.stdin.read())
    print(a[a.index("--plc-directory-url") + 1])
except Exception:
    print("")')
  if [ -n "$root_plc" ] && [ "$root_plc" = "$PLC_URL" ] && [ "$pod_plc" = "$root_plc" ]; then
    check "$b plc from its root object" PASS "network value $PLC_URL = root spec.network.plcDirectoryUrl = its DenoPod's --plc-directory-url"
  else
    check "$b plc from its root object" FAIL "network=$PLC_URL root=${root_plc:-none} denopod=${pod_plc:-none}"
  fi
  phase=$(bidder_pod_field "$b" .status.phase)
  ready=$(bidder_pod_field "$b" .status.ready)
  listeners=$(bidder_pod_field "$b" '.status.listeners[*].address')
  if [ "$phase" = "Running" ] && [ "$ready" = "true" ] && [ -n "$listeners" ]; then
    check "$b denopod" PASS "root:app/bidder in its own kcp Running ready listeners=$listeners"
  else
    check "$b denopod" FAIL "phase=${phase:-missing} ready=${ready:-missing} listeners=${listeners:-none}"
  fi
  addr=$(bidder_pod_field "$b" '.status.listeners[?(@.kind=="tcp")].address')
  code=$(http_code "http://${addr:-127.0.0.1:0}/oauth-client-metadata.json")
  if [ "$code" = "200" ]; then
    check "$b on the host" PASS "GET http://$addr/oauth-client-metadata.json -> $code"
  else
    check "$b on the host" FAIL "GET http://${addr:-none}/oauth-client-metadata.json -> $code"
  fi
done
if [ "$MARKET_NETWORK_GATEWAY" = "1" ]; then
  gphase=$(CKC gateway get gateway app -o jsonpath='{.status.phase}' 2>/dev/null || true)
  gplc=$(CKC gateway get gateway app -o jsonpath='{.spec.network.plcDirectoryUrl}' 2>/dev/null || true)
  if [ "$gphase" = "Ready" ] && [ "$gplc" = "$PLC_URL" ]; then
    check "gateway root object" PASS "Gateway app in its own kcp: phase=Ready network plc=$gplc"
  else
    check "gateway root object" FAIL "phase=${gphase:-missing} plc=${gplc:-none}"
  fi
fi

bob_pds_get() {
  curl -sS --max-time 10 --cacert "$CA_BUNDLE" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
    "https://pds.default.bob.svc.kcp.local:$BOB_PDS_PORT/xrpc/$1" 2>/dev/null || true
}
for b in $BIDDERS; do
  did=$(status_get "status.apps.$b.did")
  offerings=$(bob_pds_get "com.atproto.repo.listRecords?repo=$did&collection=com.publicdomainrelay.temp.market.offering" \
    | python3 -c 'import json,sys
try:
    print(len(json.load(sys.stdin).get("records") or []))
except Exception:
    print(0)')
  if [ -n "$did" ] && [ "${offerings:-0}" -gt 0 ]; then
    check "$b account on bob pds" PASS "did=$did offerings on pds.default.bob=$offerings"
  else
    check "$b account on bob pds" FAIL "did=${did:-none} offerings on pds.default.bob=${offerings:-0}"
  fi
done

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
for b in $BIDDERS; do
  ready=$(bidder_pod_field "$b" .status.ready)
  if [ "$ready" = "true" ]; then
    check "still up $b" PASS "ready=true after ${SETTLE_SECONDS}s"
  else
    check "still up $b" FAIL "ready=${ready:-missing} after ${SETTLE_SECONDS}s"
  fi
done

if [ -n "${ACCEPT_HOLD_DIR:-}" ]; then
  echo
  echo "== hold =="
  mkdir -p "$ACCEPT_HOLD_DIR"
  {
    echo "ACCEPT_ROOT=$MARKET_NETWORK_DIR"
    echo "MARKET_NETWORK_ID=$MARKET_NETWORK_ID"
    echo "MARKET_NETWORK_JSON=$MARKET_NETWORK_JSON"
    echo "CA_PEM=$CA_BUNDLE"
    echo "GUEST_TLS_DIR=$NET_STATE/network/guest-tls"
    echo "FC_WORK_ROOT=$FC_WORK_ROOT"
    echo "BIDDER_DID=${BIDDER_DIDS%%,*}"
    echo "BIDDER_DIDS=$BIDDER_DIDS"
    echo "PLC_PORT=$PLC_PORT"
    echo "DISPATCHER_PORT=$DISPATCHER_PORT"
    echo "GUEST_TLS_PORT=$GUEST_TLS_PORT"
    echo "ALICE_PDS_PORT=$(network_get "services.alice/pds.port")"
    echo "HOST_IP=$HOST_IP"
    echo "KUBECONFIG_PATH=$KUBECONFIG_PATH"
    echo "CORE_LOG=$CORE_LOG"
    echo "KCP_SERVER=$SERVER"
    echo "RUNS_DIR=$RUNS_DIR"
    echo "CHECKS_FAILED=$FAILED"
  } >"$ACCEPT_HOLD_DIR/facts.env.tmp"
  mv "$ACCEPT_HOLD_DIR/facts.env.tmp" "$ACCEPT_HOLD_DIR/facts.env"
  echo "holding the market network up until $ACCEPT_HOLD_DIR/release exists"
  while [ ! -e "$ACCEPT_HOLD_DIR/release" ]; do
    sleep 2
  done
  echo "released after the hold"
fi

# One compute contract through the market network: a host-side requester
# (market-requester/mod.ts, runComputeContract; a requester child application
# is 0008 M4-RUN-REQUEST) sends a signed RFP to the bidders the MarketNetwork
# status names, they bid, the requester accepts one, and the winner boots a
# guest from the cloud-init user_data the requester composed. The guest reports
# onNetwork and its ssh host keys through the network's relay; the requester
# reaches it over iroh with StrictHostKeyChecking=yes, checks that a known_hosts
# holding another key is refused, releases the contract with vm.delete, and the
# guest must then be gone.
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
if [ -n "${BIDDER_DIDS//,/}" ] && [ -n "$HOST_IP" ] && [ -n "$PLC_URL" ]; then
  market_started=$(date +%s)
  network_set "timings.requesterStarted=\"$(now)\""
  requester() {
  (cd "$REPO/ts/atproto-market" && DENO_CERT="$CA_BUNDLE" KCP_DNS_TABLE_FILE="$RUNS_DIR/.kcpdns/table.json" \
    exec timeout "$MARKET_SECONDS" \
    deno run -A --node-modules-dir=none --config "$REPO/deno.json" --lock="$REQUESTER_LOCK" \
      --preload "$RUNS_DIR/.kcpdns/shim.ts" \
      "$MARKET_DIR/market-requester/mod.ts" \
      --plc-directory-url "$PLC_URL" \
      --ingress-proxy-host "$INGRESS_HOST" \
      --bidder-dids "$BIDDER_DIDS" \
      --guest-host-aliases "$HOST_IP relay.localhost" \
      --guest-ca-file "$NET_STATE/network/guest-tls/ca.crt" \
      --guest-curl-home /root \
      --guest-journal-to-console \
      --guest-diagnose-url "$GUEST_TLS_URL/.well-known/did.json" \
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
receipt_code=$(http_code --cacert "$CA_BUNDLE" --resolve "pds.default.bob.svc.kcp.local:$BOB_PDS_PORT:127.0.0.1" \
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
gone_deadline=$(( $(date +%s) + GUEST_GONE_SECONDS ))
while :; do
  alive=""
  alive=$(own_vm_entries | tr ' ' '\n' | grep -c 'loaded=true' || true)
  [ "$alive" = "0" ] && alive=""
  dirs=""
  for g in $new_guests; do [ -d "$FC_WORK_ROOT/$g" ] && dirs="$dirs $g"; done
  [ -z "$alive" ] && [ -z "$dirs" ] && break
  [ "$(date +%s)" -ge "$gone_deadline" ] && break
  sleep 2
done
stop_sampler
network_set "contract.guests=\"$new_guests\""
if [ -z "$new_guests" ]; then
  check "market guest gone" FAIL "no guest of market network $MARKET_NETWORK_ID was seen "
elif [ -z "$alive" ] && [ -z "$dirs" ]; then
  check "market guest gone" PASS "$new_guests: no VM unit loaded and no work dir after release"
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
printf '  %-40s %-4s %s\n' "check" "result" "evidence"
for line in "${RESULTS[@]}"; do
  echo "  $line"
done

if [ "$FAILED" -ne 0 ]; then
  echo
  echo "core log, tail of $CORE_LOG:"
  tail -n 40 "$CORE_LOG" | cut -c1-400 || true
fi

echo
echo "market network record: $MARKET_NETWORK_JSON"
if [ "$FAILED" -eq 0 ]; then
  echo "accept: pass"
else
  echo "accept: fail"
fi
exit "$FAILED"
