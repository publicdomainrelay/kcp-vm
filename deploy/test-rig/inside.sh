#!/bin/bash
set -euo pipefail

export PATH=/go/toolchain/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export GOPATH=/go GOMODCACHE=/go/pkg/mod GOCACHE=/go/cache GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=mod

STORE="${SOCIALWEB_RIG_IMAGE_STORE:-/var/lib/socialweb/images}"
FC_CONFIG="${SOCIALWEB_RIG_FC_CONFIG:-/home/johnandersen777/kcp-image/nodeimage.json}"
QEMU_CONFIG="${SOCIALWEB_RIG_QEMU_CONFIG:-/home/johnandersen777/kcp-image/nodeimage-qemu.json}"
KCP_KUBECONFIG="${SOCIALWEB_KCP_KUBECONFIG:-/var/lib/socialweb/kcp/admin.kubeconfig}"
KCP_HOST="${SOCIALWEB_KCP_HOST:-https://127.0.0.1:6443}"
MINT_DIR="${SOCIALWEB_GATEWAY_MINT_KUBECONFIGS:-/var/lib/socialweb/mint}"
GRANT="${SOCIALWEB_CLUSTERPROVIDER_GRANT:-/opt/socialweb/deploy/tenant-grant.yaml}"
CLUSTER="${SOCIALWEB_CLUSTERPROVIDER_JOIN:-${SOCIALWEB_CONTAINER_CLUSTER:-socialweb}}"
REGISTRY="${SOCIALWEB_GATEWAY_DEVKEY_REGISTRY:-root:socialweb-provider}"
REGISTRY_NS="${SOCIALWEB_GATEWAY_DEVKEY_NAMESPACE:-default}"
CP_STATE="${SOCIALWEB_CLUSTERPROVIDER_STATE_DIR:-/var/lib/socialweb/clusterprovider}"

eval "$(socialweb-rig-inputs \
	-store "$STORE" -config "$FC_CONFIG" -config-qemu "$QEMU_CONFIG" \
	-kcp-kubeconfig "$KCP_KUBECONFIG" -kcp-host "$KCP_HOST" -cluster "$CLUSTER" \
	-registry-workspace "$REGISTRY" -registry-namespace "$REGISTRY_NS" \
	-clusterprovider-state "$CP_STATE" -grant "$GRANT")"

export CLUSTERPROVIDER_LIVE_KUBECONFIG="$KCP_KUBECONFIG"
export CLUSTERPROVIDER_LIVE_HOST="$KCP_HOST"
export SOCIALWEB_LIVE_KUBECONFIG="$KCP_KUBECONFIG"
export SOCIALWEB_LIVE_APISERVER="$KCP_HOST"
export SOCIALWEB_LIVE_KCP_KUBECONFIG="$KCP_KUBECONFIG"
export SOCIALWEB_LIVE_MINT_KUBECONFIGS="$MINT_DIR"
: "${SOCIALWEB_LIVE_CLUSTER:=$CLUSTER}"
export SOCIALWEB_LIVE_CLUSTER
export CLUSTERPROVIDER_LIVE_TENANT_GRANT="$GRANT"
export PROOF_STATE_DIR="${PROOF_STATE_DIR:-/var/lib/socialweb/proof-state/state}"

operator="
SOCIALWEB_LIVE_TENANT_KUBECONFIG|internal/tenantclient TestTheRenewalPathAgainstRealInfrastructure (ADR 0020 Grant->Resolve->Issue): read from the clusterprovider's own state dir
SOCIALWEB_LIVE_DID|internal/tenantclient TestTheRenewalPathAgainstRealInfrastructure: read from the Cluster CR's spec.account
SOCIALWEB_LIVE_REGISTRY_WORKSPACE|internal/tenantclient TestTheRenewalPathAgainstRealInfrastructure: read from SOCIALWEB_GATEWAY_DEVKEY_REGISTRY
SOCIALWEB_LIVE_REGISTRY_NAMESPACE|internal/tenantclient TestTheRenewalPathAgainstRealInfrastructure: the gateway's own binding namespace
SOCIALWEB_LIVE_GRANTED_SA|cmd/socialweb-gateway TestTheMintCredentialAgainstRealInfrastructure: the did-* name the tenant grant's token rule scopes
CLUSTERPROVIDER_LIVE_TENANT_KUBECONFIG|internal/clusterprovider TestLiveBootstrapRunsAgainstARealTenantCluster: read from the clusterprovider's own state dir
SOCIALWEB_MOUNT_CLUSTER|internal/kcpclient TestTheMountChildAgainstALiveKCP: read from the Cluster CR's name
SOCIALWEB_MOUNT_DID|internal/kcpclient TestTheMountChildAgainstALiveKCP: read from the Cluster CR's spec.account
"

missing=0
while IFS='|' read -r name why; do
	[ -n "$name" ] || continue
	if [ -z "${!name:-}" ]; then
		printf 'MISSING %s\n  gates: %s\n  the rig derives it from the running product: the kcp admin kubeconfig, the Cluster CR and its account DID, the clusterprovider state dir, the tenant grant and the registry string. A value still unset here means the product did not publish it.\n' "$name" "$why"
		missing=$((missing + 1))
	fi
done <<< "$operator"

if [ "$missing" -ne 0 ] && [ "$#" -eq 0 ]; then
	printf 'socialweb rig: %d live input(s) the running product did not publish are unset, so the full gate cannot run. See the MISSING lines above and re-run.\n' "$missing" >&2
	exit 1
fi
if [ "$missing" -ne 0 ]; then
	printf 'socialweb rig: %d live input(s) unset; running the requested subset anyway.\n' "$missing" >&2
fi

targets=("$@")
if [ "${#targets[@]}" -eq 0 ]; then
	targets=("./...")
fi

log=/var/lib/socialweb/rig-gate.log
echo "== SOCIALWEB_REQUIRE_LIVE=1 go test ${targets[*]} -count=1 -v =="
set +e
SOCIALWEB_REQUIRE_LIVE=1 go test "${targets[@]}" -count=1 -v 2>&1 | tee "$log"
rc="${PIPESTATUS[0]}"
set -e

skips="$(grep -c -- '^--- SKIP' "$log" || true)"
notests="$(grep -c -F '[no tests to run]' "$log" || true)"
if [ "$skips" -ne 0 ]; then
	echo "FAIL: the gate printed $skips SKIP line(s); a skip is NOT a pass:" >&2
	grep -- '^--- SKIP' "$log" | sed 's/^/  /' >&2
	exit 1
fi
if [ "$notests" -ne 0 ]; then
	echo "FAIL: the gate printed [no tests to run], so nothing was proven:" >&2
	exit 1
fi
if [ "$rc" -ne 0 ]; then
	echo "FAIL: go test exited $rc; the failing tests are:" >&2
	grep -E -- '^--- FAIL' "$log" | sed 's/^/  /' >&2
	exit 1
fi
echo "PROOF: the live gate passed with no skips; its full output is $log"
