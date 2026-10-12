#!/usr/bin/env bash
set -euo pipefail

# Deploys the AT Protocol market topology: a PLC directory in root:global, a relay
# in root:relay, a PDS in root:alice, and in root:bob a second PDS plus
# BED_BIDDERS market bidders (bidder-1 .. bidder-N). Each DenoPod runs its
# service as a supervised child process of its own deno entrypoint, because
# these are CLI entrypoints that own their argv, not libraries.
#
# Every service binds port 0. The port it got reaches its DenoPod's
# status.address (the service writes it to PORT_FILE in its run directory and
# the provider reads it back), peers reach it by its cluster-local name through
# the kcpdns shim, and this script reads status.address wherever a port has to
# be rendered into another pod: the dispatcher's and its TLS front's ports go
# into every bidder's argv, because the bidder hands them to its guests.
#
# Requires the provider to be installed first (deploy/denokcp/install-provider.sh)
# and the sibling repositories checked out: atproto-market, atproto-relay,
# hono-pds, did-key-ingress-proxy, typescript-helpers. See README.md in this
# directory for what does and does not work between the services.
#
# BED_ID       names this bed in its guests' container labels (default demo)
# BED_DIR      this bed's state directory (default <repo>/.kcp-demo); keys, the
#              guest CA and every pod's state directory live under it
# BED_BIDDERS  how many bidders to run (default 1)
# POD_DENO_DIR the module cache the services share (default $DENO_DIR or
#              ~/.cache/deno); each service's Deno KV has an explicit path under
#              BED_DIR/state, so sharing the cache never shares a store
# ADDRESSES    file the read-back addresses are written to (default
#              BED_DIR/addresses.env)

REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
DENOKCP_DIR=$REPO/deploy/denokcp
MARKET_DIR=$(cd "$(dirname "$0")" && pwd)
ORG_ROOT=${ORG_ROOT:-$(cd "$REPO/.." && pwd)}
BED_ID=${BED_ID:-demo}
BED_DIR=${BED_DIR:-$REPO/.kcp-demo}
BED_BIDDERS=${BED_BIDDERS:-1}
STATE_DIR=${STATE_DIR:-$BED_DIR/state}
POD_DENO_DIR=${POD_DENO_DIR:-${DENO_DIR:-$HOME/.cache/deno}}
ADDRESSES=${ADDRESSES:-$BED_DIR/addresses.env}
KUBECTL=${KUBECTL:-kubectl}
# kubectl caches discovery per server and the cache outlives a schema change: a
# cached NAMESPACED=false makes it POST a namespaced resource without a
# namespace, which kcp answers with a NotFound that names the resource and
# nothing else. A throwaway cache costs a round trip and removes the trap.
KCACHE=${KCACHE:-$(mktemp -d)}
trap 'rm -rf "$KCACHE"' EXIT
KC() { "$KUBECTL" --cache-dir="$KCACHE" "$@"; }
export KUBECONFIG=${KUBECONFIG:-$BED_DIR/admin.kubeconfig}
WAIT_SECONDS=${WAIT_SECONDS:-120}
KEY_DIR=${KEY_DIR:-$BED_DIR/keys}
KEY_FILE=${KEY_FILE:-$KEY_DIR/alice-pds.hex}
BOB_KEY_FILE=${BOB_KEY_FILE:-$KEY_DIR/bob-pds.hex}

WORKSPACES="global relay alice bob"

case "$BED_BIDDERS" in
  ''|*[!0-9]*|0) echo "BED_BIDDERS must be a positive number, got '$BED_BIDDERS'" >&2; exit 1 ;;
esac
BIDDERS=$(seq -f 'bidder-%g' 1 "$BED_BIDDERS" | tr '\n' ' ')

S=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
S=${S%%/clusters/*}
ROOT_SERVER="$S/clusters/root"

echo "org root:    $ORG_ROOT"
echo "root server: $ROOT_SERVER"
echo "bed:         $BED_ID ($BED_DIR) bidders: $BIDDERS"

for bin in atproto-market/hono-plc atproto-market/hono-bidder atproto-relay/hono-atproto-relay hono-pds did-key-ingress-proxy/hono-did-key-ingress-proxy; do
  [ -e "$ORG_ROOT/$bin" ] || { echo "missing $ORG_ROOT/$bin" >&2; exit 1; }
done

# socialweb-computer-kcp: the bidder runs --compute-provider-firecracker. Every
# input below is a path on this host or in the runner image, and none has a
# default that would be right on another machine, so a missing one stops the
# apply here rather than at the first RFP. FC_NODEBOOT and FC_VMM name paths
# inside the runner image, which is what boots each guest.
FC_NODEBOOT=${FC_NODEBOOT:-/usr/local/bin/socialweb-nodeboot}
FC_VMM=${FC_VMM:-/usr/local/bin/firecracker}
missing=""
for v in FC_NODEIMAGE FC_CONFIG FC_REPO_DIR FC_PREINSTALL FC_RUNNER_IMAGE FC_WORK_ROOT; do
  [ -n "${!v:-}" ] || missing="$missing $v"
done
if [ -n "$missing" ]; then
  echo "the firecracker bidder needs:$missing (see deploy/examples/atproto/market/accept.sh)" >&2
  exit 1
fi
mkdir -p "$FC_WORK_ROOT"
# Each guest's firecracker API socket lives in its work directory, and a unix
# socket path cannot exceed 107 bytes; past that the guest dies at boot and its
# work directory is removed, which reads as a bidder that never provisioned.
fc_sock="$FC_WORK_ROOT/fc-000000000000-00000000/fc.sock"
if [ "${#fc_sock}" -gt 107 ]; then
  echo "FC_WORK_ROOT is too long: a guest's API socket would be ${#fc_sock} bytes ($fc_sock), over the 107-byte unix socket limit" >&2
  exit 1
fi

# socialweb-computer-kcp: a guest reaches the dispatcher only over https (its
# OIDC proof and its onNetwork report), at <name>.relay.localhost on the
# bidder's --guest-tls-port. 36-relay-dispatcher-tls.yaml terminates that TLS
# with a leaf for relay.localhost and *.relay.localhost signed by this CA, and
# the requester has the guest trust the CA through its own user_data.
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$BED_DIR/guest-tls}
if [ ! -s "$GUEST_TLS_DIR/relay.localhost.crt" ]; then
  mkdir -p "$GUEST_TLS_DIR"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
    -subj "/CN=market test bed guest CA" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout "$GUEST_TLS_DIR/ca.key" -out "$GUEST_TLS_DIR/ca.crt" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -subj "/CN=relay.localhost" \
    -keyout "$GUEST_TLS_DIR/relay.localhost.key" -out "$GUEST_TLS_DIR/relay.localhost.csr" 2>/dev/null
  printf 'subjectAltName=DNS:relay.localhost,DNS:*.relay.localhost\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' \
    >"$GUEST_TLS_DIR/relay.localhost.ext"
  openssl x509 -req -in "$GUEST_TLS_DIR/relay.localhost.csr" -CA "$GUEST_TLS_DIR/ca.crt" \
    -CAkey "$GUEST_TLS_DIR/ca.key" -CAcreateserial -days 30 \
    -extfile "$GUEST_TLS_DIR/relay.localhost.ext" -out "$GUEST_TLS_DIR/relay.localhost.crt" 2>/dev/null
  chmod 600 "$GUEST_TLS_DIR/ca.key" "$GUEST_TLS_DIR/relay.localhost.key"
  echo "generated the guest TLS CA and the relay.localhost leaf in $GUEST_TLS_DIR" >&2
fi

KC --server="$ROOT_SERVER" apply --validate=false -f "$MARKET_DIR/00-workspaces.yaml"

for ws in $WORKSPACES; do
  tries=0
  until [ "$(KC --server="$ROOT_SERVER" get workspace "$ws" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Ready" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$WAIT_SECONDS" ]; then
      echo "workspace root:$ws did not reach Ready within ${WAIT_SECONDS}s" >&2
      exit 1
    fi
    sleep 1
  done
  echo "workspace root:$ws Ready"
done

for ws in $WORKSPACES; do
  tenant="$S/clusters/root:$ws"
  tries=0
  # Waiting for the resource to be listable is not enough: a binding that is not
  # Ready yet will list an empty list but reject a create, which is how this
  # first failed. Wait for every binding to report Ready, without assuming how
  # many there are.
  until status=$(KC --server="$tenant" get apibindings -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null) &&
    [ -n "$status" ] && [ "${status#*False}" = "$status" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$WAIT_SECONDS" ]; then
      echo "the API bindings in root:$ws never became Ready; is the provider running?" >&2
      KC --server="$tenant" get apibindings >&2 || true
      exit 1
    fi
    sleep 1
  done
  KC --server="$tenant" apply --validate=false -f "$MARKET_DIR/10-rbac.yaml" >/dev/null
done

# The certificates come from OpenBao: the root CA lives in its root namespace and
# each workspace's namespace gets an intermediate signed by it. The provider has
# to have been started against the same vault -- see deploy/denokcp/start-openbao.sh,
# which binds port 0 and writes the address it got to OPENBAO_DIR/bao.addr.
OPENBAO_DIR=${OPENBAO_DIR:-$BED_DIR/openbao}
if [ "${START_OPENBAO:-1}" = "1" ]; then
  OPENBAO_DIR="$OPENBAO_DIR" bash "$DENOKCP_DIR/start-openbao.sh"
fi
OPENBAO_ADDR=${OPENBAO_ADDR:-$(cat "$OPENBAO_DIR/bao.addr" 2>/dev/null || true)}

# One signing key per identity: alice's PDS, bob's PDS and each bidder. Each is
# overridable, each persists in its own file, and a key that showed up twice
# would make two identities collide, so a distinct one is generated for each
# that is not already spoken for.
resolve_key() {
  local env=$1 file=$2 label=$3
  if [ -n "$env" ] && [ -n "${!env:-}" ]; then
    printf '%s' "${!env}"
    return
  fi
  if [ -s "$file" ]; then
    cat "$file"
    return
  fi
  mkdir -p "$(dirname "$file")"
  local generated
  generated=$(openssl rand -hex 32)
  printf '%s' "$generated" > "$file"
  chmod 600 "$file"
  echo "generated a $label signing key at $file" >&2
  printf '%s' "$generated"
}

key=$(resolve_key PDS_PRIVATE_KEY_HEX "$KEY_FILE" "PDS")
bob_key=$(resolve_key BOB_PDS_PRIVATE_KEY_HEX "$BOB_KEY_FILE" "bob PDS")

DISPATCHER_PORT=""
GUEST_TLS_PORT=""
BIDDER_NAME=""
bidder_key=""

# Each pod's manifest carries its own placeholder, so one substitution pass can
# never hand two pods the same key.
apply_pod() {
  local ws=$1 file=$2
  sed -e "s|/home/johnandersen777/src/publicdomainrelay-kcp|$ORG_ROOT|g" \
      -e "s|__PDS_PRIVATE_KEY_HEX__|$key|g" \
      -e "s|__BOB_PDS_PRIVATE_KEY_HEX__|$bob_key|g" \
      -e "s|__BIDDER_PRIVATE_KEY_HEX__|$bidder_key|g" \
      -e "s|__BIDDER_NAME__|$BIDDER_NAME|g" \
      -e "s|__BED_ID__|$BED_ID|g" \
      -e "s|__STATE_DIR__|$STATE_DIR|g" \
      -e "s|__DENO_DIR__|$POD_DENO_DIR|g" \
      -e "s|__DISPATCHER_PORT__|$DISPATCHER_PORT|g" \
      -e "s|__GUEST_TLS_PORT__|$GUEST_TLS_PORT|g" \
      -e "s|__FC_NODEIMAGE__|$FC_NODEIMAGE|g" \
      -e "s|__FC_NODEBOOT__|$FC_NODEBOOT|g" \
      -e "s|__FC_CONFIG__|$FC_CONFIG|g" \
      -e "s|__FC_REPO_DIR__|$FC_REPO_DIR|g" \
      -e "s|__FC_PREINSTALL__|$FC_PREINSTALL|g" \
      -e "s|__FC_VMM__|$FC_VMM|g" \
      -e "s|__FC_RUNNER_IMAGE__|$FC_RUNNER_IMAGE|g" \
      -e "s|__FC_WORK_ROOT__|$FC_WORK_ROOT|g" \
      -e "s|__GUEST_TLS_DIR__|$GUEST_TLS_DIR|g" \
      "$MARKET_DIR/$file" \
    | KC --server="$S/clusters/root:$ws" apply --validate=false -f -
}

# The address a pod's process reported binding, once it has. Bounded by
# WAIT_SECONDS; an empty answer is the caller's failure to report.
pod_address() {
  local ws=$1 name=$2 deadline address
  deadline=$(( $(date +%s) + WAIT_SECONDS ))
  while :; do
    address=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.address}' 2>/dev/null || true)
    if [ -n "$address" ]; then
      echo "$address"
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "root:$ws $name never reported the address it bound within ${WAIT_SECONDS}s" >&2
      KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status}' >&2 2>/dev/null || true
      return 1
    fi
    sleep 1
  done
}

# Each workspace's namespace names the OpenBao namespace that serves it. This
# goes before the workloads so the authority exists by the time one asks for a
# certificate, which is also the state an operator wants to see when a workload
# does not come up serving TLS.
for ws in $WORKSPACES; do
  apply_pod "$ws" "15-openbao-$ws.yaml" >/dev/null
done
# A provider that reconciles anything readies these within seconds, so this
# wait is shorter than the others: it is how accept.sh learns, sooner, that a
# provider started before its export endpoints and needs a restart.
OPENBAO_WAIT_SECONDS=${OPENBAO_WAIT_SECONDS:-$WAIT_SECONDS}
for ws in $WORKSPACES; do
  tries=0
  until [ "$(KC --server="$S/clusters/root:$ws" get openbao openbao -o jsonpath='{.status.ready}' 2>/dev/null)" = "true" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$OPENBAO_WAIT_SECONDS" ]; then
      echo "the OpenBao namespace for root:$ws never became ready within ${OPENBAO_WAIT_SECONDS}s" >&2
      KC --server="$S/clusters/root:$ws" get openbao openbao -o yaml >&2 || true
      exit 1
    fi
    sleep 1
  done
  echo "root:$ws OpenBao authority ready"
done

# Order still matters for one reason: a pod's own argv can only carry a port
# that is already known. The dispatcher and its TLS front come up first because
# every bidder's argv names their ports (the bidder writes them into its
# guests' user_data). Everything else finds its peers by name, through a table
# the provider rewrites as each pod reports the port it bound.
apply_pod global 20-global-plc.yaml
# socialweb-computer-kcp: the market's XRPC dispatcher, which the bidder, the
# requester and every guest register with. It resolves did:plc signers through
# the PLC directory above.
apply_pod relay  35-relay-dispatcher.yaml
DISPATCHER_ADDRESS=$(pod_address relay dispatcher)
DISPATCHER_PORT=${DISPATCHER_ADDRESS##*:}
apply_pod relay  36-relay-dispatcher-tls.yaml
DISPATCHER_TLS_ADDRESS=$(pod_address relay dispatcher-tls)
GUEST_TLS_PORT=${DISPATCHER_TLS_ADDRESS##*:}
echo "dispatcher: $DISPATCHER_ADDRESS  guest TLS front: $DISPATCHER_TLS_ADDRESS"
apply_pod alice  40-alice-pds.yaml
apply_pod bob    60-bob-pds.yaml
apply_pod relay  30-relay-relay.yaml
for BIDDER_NAME in $BIDDERS; do
  bidder_key=$(resolve_key "" "$KEY_DIR/$BIDDER_NAME.hex" "$BIDDER_NAME")
  if [ "$BIDDER_NAME" = "bidder-1" ] && [ -n "${BIDDER_PRIVATE_KEY_HEX:-}" ]; then
    bidder_key=$BIDDER_PRIVATE_KEY_HEX
  fi
  apply_pod bob 70-bidder.yaml
done
BIDDER_NAME=""
bidder_key=""

# The verifier reaches alice's PDS, the PLC directory and the relay, so a fixed
# sleep in front of a pod whose restartPolicy is Never waits for nothing. What is
# waited for instead is the provider's own verdict on alice's PDS -- phase
# Running with ready true is the readiness probe resolving
# pds.default.alice.svc.kcp.local through the shim and answering on
# /xrpc/_health -- and the relay's reported address, because the verifier
# watches the relay over a WebSocket. The wait shares the bounded WAIT_SECONDS
# deadline used everywhere else, and when it expires the verifier is created
# anyway, so the run reports a verifier that could not reach its peers instead
# of stopping here.
verifier_deadline=$(( $(date +%s) + WAIT_SECONDS ))
while :; do
  phase=$(KC --server="$S/clusters/root:alice" get denopod pds -o jsonpath='{.status.phase}' 2>/dev/null || true)
  ready=$(KC --server="$S/clusters/root:alice" get denopod pds -o jsonpath='{.status.ready}' 2>/dev/null || true)
  relay=$(KC --server="$S/clusters/root:relay" get denopod relay -o jsonpath='{.status.address}' 2>/dev/null || true)
  [ "$phase" = "Running" ] && [ "$ready" = "true" ] && [ -n "$relay" ] && break
  if [ "$(date +%s)" -ge "$verifier_deadline" ]; then
    echo "alice pds never reported Running and ready (or the relay no address) within ${WAIT_SECONDS}s; creating the verifier anyway so the run reports it" >&2
    break
  fi
  sleep 2
done
apply_pod alice  50-verifier.yaml

PODS="global/plc relay/relay relay/dispatcher relay/dispatcher-tls alice/pds bob/pds"
for b in $BIDDERS; do PODS="$PODS bob/$b"; done

deadline=$(( $(date +%s) + WAIT_SECONDS ))
for entry in $PODS; do
  ws=${entry%%/*}; name=${entry#*/}
  while :; do
    phase=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    ready=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.ready}' 2>/dev/null || true)
    [ "$ready" = "true" ] && break
    [ "$(date +%s)" -ge "$deadline" ] && break
    sleep 2
  done
  address=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.address}' 2>/dev/null || true)
  printf 'root:%-7s %-15s phase=%s ready=%s address=%s\n' "$ws" "$name" "${phase:-unknown}" "${ready:-unknown}" "${address:-none}"
done

addr_of() {
  KC --server="$S/clusters/root:$1" get denopod "$2" -o jsonpath='{.status.address}' 2>/dev/null || true
}
mkdir -p "$(dirname "$ADDRESSES")"
{
  echo "PLC_ADDRESS=$(addr_of global plc)"
  echo "RELAY_ADDRESS=$(addr_of relay relay)"
  echo "DISPATCHER_ADDRESS=$DISPATCHER_ADDRESS"
  echo "DISPATCHER_TLS_ADDRESS=$DISPATCHER_TLS_ADDRESS"
  echo "ALICE_PDS_ADDRESS=$(addr_of alice pds)"
  echo "BOB_PDS_ADDRESS=$(addr_of bob pds)"
  echo "BIDDERS=\"$BIDDERS\""
  for b in $BIDDERS; do
    echo "BIDDER_ADDRESS_${b//-/_}=$(addr_of bob "$b")"
  done
} >"$ADDRESSES.tmp"
mv "$ADDRESSES.tmp" "$ADDRESSES"

echo
echo "waiting for the verifier, which creates an account on the PDS, reads it back"
echo "from the PLC directory and watches the relay for one of its commits:"
verdict=""
for _ in $(seq 1 90); do
  phase=$(KC --server="$S/clusters/root:alice" get denopod verifier -o jsonpath='{.status.phase}' 2>/dev/null || true)
  case "$phase" in
    Succeeded|Failed) verdict=$phase; break ;;
  esac
  sleep 2
done
printf 'verifier phase=%s\n' "${verdict:-unknown}"
KC --server="$S/clusters/root:alice" get denopod verifier -o jsonpath='{.status.outputs}' 2>/dev/null | tr ',' '\n' | sed 's/^ *//'
echo

echo
echo "authorities:"
for ws in $WORKSPACES; do
  echo "  root:$ws $(KC --server="$S/clusters/root:$ws" get openbao openbao -o jsonpath='{.status.namespace} serial={.status.serial}')"
done
echo
echo "addresses (also in $ADDRESSES):"
cat "$ADDRESSES" | sed 's/^/  /'
echo
echo "health: every service pod sets SERVICE_TLS true except the bidders, so their"
echo "listeners are https except the bidders'. The service names do not resolve on"
echo "the host, so a check resolves the name to the loopback address its listener"
echo "holds and verifies the leaf against the market CA, read out of OpenBao first:"
echo
echo "  curl -sS -H 'X-Vault-Token: root' http://${OPENBAO_ADDR:-<openbao>}/v1/pki/ca/pem > ca.pem"
alice_port=$(addr_of alice pds); alice_port=${alice_port##*:}
echo "  curl --cacert ca.pem --resolve pds.default.alice.svc.kcp.local:$alice_port:127.0.0.1 https://pds.default.alice.svc.kcp.local:$alice_port/xrpc/_health"
