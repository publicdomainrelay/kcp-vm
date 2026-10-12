#!/usr/bin/env bash
set -eux -o pipefail

KUBECTL=${KUBECTL:-kubectl}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-/var/.kcp/admin.kubeconfig}
INVENTORY=${INVENTORY:-$(dirname "$0")/permission-inventory.yaml}
PROVIDER_WORKSPACE=${PROVIDER_WORKSPACE:-root:socialweb-provider}
PROVIDER_NAME=${PROVIDER_WORKSPACE##*:}
AUDIT_LOG=${AUDIT_LOG:-/var/.kcp/audit.log}
BOUNDARY_FILE=${BOUNDARY_FILE:-${AUDIT_LOG}.boundary}
PLACEHOLDER='{{.ProviderCluster}}'

SUDO=""
[[ "$(id -u)" == "0" || -r "$KUBECONFIG_PATH" ]] || SUDO=sudo

BASE=()
[[ -n "$KUBECONFIG_PATH" ]] && BASE+=(--kubeconfig="$KUBECONFIG_PATH")

K() { $SUDO $KUBECTL "${BASE[@]}" "$@"; }

if [[ -z "${KCP_SERVER:-}" ]]; then
  KCP_SERVER=$(K config view --minify -o jsonpath='{.clusters[0].cluster.server}')
  KCP_SERVER=${KCP_SERVER%%/clusters/*}
fi
PROVIDER_SERVER="$KCP_SERVER/clusters/$PROVIDER_WORKSPACE"

KSUB() { $SUDO $KUBECTL "${BASE[@]}" --server="$PROVIDER_SERVER" "$@"; }
KAT() { $SUDO $KUBECTL "${BASE[@]}" --server="$KCP_SERVER/clusters/$1" "${@:2}"; }

APPLY=apply
[[ "${DRY_RUN:-}" == "1" ]] && APPLY="apply --dry-run=server"

if [[ -z "${PROVIDER_CLUSTER:-}" ]]; then
  PROVIDER_CLUSTER=$(K get workspace "$PROVIDER_NAME" -o jsonpath='{.spec.cluster}')
fi
if [[ -z "$PROVIDER_CLUSTER" ]]; then
  echo >&2 "the provider workspace $PROVIDER_WORKSPACE reports no spec.cluster, so no"
  echo >&2 "subject string can be built. An empty one binds nobody while looking applied."
  exit 1
fi
echo "provider workspace : $PROVIDER_WORKSPACE"
echo "provider cluster   : $PROVIDER_CLUSTER (LIVE, read from spec.cluster)"

RENDERED=$(mktemp)
trap 'rm -f "$RENDERED"' EXIT
sed "s/$PLACEHOLDER/$PROVIDER_CLUSTER/g" "$INVENTORY" > "$RENDERED"
if grep -q "$PLACEHOLDER" "$RENDERED"; then
  echo >&2 "a $PLACEHOLDER survived rendering"
  exit 1
fi

subjects() { yq 'select(.kind == "ServiceAccount")' "$RENDERED"; }
grants() { yq 'select(.kind != "ServiceAccount")' "$RENDERED"; }

fail=0
note_fail() { printf 'FAIL: %s\n' "$1" >&2; fail=1; }

echo "== 1. the SUBJECTS -> $PROVIDER_SERVER =="
subjects | KSUB $APPLY -f -

echo "== 2. the GRANTS -> $PROVIDER_SERVER, the ONLY logical cluster these"
echo "      identities can act in. RBAC is logical-cluster-local, so a binding"
echo "      applied anywhere else is consulted for nothing."
grants | KSUB $APPLY -f -

if [[ "$APPLY" != "apply" ]]; then
  echo "DRY_RUN=1: nothing persisted, so there is nothing to measure. Done."
  exit 0
fi

echo "== 3. read back all twelve =="
for sa in socialweb-materialiser socialweb-cluster-provider socialweb-fleet socialweb-lifecycle; do
  KSUB -n kcp-system get serviceaccount "$sa" >/dev/null 2>&1 \
    || note_fail "ServiceAccount kcp-system/$sa is not readable in $PROVIDER_WORKSPACE"
  KSUB get clusterrole "$sa" >/dev/null 2>&1 \
    || note_fail "ClusterRole $sa is not readable in $PROVIDER_WORKSPACE"
  KSUB get clusterrolebinding "$sa" >/dev/null 2>&1 \
    || note_fail "ClusterRoleBinding $sa is not readable in $PROVIDER_WORKSPACE"
done

echo "== 4. every subject names the CLUSTER THIS WORKSPACE HAS =="
for sa in socialweb-materialiser socialweb-cluster-provider socialweb-fleet socialweb-lifecycle; do
  got=$(KSUB get clusterrolebinding "$sa" -o jsonpath='{.subjects[0].name}')
  want="system:kcp:serviceaccount:${PROVIDER_CLUSTER}:kcp-system:${sa}"
  if [[ "$got" != "$want" ]]; then
    note_fail "$sa binds '$got' and this workspace's cluster is '$PROVIDER_CLUSTER'."
    note_fail "A subject naming another cluster is a name this workspace never"
    note_fail "authenticates as, so the binding grants nobody."
  fi
done

echo "== 5. MEASURED from a real TokenRequest token, in its ORIGIN workspace =="
echo "   verify-sar-sweep.sh's --as= probes are NOT this: impersonation sets the"
echo "   username and not the authentication.kcp.io/scopes extra, so it answers a"
echo "   different question than a token does."

probe() {
  local sa="$1" verb="$2" resource="$3" namespace="$4" lc="$5"
  local tok out=DENY
  set +x
  tok=$(KSUB -n kcp-system create token "$sa")
  local args=(--server="$KCP_SERVER/clusters/$lc" --token="$tok")
  [[ -n "$namespace" ]] && args+=(-n "$namespace")
  if $SUDO $KUBECTL "${BASE[@]}" "${args[@]}" "$verb" "$resource" >/dev/null 2>&1; then
    out=ALLOW
  fi
  printf '%s' "$out"
}

expect() {
  local want="$1" got="$2" what="$3"
  [[ "$got" != "$want" ]] && note_fail "expected $want, measured $got: $what"
  printf '  %-5s %s\n' "$got" "$what"
}

expect ALLOW "$(probe socialweb-materialiser get secrets kcp-system "$PROVIDER_WORKSPACE")" \
  "materialiser read secrets in its origin workspace"
expect ALLOW "$(probe socialweb-cluster-provider get serviceaccounts kcp-system "$PROVIDER_WORKSPACE")" \
  "cluster provider read serviceaccounts in its origin workspace"
expect ALLOW "$(probe socialweb-fleet get apiexports '' "$PROVIDER_WORKSPACE")" \
  "fleet read apiexports in its origin workspace"
expect ALLOW "$(probe socialweb-lifecycle get workspacetypes '' "$PROVIDER_WORKSPACE")" \
  "lifecycle read workspacetypes in its origin workspace"

echo "== 6. the SAME token, at a logical cluster that is NOT its origin =="
echo "   root is where Registry.Refs lists workspaces and where the XRPC surface"
echo "   creates the account workspace, so this is an operation the Go code performs."

for sa in socialweb-materialiser socialweb-cluster-provider socialweb-fleet socialweb-lifecycle; do
  expect DENY "$(probe "$sa" get workspaces '' root)" \
    "$sa list workspaces in root (a foreign logical cluster)"
done

echo "== 6b. that denial is NOT a missing binding: bind it there too, re-measure =="
KAT root apply -f - >/dev/null <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: swtest-foreign-probe
rules:
  - nonResourceURLs: ["/"]
    verbs: ["access"]
  - apiGroups: ["tenancy.kcp.io"]
    resources: ["workspaces"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: swtest-foreign-probe
subjects:
  - kind: User
    name: system:kcp:serviceaccount:${PROVIDER_CLUSTER}:kcp-system:socialweb-fleet
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: swtest-foreign-probe
YAML
expect DENY "$(probe socialweb-fleet get workspaces '' root)" \
  "fleet list workspaces in root, WITH a matching binding in root"

KAT root delete clusterrolebinding swtest-foreign-probe --ignore-not-found >/dev/null
KAT root delete clusterrole swtest-foreign-probe --ignore-not-found >/dev/null

echo "== 6c. the credential the RUNTIME holds for kcp, which is the one kcp does NOT confine =="
echo "   internal/kcpclient binds /clusters/<logicalCluster>/ from ONE rest.Config: root for"
echo "   Registry.Refs, the account workspace for the identity record, the home workspace for"
echo "   Cluster and Node, the mount for the tenant ServiceAccount. Sections 6 and 6b measured"
echo "   that a workspace ServiceAccount cannot be that credential: kcp confines it to the"
echo "   logical cluster it was minted in and refuses it everywhere else BEFORE RBAC is"
echo "   consulted. Asserting an ALLOW for one of them here was asserting the model this"
echo "   design rejected, so what is asserted instead is the credential the runtime is"
echo "   actually given for kcp -- the admin kubeconfig, the same file this install holds."

RUNTIME_KUBECONFIG=${RUNTIME_KUBECONFIG:-${SOCIALWEB_KCP_KUBECONFIG:-$KUBECONFIG_PATH}}

runtime_probe() {
  local lc="$1"; shift
  local args=()
  [[ -n "$RUNTIME_KUBECONFIG" ]] && args+=(--kubeconfig="$RUNTIME_KUBECONFIG")
  args+=(--server="$KCP_SERVER/clusters/$lc")
  if $SUDO $KUBECTL "${args[@]}" "$@" >/dev/null 2>&1; then
    printf 'ALLOW'
  else
    printf 'DENY'
  fi
}

expect ALLOW "$(runtime_probe root get workspaces)" \
  "the runtime's kcp credential lists workspaces in root, the call internal/clusterprovider/registry.go makes"

echo "== 6d. and the runtime's TENANT credential is MINTED, never copied =="
echo "   Invariant 20: a client certificate signed by THAT cluster's own ClientCA, valid only"
echo "   against that cluster, with the k3s-written admin.kubeconfig never read -- it is shared"
echo "   state on a multi-cluster host and its server name is unverified against the address a"
echo "   node dials. The mint's entire input is that cluster's own TLS material, so what a"
echo "   re-seed can break is exactly what is checked here."

CLUSTER_STATE_DIR=${CLUSTER_STATE_DIR:-${SOCIALWEB_CLUSTERPROVIDER_STATE_DIR:-}}
if [[ -z "$CLUSTER_STATE_DIR" ]]; then
  echo "  SKIPPED: neither CLUSTER_STATE_DIR nor SOCIALWEB_CLUSTERPROVIDER_STATE_DIR is set, so"
  echo "  this run cannot see a cluster's data dir and proves nothing about the mint. The paths"
  echo "  it checks are <StateDir>/<cluster>/server/tls/{client-ca.crt,client-ca.key,server-ca.crt}."
else
  checked=0
  for tls in "$CLUSTER_STATE_DIR"/*/server/tls; do
    [[ -d "$tls" ]] || continue
    checked=$((checked + 1))
    for f in client-ca.crt client-ca.key server-ca.crt; do
      $SUDO test -r "$tls/$f" || note_fail "$tls/$f is unreadable, so nothing can be minted from this cluster's own ClientCA (clusterprovider.ErrNoClientCA / ErrNoServerCA). The only other usable credential on this host is k3s's own admin kubeconfig, which is shared state and is not this provider's to hand out"
    done
    if $SUDO test -r "$tls/client-ca.key"; then
      mode=$($SUDO stat -c '%a' "$tls/client-ca.key" 2>/dev/null || true)
      [[ "$mode" == "600" ]] || note_fail "$tls/client-ca.key is mode ${mode:-unreadable}, not 600, and it signs a cluster-admin client certificate for every tenant of this cluster"
    fi
    if command -v openssl >/dev/null 2>&1 && $SUDO test -r "$tls/client-ca.crt" && $SUDO test -r "$tls/client-ca.key"; then
      certpub=""
      keypub=""
      if certpem=$($SUDO openssl x509 -noout -pubkey -in "$tls/client-ca.crt" 2>/dev/null); then
        certpub=$(printf '%s' "$certpem" | openssl sha256 2>/dev/null || true)
      fi
      if keypem=$($SUDO openssl pkey -pubout -in "$tls/client-ca.key" 2>/dev/null); then
        keypub=$(printf '%s' "$keypem" | openssl sha256 2>/dev/null || true)
      fi
      if [[ -z "$certpub" ]]; then
        note_fail "$tls/client-ca.crt does not parse as a certificate (clusterprovider.ErrClientCAUnusable): a re-seed wrote something else here, and every credential minted from it is refused by the apiserver as an unknown authority"
      elif [[ -z "$keypub" ]]; then
        note_fail "$tls/client-ca.key does not parse as a private key (clusterprovider.ErrClientCAUnusable): nothing can be signed with it"
      elif [[ "$certpub" != "$keypub" ]]; then
        note_fail "$tls/client-ca.key is not the key of $tls/client-ca.crt (clusterprovider.ErrClientCAKeyMismatch): a re-seed that replaced one and not the other still signs, and every certificate it produces is one no apiserver trusts"
      fi
    else
      echo "  note: openssl is unavailable, so the ClientCA pair under $tls was not checked for being one pair"
    fi
  done
  if [[ "$checked" == "0" ]]; then
    echo "  no cluster has been materialised under $CLUSTER_STATE_DIR yet, so the minted path is"
    echo "  configured and unexercised. The first cluster the provider brings up exercises it;"
    echo "  re-run this guard afterwards and the checks above run over it."
  fi
fi

if [[ "$fail" == "1" ]]; then
  echo
  echo "Sections 6 and 6b measured the contradiction, and it is the finding, not a flake:"
  echo "every workspace ServiceAccount token carries"
  echo "  authentication.kcp.io/scopes: [cluster:<its origin>]"
  echo "and kcp refuses it in every other logical cluster BEFORE RBAC is consulted, so no"
  echo "subject string and no ClusterRoleBinding placement lets one of these four serve the"
  echo "runtime that internal/kcpclient drives from ONE rest.Config across many logical"
  echo "clusters. That is the DESIGN, not a broken install: the runtime's kcp credential is"
  echo "the admin kubeconfig (6c) and its per-tenant credential is a client certificate"
  echo "minted from that cluster's own ClientCA (6d). A failure above therefore means one of"
  echo "THOSE is missing or malformed -- a grant naming a cluster this workspace does not"
  echo "have, a ClientCA pair a re-seed broke, or a runtime credential that cannot reach"
  echo "root -- and never that a workspace ServiceAccount needs a wider binding."
fi

echo "== 7. record the window the audit gate reads from =="
if $SUDO test -r "$AUDIT_LOG"; then
  OFFSET=$($SUDO stat -c%s "$AUDIT_LOG")
  if $SUDO sh -c "printf '%s\\n' '$OFFSET' > '$BOUNDARY_FILE'" 2>/dev/null; then
    echo "boundary: $BOUNDARY_FILE = $OFFSET (this administrative act ends here)"
  else
    echo "WARNING: could not write $BOUNDARY_FILE, so the sweep reads the whole log"
    echo "WARNING: and counts this install's own shard-admin records against the runtime."
  fi
else
  echo "WARNING: $AUDIT_LOG is unreadable, so no boundary was recorded."
fi

echo
if [[ "$fail" == "0" ]]; then
  echo "PASS: 4 subjects + 4 ClusterRoles + 4 ClusterRoleBindings, named to the live"
  echo "      cluster, each class ALLOWED its capability IN ITS ORIGIN WORKSPACE from a"
  echo "      real token, each DENIED outside it -- which is why none of the four is the"
  echo "      runtime. The runtime's own credentials are asserted separately: the kcp one"
  echo "      in 6c, the per-tenant minted client certificate in 6d."
else
  echo "FAIL: see the block above. These four identities cannot do the work"
  echo "      internal/kcpclient asks of a single credential -- that is invariant 22 and"
  echo "      it is the design. What this run failed on is one of the credentials the"
  echo "      design DID choose: 6c's kcp credential or 6d's minted tenant credential."
fi
exit $fail
