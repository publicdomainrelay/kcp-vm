#!/usr/bin/env bash
set -euo pipefail

# Live acceptance for the AT Protocol market example.
#
# This brings the whole market up in a throwaway state directory -- its own kcp,
# its own kine, its own OpenBao, the provider, and every DenoPod apply.sh
# applies -- and then reaches the workloads, rather than only decoding the
# manifests. It exits 0 only when every check passed.
#
# Everything it starts it starts itself, under $ACCEPT_ROOT, on ports the kernel
# hands out, so it cannot collide with another kcp, kine or OpenBao already
# running on the machine, and it never stops one it did not start:
# deploy/stop-kcp.sh signals only processes whose cmdline names its own root, and
# OpenBao is killed only through the pid file inside $ACCEPT_ROOT.
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
ACCEPT_ROOT=${ACCEPT_ROOT:-$(mktemp -d)}
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
# This repository vendors deno-kcp's manifests, not its provider: the provider
# binary is a test dependency built from the deno-kcp checkout at the commit
# deploy/denokcp/vendor.json records, unless DENO_KCP_PROVIDER names one.
DENO_KCP_PROVIDER=${DENO_KCP_PROVIDER:-}
# Set to 1 to keep $ACCEPT_ROOT (logs, run directories) after the run.
KEEP_ACCEPT_ROOT=${KEEP_ACCEPT_ROOT:-0}
# socialweb-computer-kcp: the bidder rents firecracker guests. FC_NODEIMAGE,
# FC_CONFIG, FC_REPO_DIR, FC_PREINSTALL and FC_RUNNER_IMAGE must be set (see
# apply.sh); each guest's work directory lands under FC_WORK_ROOT, which is this
# run's own so every guest found there is one this run created.
FC_WORK_ROOT=${FC_WORK_ROOT:-$ACCEPT_ROOT/guests}
# How long the requester waits for the guest to report onNetwork, how long the
# whole contract may take, and how long a released guest's container may
# take to go away.
VM_READY_SECONDS=${VM_READY_SECONDS:-900}
MARKET_SECONDS=${MARKET_SECONDS:-1500}
GUEST_GONE_SECONDS=${GUEST_GONE_SECONDS:-180}
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$ACCEPT_ROOT/guest-tls}
# The provider stops every deno process after --pod-timeout (5m by default),
# long-running DenoPods included, and restartPolicy Always then starts a fresh
# bidder that has forgotten every contract. A market contract outlives 5m, so
# the test bed's services are given a day.
POD_TIMEOUT=${POD_TIMEOUT:-24h}

# The four workspaces apply.sh creates. Defined here, not next to the apply
# below, because the EXIT trap reads them and it can run before then.
WORKSPACES="global relay alice bob"

KUBECONFIG_PATH=$ACCEPT_ROOT/kcp/admin.kubeconfig
KCACHE=$ACCEPT_ROOT/.kubectl-cache
OPENBAO_DIR=$ACCEPT_ROOT/openbao
PROVIDER_LOG=$ACCEPT_ROOT/provider.log
PROVIDER_PID=""
ATTEMPT_STATUS=()

mkdir -p "$ACCEPT_ROOT" "$KCACHE"

# Three free 127.0.0.1 ports, from the kernel, so two accept.sh runs on one
# machine do not fight over a pinned one.
free_port() {
  python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()'
}

KCP_PORT=$(free_port)
KINE_PORT=$(free_port)
while [ "$KINE_PORT" = "$KCP_PORT" ]; do KINE_PORT=$(free_port); done
OPENBAO_PORT=$(free_port)
while [ "$OPENBAO_PORT" = "$KCP_PORT" ] || [ "$OPENBAO_PORT" = "$KINE_PORT" ]; do
  OPENBAO_PORT=$(free_port)
done

# The provider appends to $PROVIDER_LOG, so a restart keeps the log of the
# attempt that stalled alongside the one that came up.
start_provider() {
  RUNS_DIR="$ACCEPT_ROOT/runs" "$ACCEPT_ROOT/deno-kcp-provider" \
    --kubeconfig="$KUBECONFIG_PATH" \
    --openbao-addr="http://127.0.0.1:$OPENBAO_PORT" \
    --openbao-token=root \
    --pod-timeout="$POD_TIMEOUT" \
    >>"$PROVIDER_LOG" 2>&1 &
  PROVIDER_PID=$!
}

stop_provider() {
  if [ -n "$PROVIDER_PID" ] && kill -0 "$PROVIDER_PID" 2>/dev/null; then
    kill "$PROVIDER_PID" 2>/dev/null || true
    wait "$PROVIDER_PID" 2>/dev/null || true
  fi
  PROVIDER_PID=""
}

# The DenoPod processes are children of the provider. Killing the provider
# first orphans them and they keep holding the fixed host ports 2583 to 2587 of
# the example, so the next run on this machine cannot bind them, and a run that
# leaks its workloads is not repeatable. Delete every DenoPod in every workspace
# through kcp first -- the denopod.deno.computer/run finalizer stops each
# process, and it only runs while the provider is up -- wait, bounded, for the
# objects to disappear, and only then stop the provider. This does nothing when
# the cluster was never reached, so it never touches a kcp it did not start.
delete_workloads() {
  [ -n "${SERVER:-}" ] || return 0
  local ws deadline
  for ws in $WORKSPACES; do
    KC --server="$SERVER/clusters/root:$ws" delete denopods --all --ignore-not-found >/dev/null 2>&1 || true
  done
  deadline=$(( $(date +%s) + DELETE_SECONDS ))
  while :; do
    if ! denopod_exists_any; then
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "workloads did not disappear within ${DELETE_SECONDS}s; stopping the provider anyway" >&2
      return 0
    fi
    sleep 2
  done
}

# Guests are named by their work directory under FC_WORK_ROOT, which belongs to
# this run alone, so this stops only containers this run's bidder started.
own_guests() {
  [ -d "$FC_WORK_ROOT" ] || return 0
  find "$FC_WORK_ROOT" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort
}

stop_own_guests() {
  local name
  for name in $(own_guests); do
    if docker inspect "$name" >/dev/null 2>&1; then
      echo "stopping guest container $name, left behind by this run" >&2
      docker rm -f "$name" >/dev/null 2>&1 || true
    fi
  done
}

cleanup() {
  local status=$?
  trap - EXIT
  # Workloads before the provider that owns them, in that order, so no process
  # is orphaned onto the example's fixed ports.
  delete_workloads
  stop_provider
  stop_own_guests
  if [ -s "$OPENBAO_DIR/bao.pid" ]; then
    local bao_pid
    bao_pid=$(cat "$OPENBAO_DIR/bao.pid")
    if [ -n "$bao_pid" ] && kill -0 "$bao_pid" 2>/dev/null; then
      kill "$bao_pid" 2>/dev/null || true
    fi
  fi
  ROOT="$ACCEPT_ROOT/kcp" bash "$DENOKCP_DIR/stop-kcp.sh" >/dev/null || true
  if [ "$KEEP_ACCEPT_ROOT" = "1" ]; then
    echo "kept $ACCEPT_ROOT"
  else
    rm -rf "$ACCEPT_ROOT"
  fi
  exit "$status"
}
trap cleanup EXIT

echo "accept root: $ACCEPT_ROOT"
echo "ports: kcp=$KCP_PORT kine=$KINE_PORT openbao=$OPENBAO_PORT"

echo
echo "== kcp =="
ROOT="$ACCEPT_ROOT/kcp" \
KCP_SECURE_PORT="$KCP_PORT" \
KINE_ENDPOINT="http://127.0.0.1:$KINE_PORT" \
  bash "$DENOKCP_DIR/start-kcp.sh"

echo
echo "== provider workspace and schemas =="
KUBECONFIG_PATH="$KUBECONFIG_PATH" bash "$DENOKCP_DIR/install-provider.sh"

echo
echo "== provider build =="
if [ -n "$DENO_KCP_PROVIDER" ]; then
  cp "$DENO_KCP_PROVIDER" "$ACCEPT_ROOT/deno-kcp-provider"
  echo "provider: $DENO_KCP_PROVIDER"
else
  ORG_ROOT="$ORG_ROOT" WORK_DIR="$ACCEPT_ROOT/deno-kcp-src" \
    bash "$DENOKCP_DIR/build-provider.sh" "$ACCEPT_ROOT/deno-kcp-provider"
fi

echo
echo "== openbao =="
OPENBAO_DIR="$OPENBAO_DIR" \
OPENBAO_LISTEN="127.0.0.1:$OPENBAO_PORT" \
  bash "$DENOKCP_DIR/start-openbao.sh"

# The provider reconciles nothing when it starts before its APIExports publish
# their virtual workspace endpoints, and the retry below only notices after a
# full WAIT_SECONDS. kcp publishes an export's endpoint only once a workspace
# binds it, so the market's workspaces are created here (apply.sh applies the
# same file again, unchanged) and the endpoints are waited for, bounded,
# before the provider starts.
echo
echo "== export endpoints =="
ACCEPT_SERVER=$("$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
ACCEPT_SERVER=${ACCEPT_SERVER%%/clusters/*}
"$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" --server="$ACCEPT_SERVER/clusters/root" \
  apply --validate=false -f "$MARKET_DIR/00-workspaces.yaml"
urls=""
for _ in $(seq 1 120); do
  urls=$("$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" \
    --server="$ACCEPT_SERVER/clusters/root:deno-provider" \
    get apiexportendpointslices -o jsonpath='{.items[*].status.endpoints[*].url}' 2>/dev/null || true)
  [ "$(echo "$urls" | wc -w)" -ge 2 ] && break
  sleep 1
done
echo "endpoints: ${urls:-none}"

echo
echo "== provider =="
start_provider

# kubectl and the workspace list are needed while apply.sh is still running: the
# retry below reads the cluster to decide whether the provider reconciled
# anything at all, so they are set up before it is started.
KC() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --cache-dir="$KCACHE" "$@"; }

SERVER=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SERVER=${SERVER%%/clusters/*}

# The provider proves nothing is reconciled: no OpenBao object in any workspace
# carries status.ready true, and no DenoPod exists in any workspace. Either one
# appearing means it is working, only slowly, and must not be restarted.
openbao_ready_any() {
  local ws
  for ws in $WORKSPACES; do
    if [ "$(KC --server="$SERVER/clusters/root:$ws" get openbao openbao -o jsonpath='{.status.ready}' 2>/dev/null || true)" = "true" ]; then
      return 0
    fi
  done
  return 1
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
  OPENBAO_LISTEN="127.0.0.1:$OPENBAO_PORT" \
  START_OPENBAO=0 \
  WAIT_SECONDS="$WAIT_SECONDS" \
  KEY_FILE="$ACCEPT_ROOT/alice-pds-key.hex" \
  BOB_KEY_FILE="$ACCEPT_ROOT/bob-pds-key.hex" \
  BIDDER_KEY_FILE="$ACCEPT_ROOT/bidder-key.hex" \
  FC_WORK_ROOT="$FC_WORK_ROOT" \
  GUEST_TLS_DIR="$GUEST_TLS_DIR" \
    bash "$MARKET_DIR/apply.sh" || apply_status=$?
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

# apply.sh failing is not a reason to skip the checks: what came up and what did
# not is the evidence, and it is read the same way either way.
echo
echo "== checks =="

pod_phase() {
  KC --server="$SERVER/clusters/root:$1" get denopod "$2" -o jsonpath='{.status.phase}' 2>/dev/null || true
}
pod_ready() {
  KC --server="$SERVER/clusters/root:$1" get denopod "$2" -o jsonpath='{.status.ready}' 2>/dev/null || true
}

# The market CA -- the root every workspace's intermediate chains to -- read from
# the PKI mount in OpenBao's root namespace. A workload serves its intermediate
# alongside its leaf, so this one file is enough to verify any of the https
# listeners below.
CA_PEM=$ACCEPT_ROOT/market-ca.pem
if ! curl -sS --fail --header "X-Vault-Token: root" \
    "http://127.0.0.1:$OPENBAO_PORT/v1/pki/ca/pem" -o "$CA_PEM" 2>/dev/null || [ ! -s "$CA_PEM" ]; then
  echo "could not read the market CA from http://127.0.0.1:$OPENBAO_PORT/v1/pki/ca/pem; the https host checks below cannot verify the leaf" >&2
fi

# The status curl printed, and 000 exactly once when the fetch failed. curl
# already prints 000 and exits non-zero on a failed transfer, so the fallback
# only fills in an empty answer; it must not append a second 000 of its own.
# Verification stays on: the leaf is signed by the workspace's OpenBao
# authority for the service's cluster-local name, which does not resolve on the
# host, so the caller passes --cacert with the market CA and --resolve to the
# loopback address. A fetch that trusted any certificate would prove only that
# something is listening.
http_code() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$@" 2>/dev/null) || true
  echo "${code:-000}"
}

RESULTS=()
FAILED=0
check() {
  RESULTS+=("$(printf '%-36s %-4s %s' "$1" "$2" "$3")")
  [ "$2" = "PASS" ] || FAILED=1
}

PODS=("global plc" "relay relay" "relay dispatcher" "relay dispatcher-tls" "alice pds" "bob pds" "bob bidder")

if [ "$apply_status" -eq 0 ]; then
  check "apply.sh" PASS "exit=$apply_status attempts=${ATTEMPT_STATUS[*]}"
else
  check "apply.sh" FAIL "exit=$apply_status attempts=${ATTEMPT_STATUS[*]}"
fi

for entry in "${PODS[@]}"; do
  set -- $entry
  ws=$1; name=$2
  phase=$(pod_phase "$ws" "$name")
  ready=$(pod_ready "$ws" "$name")
  if [ "$phase" = "Running" ] && [ "$ready" = "true" ]; then
    check "denopod root:$ws/$name" PASS "phase=Running ready=true"
  else
    check "denopod root:$ws/$name" FAIL "phase=${phase:-missing} ready=${ready:-missing}"
  fi
done

vphase=$(pod_phase alice verifier)
if [ "$vphase" = "Succeeded" ]; then
  check "denopod root:alice/verifier" PASS "phase=Succeeded"
else
  check "denopod root:alice/verifier" FAIL "phase=${vphase:-missing}"
fi

# The provider proves the service answers on its own cluster-local name: the
# readiness probe resolves the name through the shim and requests the path, and
# ready=true is that probe's verdict. The host proves the same listener answers
# from outside, where the name does not resolve. Each host check names the scheme
# the listener actually speaks, and the two are not the same: the bob PDS writes
# the injected leaf and appends --tls-cert-file and --tls-key-file, so its
# listener is the TLS one and an http fetch of it is not a check of anything;
# the bidder declares no such options -- its parser rejects unknown flags -- so
# it consumes only the trust bundle, listens in plain HTTP, and an https fetch of
# its plain listener fails the same way round. The provider's readiness probe is
# the one that does not care, because it tries https and then http.
ready=$(pod_ready bob pds)
if [ "$ready" = "true" ]; then
  check "bob pds on its name" PASS "ready=true probe=kcpdns pds.default.bob.svc.kcp.local /xrpc/_health"
else
  check "bob pds on its name" FAIL "ready=${ready:-missing} probe=kcpdns pds.default.bob.svc.kcp.local /xrpc/_health"
fi

code=$(http_code --cacert "$CA_PEM" --resolve pds.default.bob.svc.kcp.local:2585:127.0.0.1 \
  https://pds.default.bob.svc.kcp.local:2585/xrpc/_health)
if [ "$code" = "200" ]; then
  check "bob pds on the host" PASS "GET https://pds.default.bob.svc.kcp.local:2585/xrpc/_health -> $code"
else
  check "bob pds on the host" FAIL "GET https://pds.default.bob.svc.kcp.local:2585/xrpc/_health -> $code"
fi

ready=$(pod_ready bob bidder)
if [ "$ready" = "true" ]; then
  check "bidder on its name" PASS "ready=true probe=kcpdns bidder.default.bob.svc.kcp.local /oauth-client-metadata.json"
else
  check "bidder on its name" FAIL "ready=${ready:-missing} probe=kcpdns bidder.default.bob.svc.kcp.local /oauth-client-metadata.json"
fi

code=$(http_code http://127.0.0.1:2586/oauth-client-metadata.json)
if [ "$code" = "200" ]; then
  check "bidder on the host" PASS "GET http://127.0.0.1:2586/oauth-client-metadata.json -> $code"
else
  check "bidder on the host" FAIL "GET http://127.0.0.1:2586/oauth-client-metadata.json -> $code"
fi

# A pod that dies after startup passes a single read. Reading the long-running
# pods again after a pause is what catches it.
echo "waiting ${SETTLE_SECONDS}s, then reading the long-running pods again"
sleep "$SETTLE_SECONDS"
for entry in "${PODS[@]}"; do
  set -- $entry
  ws=$1; name=$2
  phase=$(pod_phase "$ws" "$name")
  ready=$(pod_ready "$ws" "$name")
  if [ "$phase" = "Running" ] && [ "$ready" = "true" ]; then
    check "still up root:$ws/$name" PASS "phase=Running ready=true after ${SETTLE_SECONDS}s"
  else
    check "still up root:$ws/$name" FAIL "phase=${phase:-missing} ready=${ready:-missing} after ${SETTLE_SECONDS}s"
  fi
done

# socialweb-computer-kcp: one compute contract through the test bed. A
# requester on the host (market-requester/mod.ts, runComputeContract) sends a
# signed RFP to the bidder through the dispatcher; the bidder bids, the
# requester accepts, and the bidder boots a firecracker guest from the
# cloud-init user_data the requester composed. The guest reports onNetwork,
# the requester releases the contract with vm.delete without ever opening a
# session to it, and the guest's container must then be gone.
echo
echo "== market contract =="
BIDDER_DID=""
for f in $(grep -rl 'localPDS plc' "$ACCEPT_ROOT/runs" --include=stdout.txt 2>/dev/null || true); do
  BIDDER_DID=$(grep -h 'localPDS plc' "$f" | grep -o 'did:plc:[a-z2-7]\{24\}' | head -1)
  [ -n "$BIDDER_DID" ] && break
done
HOST_IP=${HOST_IP:-$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')}
MARKET_RESULT=$ACCEPT_ROOT/market-requester-result.json
MARKET_LOG=$ACCEPT_ROOT/market-requester.log
guests_before=$(own_guests)
echo "bidder: ${BIDDER_DID:-unknown}  guests reach the dispatcher at: ${HOST_IP:-unknown}"
market_status=0
if [ -n "$BIDDER_DID" ] && [ -n "$HOST_IP" ]; then
  market_started=$(date +%s)
  (cd "$ORG_ROOT/atproto-market" && DENO_CERT="$CA_PEM" timeout "$MARKET_SECONDS" \
    deno run -A --cached-only --config "$ORG_ROOT/atproto-market/deno.json" \
      "$MARKET_DIR/market-requester/mod.ts" \
      --plc-directory-url https://127.0.0.1:2587 \
      --ingress-proxy-host relay.localhost:2588 \
      --bidder-dids "$BIDDER_DID" \
      --guest-host-aliases "$HOST_IP relay.localhost" \
      --guest-ca-file "$GUEST_TLS_DIR/ca.crt" \
      --guest-journal-to-console \
      --vm-ready-timeout-sec "$VM_READY_SECONDS" \
      --result-file "$MARKET_RESULT") >"$MARKET_LOG" 2>&1 || market_status=$?
  echo "requester exit=$market_status after $(( $(date +%s) - market_started ))s, log $MARKET_LOG"
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
  check "market guest onNetwork" FAIL "bidder=${BIDDER_DID:-unknown} exit=$market_status reason=$(field reason)"
fi
if [ "$(field vmDeleteOk)" = "True" ] && [ "$market_status" -eq 0 ]; then
  check "market contract released" PASS "vm.delete status=$(field vmDeleteStatus) total=$(field totalMs)ms"
else
  check "market contract released" FAIL "vm.delete status=$(field vmDeleteStatus) reason=$(field reason)"
fi
new_guests=""
for g in $(own_guests); do
  case " $(echo $guests_before) " in
    *" $g "*) ;;
    *) new_guests="$new_guests $g" ;;
  esac
done
new_guests=${new_guests# }
if [ -z "$new_guests" ]; then
  check "market guest gone" FAIL "no guest work directory appeared under $FC_WORK_ROOT"
else
  gone_deadline=$(( $(date +%s) + GUEST_GONE_SECONDS ))
  while :; do
    alive=""
    for g in $new_guests; do
      if [ "$(docker inspect -f '{{.State.Running}}' "$g" 2>/dev/null || true)" = "true" ]; then
        alive="$alive $g"
      fi
    done
    [ -z "$alive" ] && break
    [ "$(date +%s)" -ge "$gone_deadline" ] && break
    sleep 2
  done
  if [ -z "$alive" ]; then
    check "market guest gone" PASS "$(echo $new_guests) not running $(( GUEST_GONE_SECONDS - (gone_deadline - $(date +%s)) ))s after release"
  else
    check "market guest gone" FAIL "still running after ${GUEST_GONE_SECONDS}s:$alive"
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
    phase=$(pod_phase "$ws" "$name")
    ready=$(pod_ready "$ws" "$name")
    if [ "$phase" = "Running" ] && [ "$ready" = "true" ] || [ "$phase" = "Succeeded" ]; then
      continue
    fi
    echo "root:$ws $name:"
    KC --server="$SERVER/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status}' 2>&1 | sed 's/^/  /' || true
    echo
  done
fi

echo
if [ "$FAILED" -eq 0 ]; then
  echo "accept: pass"
else
  echo "accept: fail"
fi
exit "$FAILED"
