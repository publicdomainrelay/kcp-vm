#!/usr/bin/env bash
set -euo pipefail

# Deploys the AT Protocol market topology: a PLC directory in root:global, a relay
# in root:relay, a PDS in root:alice, and in root:bob a second PDS plus a market
# bidder. Each DenoPod runs its service as a supervised child process of its own
# deno entrypoint, because these are CLI entrypoints that own their argv, not
# libraries.
#
# Requires the provider to be installed first (deploy/install-provider.sh) and
# the sibling repositories checked out: atproto-market, atproto-relay, hono-pds,
# typescript-helpers. See README.md in this directory for what does and does not
# work between the services.

REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
DENOKCP_DIR=$REPO/deploy/denokcp
MARKET_DIR=$(cd "$(dirname "$0")" && pwd)
ORG_ROOT=${ORG_ROOT:-$(cd "$REPO/.." && pwd)}
KUBECTL=${KUBECTL:-kubectl}
# kubectl caches discovery per server and the cache outlives a schema change: a
# cached NAMESPACED=false makes it POST a namespaced resource without a
# namespace, which kcp answers with a NotFound that names the resource and
# nothing else. A throwaway cache costs a round trip and removes the trap.
KCACHE=${KCACHE:-$(mktemp -d)}
trap 'rm -rf "$KCACHE"' EXIT
KC() { "$KUBECTL" --cache-dir="$KCACHE" "$@"; }
export KUBECONFIG=${KUBECONFIG:-$REPO/.kcp-demo/admin.kubeconfig}
WAIT_SECONDS=${WAIT_SECONDS:-120}
KEY_FILE=${KEY_FILE:-$REPO/.kcp-demo/atproto-market-pds-key.hex}
BOB_KEY_FILE=${BOB_KEY_FILE:-$REPO/.kcp-demo/atproto-market-bob-pds-key.hex}
BIDDER_KEY_FILE=${BIDDER_KEY_FILE:-$REPO/.kcp-demo/atproto-market-bidder-key.hex}

WORKSPACES="global relay alice bob"

S=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
S=${S%%/clusters/*}
ROOT_SERVER="$S/clusters/root"

K() { KC --server="$1" "${@:2}"; }

echo "org root:   $ORG_ROOT"
echo "root server: $ROOT_SERVER"

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

# socialweb-computer-kcp: a guest reaches the dispatcher only over https (its
# OIDC proof and its onNetwork report), at <name>.relay.localhost on the
# bidder's --guest-tls-port. 36-relay-dispatcher-tls.yaml terminates that TLS
# with a leaf for relay.localhost and *.relay.localhost signed by this CA, and
# the requester has the guest trust the CA through its own user_data.
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$REPO/.kcp-demo/guest-tls}
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
# to have been started against the same vault -- see deploy/start-openbao.sh.
OPENBAO_LISTEN=${OPENBAO_LISTEN:-127.0.0.1:8200}
if [ "${START_OPENBAO:-1}" = "1" ]; then
  OPENBAO_LISTEN="$OPENBAO_LISTEN" bash "$DENOKCP_DIR/start-openbao.sh"
fi

# Three signing keys, one per identity: alice's PDS, bob's PDS and the bidder.
# Each is overridable, each persists in its own file, and a key that showed up
# twice would make two identities collide, so a distinct one is generated for
# each that is not already spoken for.
resolve_key() {
  local env=$1 file=$2 label=$3
  if [ -n "${!env:-}" ]; then
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
bidder_key=$(resolve_key BIDDER_PRIVATE_KEY_HEX "$BIDDER_KEY_FILE" "bidder")

# Each pod's manifest carries its own placeholder, so one substitution pass can
# never hand two pods the same key.
apply_pod() {
  local ws=$1 file=$2
  sed -e "s|/home/johnandersen777/src/publicdomainrelay-kcp|$ORG_ROOT|g" \
      -e "s|__PDS_PRIVATE_KEY_HEX__|$key|g" \
      -e "s|__BOB_PDS_PRIVATE_KEY_HEX__|$bob_key|g" \
      -e "s|__BIDDER_PRIVATE_KEY_HEX__|$bidder_key|g" \
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

# Order matters, and only for the shim's sake. A workload's address table is
# written once, when it starts, so a consumer that starts before its producer
# never learns the producer's address. The relay subscribes to the PDS over a
# WebSocket, and WebSocket is constructed synchronously and so cannot fall back
# to discovery the way fetch does, which means the relay has to start after the
# PDS or it will never see a single commit.
# Each workspace's namespace names the OpenBao namespace that serves it. This
# goes before the workloads so the authority exists by the time one asks for a
# certificate, which is also the state an operator wants to see when a workload
# does not come up serving TLS.
for ws in $WORKSPACES; do
  apply_pod "$ws" "15-openbao-$ws.yaml" >/dev/null
done
for ws in $WORKSPACES; do
  tries=0
  until [ "$(KC --server="$S/clusters/root:$ws" get openbao openbao -o jsonpath='{.status.ready}' 2>/dev/null)" = "true" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$WAIT_SECONDS" ]; then
      echo "the OpenBao namespace for root:$ws never became ready" >&2
      KC --server="$S/clusters/root:$ws" get openbao openbao -o yaml >&2 || true
      exit 1
    fi
    sleep 1
  done
  echo "root:$ws OpenBao authority ready"
done

apply_pod global 20-global-plc.yaml
# socialweb-computer-kcp: the market's XRPC dispatcher, which the bidder, the
# requester and every guest register with. It resolves did:plc signers through
# the PLC directory above, so it starts after it.
apply_pod relay  35-relay-dispatcher.yaml
apply_pod relay  36-relay-dispatcher-tls.yaml
apply_pod alice  40-alice-pds.yaml
apply_pod bob    60-bob-pds.yaml
apply_pod relay  30-relay-relay.yaml
# The bidder reaches both the PLC directory and the relay, so it starts after
# both, and after bob's own PDS so the two share a workspace from the first
# reconcile.
apply_pod bob    70-bidder.yaml
# The verifier reaches alice's PDS, the PLC directory and the relay, so a fixed
# sleep in front of a pod whose restartPolicy is Never waits for nothing. What is
# waited for instead is the provider's own verdict on alice's PDS: phase Running
# with ready true is the readiness probe resolving pds.default.alice.svc.kcp.local
# through the shim's table and answering on /xrpc/_health, which is the same name
# and the same path the verifier uses. The wait shares the bounded WAIT_SECONDS
# deadline used everywhere else, and when it expires the verifier is created
# anyway, so the run reports a verifier that could not reach its peers instead of
# stopping here.
verifier_deadline=$(( $(date +%s) + WAIT_SECONDS ))
while :; do
  phase=$(KC --server="$S/clusters/root:alice" get denopod pds -o jsonpath='{.status.phase}' 2>/dev/null || true)
  ready=$(KC --server="$S/clusters/root:alice" get denopod pds -o jsonpath='{.status.ready}' 2>/dev/null || true)
  [ "$phase" = "Running" ] && [ "$ready" = "true" ] && break
  if [ "$(date +%s)" -ge "$verifier_deadline" ]; then
    echo "alice pds in root:alice never reported Running and ready within ${WAIT_SECONDS}s; creating the verifier anyway so the run reports it" >&2
    break
  fi
  sleep 2
done
apply_pod alice  50-verifier.yaml

deadline=$(( $(date +%s) + WAIT_SECONDS ))
for entry in "global plc" "relay relay" "relay dispatcher" "relay dispatcher-tls" "alice pds" "bob pds" "bob bidder"; do
  set -- $entry
  ws=$1; name=$2
  while :; do
    phase=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    ready=$(KC --server="$S/clusters/root:$ws" get denopod "$name" -o jsonpath='{.status.ready}' 2>/dev/null || true)
    [ "$ready" = "true" ] && break
    [ "$(date +%s)" -ge "$deadline" ] && break
    sleep 2
  done
  printf 'root:%-7s %-6s phase=%s ready=%s\n' "$ws" "$name" "${phase:-unknown}" "${ready:-unknown}"
done

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
echo "health: every service pod sets SERVICE_TLS true except the bidder, so the"
echo "listeners below are https except the bidder's. The service names do not"
echo "resolve on the host, so each check resolves the name to the loopback address"
echo "its listener holds, and verifies the leaf against the market CA instead of"
echo "disabling verification. Read that CA out of OpenBao's root namespace first:"
echo
echo "  curl -sS -H 'X-Vault-Token: root' http://$OPENBAO_LISTEN/v1/pki/ca/pem > ca.pem"
echo
echo "  plc      curl --cacert ca.pem --resolve plc.default.global.svc.kcp.local:2587:127.0.0.1 https://plc.default.global.svc.kcp.local:2587/health"
echo "  relay    curl --cacert ca.pem --resolve relay.default.relay.svc.kcp.local:2584:127.0.0.1 https://relay.default.relay.svc.kcp.local:2584/xrpc/_health"
echo "  alice    curl --cacert ca.pem --resolve pds.default.alice.svc.kcp.local:2583:127.0.0.1 https://pds.default.alice.svc.kcp.local:2583/xrpc/_health"
echo "  bob pds  curl --cacert ca.pem --resolve pds.default.bob.svc.kcp.local:2585:127.0.0.1 https://pds.default.bob.svc.kcp.local:2585/xrpc/_health"
echo "  bidder   curl http://127.0.0.1:2586/oauth-client-metadata.json  (plain HTTP by design)"
echo
echo "create alice:"
echo "  curl --cacert ca.pem --resolve pds.default.alice.svc.kcp.local:2583:127.0.0.1 \\"
echo "    -sS -X POST https://pds.default.alice.svc.kcp.local:2583/xrpc/com.atproto.server.createAccount \\"
echo "    -H 'content-type: application/json' \\"
echo "    -d '{\"handle\":\"alice\",\"password\":\"hunter2\"}'"
