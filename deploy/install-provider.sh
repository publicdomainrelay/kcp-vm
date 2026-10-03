#!/bin/bash
set -eux -o pipefail

KUBECTL=${KUBECTL:-kubectl}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-/var/.kcp/admin.kubeconfig}
PROVIDER_WORKSPACE=${PROVIDER_WORKSPACE:-socialweb-provider}
GRANT_CONTEXT=${GRANT_CONTEXT:-system:admin}
WAIT_SECONDS=${WAIT_SECONDS:-120}
DEPLOY_DIR=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" = 0 ] || [ -r "$KUBECONFIG_PATH" ]; then
  SUDO=""
else
  SUDO="sudo"
fi

K() {
  $SUDO $KUBECTL --kubeconfig="$KUBECONFIG_PATH" "$@"
}

SERVER=$(K config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SERVER=${SERVER%%/clusters/*}
PROVIDER_PATH=root:${PROVIDER_WORKSPACE}
PROVIDER_SERVER=${SERVER}/clusters/${PROVIDER_PATH}

KW() {
  $SUDO $KUBECTL --kubeconfig="$KUBECONFIG_PATH" --server="$PROVIDER_SERVER" "$@"
}

K --context="$GRANT_CONTEXT" version >/dev/null

if ! K get workspace "$PROVIDER_WORKSPACE" >/dev/null 2>&1; then
  K apply -f - <<YAML
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: ${PROVIDER_WORKSPACE}
spec:
  type:
    name: universal
    path: root
YAML
fi

tries=0
until [ "$(K get workspace "$PROVIDER_WORKSPACE" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Ready" ]; do
  tries=$((tries + 1))
  if [ "$tries" -ge "$WAIT_SECONDS" ]; then
    echo >&2 "workspace ${PROVIDER_PATH} did not reach Ready within ${WAIT_SECONDS}s"
    exit 1
  fi
  sleep 1
done

KW apply -f "$DEPLOY_DIR/cluster-apiresourceschema.yaml"
KW apply -f "$DEPLOY_DIR/node-apiresourceschema.yaml"
KW apply -f "$DEPLOY_DIR/atprotoidentity-apiresourceschema.yaml"
KW apply -f "$DEPLOY_DIR/cluster-apiexport.yaml"
KW apply -f "$DEPLOY_DIR/atprotoidentity-apiexport.yaml"

K apply -f "$DEPLOY_DIR/workspacetype-account.yaml"
K apply -f "$DEPLOY_DIR/workspacetype-cluster.yaml"

for export in clusters atprotoidentities; do
  tries=0
  until [ "$(KW get apiexport "$export" -o jsonpath='{.status.conditions[?(@.type=="IdentityValid")].status}' 2>/dev/null)" = "True" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$WAIT_SECONDS" ]; then
      echo >&2 "apiexport ${export} in ${PROVIDER_PATH} never reported IdentityValid"
      exit 1
    fi
    sleep 1
  done
done

PROVIDER_CLUSTER=$(K get workspace "$PROVIDER_WORKSPACE" -o jsonpath='{.spec.cluster}')
echo "provider workspace : ${PROVIDER_PATH}"
echo "provider cluster   : ${PROVIDER_CLUSTER}"
echo "provider server    : ${PROVIDER_SERVER}"

KW get apiresourceschemas -o custom-columns='NAME:.metadata.name'
KW get apiexports -o custom-columns='NAME:.metadata.name,IDENTITYHASH:.status.identityHash'

KUBECTL="$KUBECTL" \
  KUBECONFIG_PATH="$KUBECONFIG_PATH" \
  KCP_SERVER="$SERVER" \
  PROVIDER_CLUSTER="$PROVIDER_CLUSTER" \
  PROVIDER_WORKSPACE="$PROVIDER_PATH" \
  AUDIT_LOG="${AUDIT_LOG:-/var/lib/socialweb/kcp/audit.log}" \
  "$DEPLOY_DIR/apply-permission-inventory.sh"
