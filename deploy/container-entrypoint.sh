#!/bin/sh
set -eu

if [ "$#" -gt 0 ]; then
	exec "$@"
fi

STATE_DIR="${SOCIALWEB_STATE_DIR:-/var/lib/socialweb}"
KCP_ROOT="${SOCIALWEB_KCP_ROOT:-$STATE_DIR/kcp}"
KUBECTL_BIN="${SOCIALWEB_KUBECTL:-/usr/local/bin/kubectl}"
DEPLOY_DIR="${SOCIALWEB_DEPLOY_DIR:-/opt/socialweb/deploy}"
AUDIT_POLICY="${SOCIALWEB_KCP_AUDIT_POLICY:-$DEPLOY_DIR/kcp-audit-policy.yaml}"
AUDIT_LOG="${SOCIALWEB_KCP_AUDIT_LOG:-$KCP_ROOT/audit.log}"
MINT_DIR="${SOCIALWEB_GATEWAY_MINT_KUBECONFIGS:-$STATE_DIR/mint}"
IMAGE_STORE="${SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR:-$STATE_DIR/images}"
IMAGE_CONFIG="${SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG:-/home/johnandersen777/kcp-image/nodeimage.json}"
TLS_PORT="${SOCIALWEB_CONTAINER_TLS_PORT:-8443}"
TLS_CERT="${SOCIALWEB_CONTAINER_TLS_CERT:-}"
TLS_KEY="${SOCIALWEB_CONTAINER_TLS_KEY:-}"
TLS_DIR="${SOCIALWEB_CONTAINER_TLS_DIR:-$STATE_DIR/tls}"
ROOT_VAULT_DIR="$STATE_DIR/openbao"
ROOT_VAULT_CONFIG="$ROOT_VAULT_DIR/root.hcl"
ROOT_VAULT_ADDR="127.0.0.1:8200"
export SOCIALWEB_ROOTVAULT_ADDR="$ROOT_VAULT_ADDR"

export SOCIALWEB_KCP_HOST="${SOCIALWEB_KCP_HOST:-https://127.0.0.1:6443}"
export SOCIALWEB_KCP_KUBECONFIG="${SOCIALWEB_KCP_KUBECONFIG:-$KCP_ROOT/admin.kubeconfig}"
export AUDIT_LOG

export SOCIALWEB_GATEWAY_ADDR="${SOCIALWEB_GATEWAY_ADDR:-127.0.0.1:8080}"
export SOCIALWEB_GATEWAY_AUDIENCES="${SOCIALWEB_GATEWAY_AUDIENCES:-did:web:socialweb.local}"
export SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE="${SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE:-${SOCIALWEB_GATEWAY_AUDIENCES%%,*}}"
export SOCIALWEB_GATEWAY_PLC_URL="${SOCIALWEB_GATEWAY_PLC_URL:-https://plc.directory}"
export SOCIALWEB_GATEWAY_DEVKEY_REGISTRY="${SOCIALWEB_GATEWAY_DEVKEY_REGISTRY:-root:socialweb-provider}"
export SOCIALWEB_GATEWAY_MINT_KUBECONFIGS="$MINT_DIR"
export SOCIALWEB_GATEWAY_DEVICE_KEY_TTL="${SOCIALWEB_GATEWAY_DEVICE_KEY_TTL:-24h}"
export SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_COMMAND="${SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_COMMAND:-/usr/local/bin/socialweb-credential}"
export SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME="${SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME:-SOCIALWEB_DEVICE_KEY}"

export SOCIALWEB_CLUSTERPROVIDER_HOST="${SOCIALWEB_CLUSTERPROVIDER_HOST:-$(hostname)}"
export SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE="${SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE:-firecracker}"
export SOCIALWEB_CLUSTERPROVIDER_NETWORK="${SOCIALWEB_CLUSTERPROVIDER_NETWORK:-pasta}"
export SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR="$IMAGE_STORE"
export SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG="$IMAGE_CONFIG"
if [ "$SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE" = "qemu" ]; then
	export SOCIALWEB_CLUSTERPROVIDER_QEMU="${SOCIALWEB_CLUSTERPROVIDER_QEMU:-/usr/local/bin/qemu-system-x86_64}"
	unset SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER
else
	export SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER="${SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER:-/usr/local/bin/firecracker}"
fi
export SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT="${SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT:-/usr/local/bin/kcpagent}"
export SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER="${SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER:-/usr/local/bin/socialweb-cloud-controller-manager}"
export SOCIALWEB_CLUSTERPROVIDER_STATE_DIR="${SOCIALWEB_CLUSTERPROVIDER_STATE_DIR:-$STATE_DIR/clusterprovider}"
export SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH="${SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH:-$STATE_DIR/leases.json}"
export SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE="${SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE:-20000-29999}"
export SOCIALWEB_CLUSTERPROVIDER_GRANT="${SOCIALWEB_CLUSTERPROVIDER_GRANT:-$DEPLOY_DIR/tenant-grant.yaml}"
export SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW="${SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW:-false}"
export SOCIALWEB_CLUSTERPROVIDER_AUDIT_POLICY="${SOCIALWEB_CLUSTERPROVIDER_AUDIT_POLICY:-$AUDIT_POLICY}"

BOOTSTRAP=0
if [ -z "${SOCIALWEB_CLUSTERPROVIDER_JOIN:-}" ]; then
	SOCIALWEB_CLUSTERPROVIDER_JOIN="${SOCIALWEB_CONTAINER_CLUSTER:-socialweb}"
	BOOTSTRAP=1
fi
export SOCIALWEB_CLUSTERPROVIDER_JOIN

mkdir -p "$KCP_ROOT" "$MINT_DIR" "$STATE_DIR/clusterprovider" "$STATE_DIR/stage" "$ROOT_VAULT_DIR"

say() {
	echo "container: $*"
}

account_hash() {
	printf '%s' "$1" | sha256sum | awk '
	function b36(hex,   i,j,v,carry,t,L,D,out,zero) {
		L=1; D[1]=0
		for (i=1; i<=length(hex); i++) {
			v=index("0123456789abcdef",tolower(substr(hex,i,1)))-1
			carry=0
			for (j=L; j>=1; j--) { t=D[j]*16+carry; D[j]=t%10; carry=int(t/10) }
			while (carry>0) { for (j=L; j>=1; j--) D[j+1]=D[j]; D[1]=carry%10; carry=int(carry/10); L++ }
			carry=v
			for (j=L; j>=1 && carry>0; j--) { t=D[j]+carry; D[j]=t%10; carry=int(t/10) }
			while (carry>0) { for (j=L; j>=1; j--) D[j+1]=D[j]; D[1]=carry%10; carry=int(carry/10); L++ }
		}
		out=""
		while (1) {
			zero=1
			for (j=1; j<=L; j++) if (D[j]!=0) { zero=0; break }
			if (zero) break
			carry=0
			for (j=1; j<=L; j++) { t=carry*10+D[j]; D[j]=int(t/36); carry=t%36 }
			out=substr("0123456789abcdefghijklmnopqrstuvwxyz",carry+1,1) out
		}
		return out
	}
	{ print substr(b36($1),1,8) }
	'
}

tls_certificate() {
	if [ -n "$TLS_CERT" ] || [ -n "$TLS_KEY" ]; then
		if [ -z "$TLS_CERT" ] || [ -z "$TLS_KEY" ]; then
			say "SOCIALWEB_CONTAINER_TLS_CERT and SOCIALWEB_CONTAINER_TLS_KEY must be set together"
			exit 1
		fi
		[ -r "$TLS_CERT" ] || { say "the TLS certificate $TLS_CERT is not readable"; exit 1; }
		[ -r "$TLS_KEY" ] || { say "the TLS key $TLS_KEY is not readable"; exit 1; }
		return 0
	fi
	mkdir -p "$TLS_DIR"
	TLS_CERT="$TLS_DIR/tls.crt"
	TLS_KEY="$TLS_DIR/tls.key"
	if [ ! -s "$TLS_CERT" ] || [ ! -s "$TLS_KEY" ]; then
		openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3650 \
			-keyout "$TLS_KEY" -out "$TLS_CERT" \
			-subj "/CN=socialweb.local" \
			-addext "subjectAltName=DNS:localhost,DNS:socialweb.local,IP:127.0.0.1" >/dev/null 2>&1
		chmod 0600 "$TLS_KEY"
		chmod 0644 "$TLS_CERT"
	fi
}

K() {
	"$KUBECTL_BIN" --kubeconfig="$SOCIALWEB_KCP_KUBECONFIG" "$@"
}

ROOT_SERVER="$SOCIALWEB_KCP_HOST/clusters/root"

bootstrap_cluster() {
	cluster="$SOCIALWEB_CLUSTERPROVIDER_JOIN"
	did="${SOCIALWEB_CONTAINER_ACCOUNT_DID:-did:web:example.com}"
	record="$(account_hash "$did")"
	account="${SOCIALWEB_CONTAINER_ACCOUNT:-$record}"
	home="${SOCIALWEB_CONTAINER_HOME:-$cluster}"

	tries=0
	until K --server="$ROOT_SERVER" apply -f - <<YAML
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: ${account}
spec:
  type:
    name: account
    path: root
YAML
	do
		tries=$((tries + 1))
		[ "$tries" -ge 120 ] && return 1
		sleep 1
	done

	tries=0
	until K --server="$SOCIALWEB_KCP_HOST/clusters/root:${account}" apply -f - <<YAML
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: ${home}
spec:
  type:
    name: cluster
    path: root
YAML
	do
		tries=$((tries + 1))
		[ "$tries" -ge 120 ] && return 1
		sleep 1
	done

	tries=0
	until K --server="$SOCIALWEB_KCP_HOST/clusters/root:${account}" apply -f - <<YAML
apiVersion: socialweb.computer/v1alpha1
kind: AtprotoIdentity
metadata:
  name: ${record}
  annotations:
    socialweb.computer/sweep: exempt
spec:
  did: ${did}
  scope: Account
  revoked: false
YAML
	do
		tries=$((tries + 1))
		[ "$tries" -ge 120 ] && return 1
		sleep 1
	done

	tries=0
	until K --server="$SOCIALWEB_KCP_HOST/clusters/root:${account}:${home}" apply -f - <<YAML
apiVersion: socialweb.computer/v1alpha1
kind: Cluster
metadata:
  name: ${cluster}
spec:
  account: ${did}
  running: true
YAML
	do
		tries=$((tries + 1))
		[ "$tries" -ge 120 ] && return 1
		sleep 1
	done

	tries=0
	until K --server="$SOCIALWEB_KCP_HOST/clusters/root:${account}:${home}" \
		get cluster "$cluster" -o name >/dev/null 2>&1; do
		tries=$((tries + 1))
		[ "$tries" -ge 120 ] && return 1
		sleep 1
	done
	return 0
}

say "materialising the root vault's mutual TLS material under $ROOT_VAULT_DIR"
if ! SOCIALWEB_STATE_DIR="$STATE_DIR" /usr/local/bin/socialweb-vault prepare-root; then
	say "the root vault's TLS material could not be written, so its listener has no certificate, no CA to verify clients against, and no client certificate its consumers could present"
	exit 1
fi

say "starting the root vault on $ROOT_VAULT_ADDR"
bao server -config="$ROOT_VAULT_CONFIG" &
ROOT_VAULT_PID=$!
say "the root vault holds the cross-deployment credentials on $ROOT_VAULT_ADDR; it is started uninitialised, and init, unseal and engine configuration land in the next step through internal/openbao"

say "starting kcp"
kcp start \
	--root-directory="$KCP_ROOT" \
	--bind-address=127.0.0.1 \
	--secure-port=6443 \
	--feature-gates=WorkspaceMounts=true \
	--audit-policy-file=/opt/socialweb/deploy/kcp-audit-policy.yaml \
	--audit-log-path=/var/lib/socialweb/kcp/audit.log \
	--audit-log-maxsize=100 --audit-log-maxage=0 --audit-log-maxbackup=0 &
KCP_PID=$!

say "waiting for kcp /readyz"
tries=0
until K get --raw=/readyz >/dev/null 2>&1; do
	tries=$((tries + 1))
	if [ "$tries" -ge 180 ]; then
		say "kcp never became ready"
		exit 1
	fi
	sleep 1
done
say "kcp is ready"

tls_certificate
export SOCIALWEB_CONTAINER_TLS_CERT="$TLS_CERT"
export SOCIALWEB_CONTAINER_TLS_KEY="$TLS_KEY"

say "applying the root vault layout to $ROOT_VAULT_ADDR"
if ! SOCIALWEB_STATE_DIR="$STATE_DIR" /usr/local/bin/socialweb-vault apply-root; then
	say "the root vault could not be made to hold the deployment's root credentials; a deployment whose vault is empty is not the deployment, so the gateway must not start"
	exit 1
fi
say "the root vault holds the root layout"

say "applying the provider manifests"
KUBECTL="$KUBECTL_BIN" \
	KUBECONFIG_PATH="$SOCIALWEB_KCP_KUBECONFIG" \
	PROVIDER_WORKSPACE="${PROVIDER_WORKSPACE:-socialweb-provider}" \
	GRANT_CONTEXT="${GRANT_CONTEXT:-system:admin}" \
	AUDIT_LOG="$AUDIT_LOG" \
	"$DEPLOY_DIR/install-provider.sh"

if [ "$BOOTSTRAP" = "1" ]; then
	say "creating the default cluster ${SOCIALWEB_CLUSTERPROVIDER_JOIN}"
	if (set -e; bootstrap_cluster); then
		say "the default cluster exists"
	else
		say "the default cluster could not be created; running observe-only"
		unset SOCIALWEB_CLUSTERPROVIDER_JOIN
	fi
fi

say "starting socialweb-gateway"
/usr/local/bin/socialweb-gateway &
GATEWAY_PID=$!

say "starting the TLS terminator"
say "the TLS certificate is $TLS_CERT with SHA-256 fingerprint $(openssl x509 -in "$TLS_CERT" -noout -fingerprint -sha256 | cut -d= -f2)"
# ponytail: socat forks one process per connection; swap for a real proxy if connection volume matters
socat OPENSSL-LISTEN:"$TLS_PORT",fork,reuseaddr,cert="$TLS_CERT",key="$TLS_KEY",verify=0 TCP:"$SOCIALWEB_GATEWAY_ADDR" &
TLS_PID=$!

say "starting socialweb-clusterprovider"
/usr/local/bin/socialweb-clusterprovider &
PROVIDER_PID=$!

stop_children() {
	kill -TERM "$KCP_PID" "$GATEWAY_PID" "$TLS_PID" "$PROVIDER_PID" "$ROOT_VAULT_PID" 2>/dev/null || true
}

trap 'stop_children; exit 0' TERM INT

say "supervising rootvault=$ROOT_VAULT_PID kcp=$KCP_PID gateway=$GATEWAY_PID tls=$TLS_PID clusterprovider=$PROVIDER_PID"
while :; do
	for child in "rootvault $ROOT_VAULT_PID" "kcp $KCP_PID" "gateway $GATEWAY_PID" "tls $TLS_PID" "clusterprovider $PROVIDER_PID"; do
		name="${child%% *}"
		pid="${child##* }"
		if ! kill -0 "$pid" 2>/dev/null; then
			say "$name exited"
			stop_children
			exit 1
		fi
	done
	sleep 2
done
