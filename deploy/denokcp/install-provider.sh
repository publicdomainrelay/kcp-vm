#!/usr/bin/env bash
set -euo pipefail

KUBECTL=${KUBECTL:-kubectl}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-$PWD/.kcp/admin.kubeconfig}
PROVIDER_WORKSPACE=${PROVIDER_WORKSPACE:-deno-provider}
WAIT_SECONDS=${WAIT_SECONDS:-120}
DEPLOY_DIR=$(cd "$(dirname "$0")" && pwd)

K() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" "$@"; }

KA() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" apply --validate=false "$@"; }

SERVER=$(K config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SERVER=${SERVER%%/clusters/*}
PROVIDER_PATH=root:${PROVIDER_WORKSPACE}
PROVIDER_SERVER=${SERVER}/clusters/${PROVIDER_PATH}

KW() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --server="$PROVIDER_SERVER" "$@"; }

KWA() { "$KUBECTL" --kubeconfig="$KUBECONFIG_PATH" --server="$PROVIDER_SERVER" apply --validate=false "$@"; }

tries=0
until K get workspaces >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ "$tries" -ge "$WAIT_SECONDS" ]; then
    echo "kcp did not accept admin requests within ${WAIT_SECONDS}s" >&2
    exit 1
  fi
  sleep 1
done

if ! K get workspace "$PROVIDER_WORKSPACE" >/dev/null 2>&1; then
  KA -f - <<YAML
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
    echo "workspace ${PROVIDER_PATH} did not reach Ready within ${WAIT_SECONDS}s" >&2
    exit 1
  fi
  sleep 1
done

KWA -f "$DEPLOY_DIR/policyworkflowrun-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/policyworkflowrun-apiexport.yaml"
KWA -f "$DEPLOY_DIR/denopod-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/denorun-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/denojob-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/runtrigger-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/policyengine-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/policyworkflowpod-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/openbao-apiresourceschema.yaml"
KWA -f "$DEPLOY_DIR/denoruntime-apiexport.yaml"
KA -f "$DEPLOY_DIR/workspacetype-workflow.yaml"
KA -f "$DEPLOY_DIR/workspacetype-denoruntime.yaml"

for export in policyworkflowruns denoruntime; do
  tries=0
  until [ "$(KW get apiexport "$export" -o jsonpath='{.status.conditions[?(@.type=="IdentityValid")].status}' 2>/dev/null)" = "True" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$WAIT_SECONDS" ]; then
      echo "apiexport $export never reported IdentityValid" >&2
      exit 1
    fi
    sleep 1
  done
done

echo "provider workspace: ${PROVIDER_PATH}"
echo "provider server:    ${PROVIDER_SERVER}"
KW get apiresourceschemas -o custom-columns='NAME:.metadata.name'
KW get apiexports -o custom-columns='NAME:.metadata.name,IDENTITYHASH:.status.identityHash'
