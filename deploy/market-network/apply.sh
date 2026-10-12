#!/usr/bin/env bash
set -euo pipefail

# Deploys the AT Protocol market topology: a PLC directory in root:global, a relay
# in root:relay, a PDS in root:alice, and in root:bob a second PDS plus
# MARKET_NETWORK_BIDDERS market bidders (bidder-1 .. bidder-N). Every service
# DenoPod is rendered from the one service template, deploy/denopods/service.yaml:
# 20-70 are its values (entry, cwd, args, env, state dir, explicit deno
# permissions, probe), and the template's supervisor runs the entry on the
# pinned deno from the TypeScript source root the deno runtime names.
#
# Every service binds port 0. Its listeners reach its DenoPod's status.listeners
# and status.address (the service writes LISTENERS_FILE and PORT_FILE in its run
# directory and the provider reads them back), peers reach it by its
# cluster-local name through the kcpdns shim, and the market-network set's waits
# capture status.address wherever a port has to be rendered into another pod:
# the dispatcher's and its TLS front's ports go into every bidder's argv,
# because the bidder hands them to its guests.
#
# Requires a running core whose deno runtime reconciles DenoPods (socialweb-computer
# dev up, which applies the denoruntime set). See README.md in this directory for
# what does and does not work between the services.
#
# MARKET_NETWORK_ID       names this market network in its guests' container labels (default demo)
# MARKET_NETWORK_DIR      this market network's state directory (default <repo>/.kcp-demo); keys, the
#              guest CA and every pod's state directory live under it
# MARKET_NETWORK_BIDDERS  how many bidders to run (default 1)
# ADDRESSES    file the read-back addresses are written to (default
#              MARKET_NETWORK_DIR/addresses.env)
# SOCIALWEB_COMPUTER the socialweb-computer binary that applies the set (default:
#              built from this repository into MARKET_NETWORK_DIR/bin)

REPO=$(cd "$(dirname "$0")/../.." && pwd)
DENOKCP_DIR=$REPO/deploy/denokcp
MARKET_DIR=$(cd "$(dirname "$0")" && pwd)
MARKET_NETWORK_ID=${MARKET_NETWORK_ID:-demo}
MARKET_NETWORK_DIR=${MARKET_NETWORK_DIR:-$REPO/.kcp-demo}
MARKET_NETWORK_BIDDERS=${MARKET_NETWORK_BIDDERS:-1}
STATE_DIR=${STATE_DIR:-$MARKET_NETWORK_DIR/state}
ADDRESSES=${ADDRESSES:-$MARKET_NETWORK_DIR/addresses.env}
KUBECTL=${KUBECTL:-kubectl}
# kubectl caches discovery per server and the cache outlives a schema change: a
# cached NAMESPACED=false makes it POST a namespaced resource without a
# namespace, which kcp answers with a NotFound that names the resource and
# nothing else. A throwaway cache costs a round trip and removes the trap.
KCACHE=${KCACHE:-$(mktemp -d)}
trap 'rm -rf "$KCACHE"' EXIT
KC() { "$KUBECTL" --cache-dir="$KCACHE" "$@"; }
export KUBECONFIG=${KUBECONFIG:-$MARKET_NETWORK_DIR/admin.kubeconfig}
WAIT_SECONDS=${WAIT_SECONDS:-120}
KEY_DIR=${KEY_DIR:-$MARKET_NETWORK_DIR/keys}
KEY_FILE=${KEY_FILE:-$KEY_DIR/alice-pds.hex}
BOB_KEY_FILE=${BOB_KEY_FILE:-$KEY_DIR/bob-pds.hex}

WORKSPACES="global relay alice bob"

case "$MARKET_NETWORK_BIDDERS" in
  ''|*[!0-9]*|0) echo "MARKET_NETWORK_BIDDERS must be a positive number, got '$MARKET_NETWORK_BIDDERS'" >&2; exit 1 ;;
esac
BIDDERS=$(seq -f 'bidder-%g' 1 "$MARKET_NETWORK_BIDDERS" | tr '\n' ' ')

S=$(KC config view --minify -o jsonpath='{.clusters[0].cluster.server}')
S=${S%%/clusters/*}
ROOT_SERVER="$S/clusters/root"

echo "source root: $REPO"
echo "root server: $ROOT_SERVER"
echo "market network:         $MARKET_NETWORK_ID ($MARKET_NETWORK_DIR) bidders: $BIDDERS"

for bin in ts/atproto-market/hono-plc ts/atproto-market/hono-bidder ts/atproto-relay/hono-atproto-relay ts/hono-pds ts/did-key-ingress-proxy/hono-did-key-ingress-proxy; do
  [ -e "$REPO/$bin" ] || { echo "missing $REPO/$bin" >&2; exit 1; }
done

# socialweb-computer-kcp: the bidder runs --compute-provider-firecracker. Every
# input below is a path on this host or in the runner image, and none has a
# default that would be right on another machine, so a missing one stops the
# apply here rather than at the first RFP. FC_NODEBOOT and FC_VMM name paths
# inside the runner image, which is what boots each guest.
FC_NODEBOOT=${FC_NODEBOOT:-/usr/local/bin/socialweb-nodeboot}
FC_VMM=${FC_VMM:-/usr/local/bin/firecracker}
missing=""
need="FC_NODEIMAGE FC_CONFIG FC_REPO_DIR FC_PREINSTALL FC_RUNNER_IMAGE FC_WORK_ROOT"
[ -n "${FC_VMRUNTIME_COMMAND:-}" ] && need="FC_WORK_ROOT"
for v in $need; do
  [ -n "${!v:-}" ] || missing="$missing $v"
done
if [ -n "$missing" ]; then
  echo "the firecracker bidder needs:$missing (see deploy/market-network/accept.sh)" >&2
  exit 1
fi
mkdir -p "$FC_WORK_ROOT"
FC_EXTRA_ARGS=""
FC_EXTRA_RUN=""
FC_EXTRA_READ=""
FC_EXTRA_WRITE=""
if [ -n "${FC_VMRUNTIME_COMMAND:-}" ]; then
  for v in FC_NODEIMAGE FC_CONFIG FC_REPO_DIR FC_PREINSTALL FC_RUNNER_IMAGE; do
    [ -n "${!v:-}" ] || printf -v "$v" '%s' "/unused-with-vmruntime"
  done
  FC_EXTRA_ARGS=",\"--compute-provider-firecracker-vmruntime-command\",$(jq -cn --arg v "$FC_VMRUNTIME_COMMAND" '$v')"
  FC_EXTRA_RUN=",$(jq -c '.[0]' <<<"$FC_VMRUNTIME_COMMAND")"
fi
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
GUEST_TLS_DIR=${GUEST_TLS_DIR:-$MARKET_NETWORK_DIR/guest-tls}
if [ ! -s "$GUEST_TLS_DIR/relay.localhost.crt" ]; then
  mkdir -p "$GUEST_TLS_DIR"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
    -subj "/CN=market network guest CA" \
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

# The certificates come from OpenBao: the root CA lives in its root namespace and
# each workspace's namespace gets an intermediate signed by it. The provider has
# to have been started against the same vault -- see deploy/denokcp/start-openbao.sh,
# which binds port 0 and writes the address it got to OPENBAO_DIR/bao.addr.
OPENBAO_DIR=${OPENBAO_DIR:-$MARKET_NETWORK_DIR/openbao}
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

# Every manifest goes through the market-network set (deploy/sets.yaml): the
# values below are typed fields of internal/manifests.MarketNetwork, a field left
# unset is a render error rather than an empty string, and the set's waits
# capture the dispatcher's and its TLS front's bound ports. socialweb-computer
# applies it server side as field manager socialweb-computer; nothing here
# runs kubectl apply or sed over a manifest. The set runs in two halves
# because each bidder's account on bob's PDS has to exist before the bidder.
SWC=${SOCIALWEB_COMPUTER:-}
if [ -z "$SWC" ]; then
  SWC=$MARKET_NETWORK_DIR/bin/socialweb-computer
  mkdir -p "$(dirname "$SWC")"
  (cd "$REPO" && go build -o "$SWC" ./cmd/socialweb-computer)
fi

q() { printf '"%s"' "$1"; }
VALUES=${VALUES:-$MARKET_NETWORK_DIR/values.yaml}
mkdir -p "$(dirname "$VALUES")"
write_values() {
  (
    umask 077
    {
      echo "market-network:"
      echo "  NetworkID: $(q "$MARKET_NETWORK_ID")"
      echo "  StateDir: $(q "$STATE_DIR")"
      echo "  GuestTLSDir: $(q "$GUEST_TLS_DIR")"
      echo "  PDSPrivateKeyHex: $(q "$key")"
      echo "  BobPDSPrivateKeyHex: $(q "$bob_key")"
      [ -n "$DISPATCHER_PORT" ] && echo "  DispatcherPort: $DISPATCHER_PORT"
      [ -n "$GUEST_TLS_PORT" ] && echo "  GuestTLSPort: $GUEST_TLS_PORT"
      echo "  Bidders:"
      for BIDDER_NAME in $BIDDERS; do
        bidder_key=$(resolve_key "" "$KEY_DIR/$BIDDER_NAME.hex" "$BIDDER_NAME")
        if [ "$BIDDER_NAME" = "bidder-1" ] && [ -n "${BIDDER_PRIVATE_KEY_HEX:-}" ]; then
          bidder_key=$BIDDER_PRIVATE_KEY_HEX
        fi
        echo "    - Name: $(q "$BIDDER_NAME")"
        echo "      Handle: $(q "$BIDDER_NAME.bob.test")"
        echo "      PrivateKeyHex: $(q "$bidder_key")"
      done
      echo "  Firecracker:"
      echo "    NodeImage: $(q "$FC_NODEIMAGE")"
      echo "    NodeBoot: $(q "$FC_NODEBOOT")"
      echo "    Config: $(q "$FC_CONFIG")"
      echo "    RepoDir: $(q "$FC_REPO_DIR")"
      echo "    Preinstall: $(q "$FC_PREINSTALL")"
      echo "    VMM: $(q "$FC_VMM")"
      echo "    RunnerImage: $(q "$FC_RUNNER_IMAGE")"
      echo "    WorkRoot: $(q "$FC_WORK_ROOT")"
      echo "    ExtraArgs: '$FC_EXTRA_ARGS'"
      echo "    ExtraRead: '$FC_EXTRA_READ'"
      echo "    ExtraWrite: '$FC_EXTRA_WRITE'"
      echo "    ExtraRun: '$FC_EXTRA_RUN'"
    } >"$VALUES.tmp"
    mv "$VALUES.tmp" "$VALUES"
  )
}
apply_set() {
  "$SWC" apply --set market-network --values "$VALUES" --phase "$1" --kcp-kubeconfig "$KUBECONFIG" \
    --wait-timeout "${WAIT_SECONDS}s" >"$2"
  echo "applied market-network phases $1; report in $2"
}
addr_of() {
  KC --server="$S/clusters/root:$1" get denopod "$2" -o jsonpath='{.status.address}' 2>/dev/null || true
}

DISPATCHER_PORT=""
GUEST_TLS_PORT=""
write_values
apply_set workspaces..relay "$MARKET_NETWORK_DIR/apply-report-1.json"
DISPATCHER_ADDRESS=$(addr_of relay dispatcher)
DISPATCHER_TLS_ADDRESS=$(addr_of relay dispatcher-tls)
DISPATCHER_PORT=${DISPATCHER_ADDRESS##*:}
GUEST_TLS_PORT=${DISPATCHER_TLS_ADDRESS##*:}
echo "dispatcher: $DISPATCHER_ADDRESS  guest TLS front: $DISPATCHER_TLS_ADDRESS"

# Every bidder signs in to an account of its own on bob's PDS (pdsAccount) and
# writes its offering, bids and receipts there, so the account has to exist
# before the bidder starts. The password never enters kcp: it is generated
# here, kept in a mode 600 vault file under the bidder's state directory (the
# market network has no OpenBao KV wiring for a DenoPod yet), and the DenoPod names only
# the file's path. A second run finds the file and signs in with it.
BOB_PDS_NAME=pds.default.bob.svc.kcp.local
BOB_PDS_ADDRESS=$(addr_of bob pds)
BOB_PDS_PORT=${BOB_PDS_ADDRESS##*:}
MARKET_CA=$MARKET_NETWORK_DIR/market-ca.pem
curl -sS --fail --max-time 10 --header "X-Vault-Token: root" "http://$OPENBAO_ADDR/v1/pki/ca/pem" -o "$MARKET_CA"
bob_pds_post() {
  curl -sS --max-time 15 --cacert "$MARKET_CA" --resolve "$BOB_PDS_NAME:$BOB_PDS_PORT:127.0.0.1" \
    --header 'content-type: application/json' --data-binary @- \
    "https://$BOB_PDS_NAME:$BOB_PDS_PORT/xrpc/$1"
}
tries=0
until curl -sS --fail --max-time 5 --cacert "$MARKET_CA" --resolve "$BOB_PDS_NAME:$BOB_PDS_PORT:127.0.0.1" \
    -o /dev/null "https://$BOB_PDS_NAME:$BOB_PDS_PORT/xrpc/_health" 2>/dev/null; do
  tries=$((tries + 1))
  if [ "$tries" -ge "$WAIT_SECONDS" ]; then
    echo "bob's PDS at $BOB_PDS_ADDRESS never answered /xrpc/_health within ${WAIT_SECONDS}s" >&2
    exit 1
  fi
  sleep 1
done

ensure_bidder_account() {
  local name=$1 handle=$2 dir=$3 vault password out did
  vault=$dir/identity-vault.json
  mkdir -p "$dir"
  chmod 700 "$dir"
  password=$(python3 -c 'import json,sys
try:
    print(json.load(open(sys.argv[1]))["bidder/identity/bidder/keys"]["password"])
except Exception:
    print("")' "$vault")
  if [ -z "$password" ]; then
    password=$(openssl rand -hex 24)
    printf '%s' "$password" | python3 -c 'import json,os,sys
path = sys.argv[1]
data = {}
if os.path.exists(path):
    data = json.load(open(path))
data["bidder/identity/bidder/keys"] = {"password": sys.stdin.read()}
tmp = path + ".tmp"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
os.write(fd, (json.dumps(data, indent=2) + "\n").encode())
os.close(fd)
os.chmod(tmp, 0o600)
os.replace(tmp, path)' "$vault"
  fi
  out=$(printf '%s' "$password" | python3 -c 'import json,sys; print(json.dumps({"handle": sys.argv[1], "email": sys.argv[1] + "@bob.test", "password": sys.stdin.read()}))' "$handle" \
    | bob_pds_post com.atproto.server.createAccount || true)
  did=$(printf '%s' "$out" | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("did") or "")
except Exception:
    print("")')
  if [ -z "$did" ]; then
    out=$(printf '%s' "$password" | python3 -c 'import json,sys; print(json.dumps({"identifier": sys.argv[1], "password": sys.stdin.read()}))' "$handle" \
      | bob_pds_post com.atproto.server.createSession || true)
    did=$(printf '%s' "$out" | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("did") or "")
except Exception:
    print("")')
  fi
  if [ -z "$did" ]; then
    echo "could not create or sign in to $handle on bob's PDS: $(printf '%s' "$out" | head -c 200)" >&2
    return 1
  fi
  printf '%s\n' "$did" >"$dir/account.did"
  echo "$name: account $handle on bob's PDS is $did (password in $vault)"
}

for BIDDER_NAME in $BIDDERS; do
  ensure_bidder_account "$BIDDER_NAME" "$BIDDER_NAME.bob.test" "$STATE_DIR/bob-$BIDDER_NAME"
done
BIDDER_NAME=""

write_values
apply_set bidders..verifier "$MARKET_NETWORK_DIR/apply-report-2.json"

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
