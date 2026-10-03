#!/usr/bin/env bash
set -uo pipefail

KUBECTL=${KUBECTL:-kubectl}
AS_NAMESPACE=${AS_NAMESPACE:-kcp-system}
AS_USER=${AS_USER:-socialweb-materialiser}
AS_CLUSTER_PROVIDER=${AS_CLUSTER_PROVIDER:-socialweb-cluster-provider}
AS_FLEET=${AS_FLEET:-socialweb-fleet}
AS_LIFECYCLE=${AS_LIFECYCLE:-socialweb-lifecycle}
TENANT_KUBECTL=${TENANT_KUBECTL:-kubectl}
TENANT_AS=${TENANT_AS:-system:serviceaccount:users:gwmint}
TENANT_SA=${TENANT_SA:-gwmint}
GRANT_CONTEXT=${GRANT_CONTEXT:-system:admin}
PROVIDER_WORKSPACE=${PROVIDER_WORKSPACE:-root:socialweb-provider}
PROVIDER_NAME=${PROVIDER_WORKSPACE##*:}
fail=0

if [[ -z "${PROVIDER_CLUSTER:-}" ]]; then
  PROVIDER_CLUSTER=$($KUBECTL get workspace "$PROVIDER_NAME" \
    -o jsonpath='{.spec.cluster}' 2>/dev/null || true)
fi

if [[ -z "${KCP_SERVER:-}" ]]; then
  KCP_SERVER=$($KUBECTL config view --minify -o jsonpath='{.clusters[0].cluster.server}' \
    2>/dev/null || true)
  KCP_SERVER=${KCP_SERVER%%/clusters/*}
fi
PROVIDER_SERVER="$KCP_SERVER/clusters/$PROVIDER_WORKSPACE"

subject() {
  case "$1" in
    *:*) printf '%s' "$1" ;;
    *)   printf 'system:kcp:serviceaccount:%s:%s:%s' "$PROVIDER_CLUSTER" "$AS_NAMESPACE" "$1" ;;
  esac
}


verdict() {
  local verb="$1" group="$2" res="$3" who="$4" sub=""
  case "$res" in */*) sub="--subresource=${res#*/}"; res="${res%%/*}" ;; esac
  # shellcheck disable=SC2086
  $KUBECTL auth can-i "$verb" "$res${group:+.$group}" $sub --as="$(subject "$who")" \
    --server="$PROVIDER_SERVER" 2>/dev/null | tail -1 | cut -d' ' -f1
}

observe() {
  local d
  d=$(verdict "$@")
  if [[ -z "$d" ]]; then
    printf '  UNPROBEABLE  %s %s %s as %s\n' "$1" "$2" "$3" "$(subject "$4")"
  elif [[ "$d" == "yes" ]]; then
    printf '  GRANTED      %s %s %s as %s\n' "$1" "$2" "$3" "$(subject "$4")"
  else
    printf '  NOT-GRANTED  %s %s %s as %s\n' "$1" "$2" "$3" "$(subject "$4")"
  fi
}

expect_deny() {
  local d
  d=$(verdict "$@")
  if [[ -z "$d" ]]; then
    echo "PROBE ERROR: $1 $2 $3 as $(subject "$4") -> kubectl produced no verdict"
    fail=1
  elif [[ "$d" != "no" ]]; then
    echo "OVER-GRANTED: $1 $2 $3 as $(subject "$4") -> $d"
    fail=1
  fi
}


echo "==========================================================================="
echo "SECTIONS A.1-A.4 ARE --as=-ONLY, OBSERVATIONS AND NOT THE GATE. Impersonation"
echo "sets the USERNAME and not the authentication.kcp.io/scopes extra, so it"
echo "cannot see the confinement that holds a workspace ServiceAccount token"
echo "inside its origin workspace. They probe the PROVIDER WORKSPACE, the only"
echo "logical cluster these identities can act in, so every line naming a kind it"
echo "does not serve (clusters, nodes, vaults, postgreses, kines, shards) reports"
echo "NOT-GRANTED for a reason that is not a missing grant. A green A would still"
echo "be consistent with a binding no token can ever use. The gate is B, D and E;"
echo "the real instrument is deploy/apply-permission-inventory.sh, which mints"
echo "tokens and attempts the operations the Go code performs."
echo "Section C is weaker than its title too -- see its comment."
echo "==========================================================================="

echo "== A.1 the MATERIALISER must be able to do exactly this, and no more =="
observe get    socialweb.computer clusters                 "$AS_USER"
observe list   socialweb.computer clusters                 "$AS_USER"
observe watch  socialweb.computer clusters                 "$AS_USER"
observe create socialweb.computer vaults                   "$AS_USER"
observe create socialweb.computer postgreses               "$AS_USER"
observe create socialweb.computer kines                    "$AS_USER"
observe update socialweb.computer clusters/status          "$AS_USER"
observe get    tenancy.kcp.io     workspaces               "$AS_USER"
observe get    core.kcp.io        logicalclusters          "$AS_USER"
observe get    core.kcp.io        shards                   "$AS_USER"
observe get    ""                 secrets                  "$AS_USER"
observe create coordination.k8s.io leases                  "$AS_USER"

echo "== A.2 the CLUSTER PROVIDER must be able to do exactly this =="
observe create socialweb.computer clusters                 "$AS_CLUSTER_PROVIDER"
observe update socialweb.computer clusters/status          "$AS_CLUSTER_PROVIDER"
observe get    ""                 serviceaccounts          "$AS_CLUSTER_PROVIDER"

echo "== A.3 the NODE / FLEET identity must be able to do exactly this =="
observe create socialweb.computer nodes                    "$AS_FLEET"
observe update socialweb.computer nodes/status             "$AS_FLEET"
observe get    apis.kcp.io        apiexports               "$AS_FLEET"
observe get    apis.kcp.io        apiresourceschemas       "$AS_FLEET"
observe get    apis.kcp.io        apibindings              "$AS_FLEET"
observe get    apis.kcp.io        apiexportendpointslices  "$AS_FLEET"

echo "== A.4 the LIFECYCLE (XRPC) identity must be able to do exactly this =="
observe create tenancy.kcp.io     workspaces               "$AS_LIFECYCLE"
observe delete tenancy.kcp.io     workspaces               "$AS_LIFECYCLE"
observe get    tenancy.kcp.io     workspacetypes           "$AS_LIFECYCLE"
observe create tenancy.kcp.io     workspacetypes           "$AS_LIFECYCLE"
observe update core.kcp.io        logicalclusters          "$AS_LIFECYCLE"
observe create apis.kcp.io        apibindings              "$AS_LIFECYCLE"
observe create ""                 resourcequotas           "$AS_LIFECYCLE"


echo "== B. NO subject may widen itself =="
for who in "$AS_USER" "$AS_CLUSTER_PROVIDER" "$AS_FLEET" "$AS_LIFECYCLE"; do
  expect_deny create rbac.authorization.k8s.io clusterroles                  "$who"
  expect_deny create rbac.authorization.k8s.io clusterrolebindings           "$who"
  expect_deny create rbac.authorization.k8s.io rolebindings                  "$who"
  expect_deny create ""                         pods/exec                   "$who"
  expect_deny get    ""                         nodes/proxy                 "$who"
  expect_deny update core.kcp.io                shards                      "$who"
  expect_deny create certificates.k8s.io        certificatesigningrequests  "$who"
done

echo "== C. the bypass must be gone =="
echo "  (not probeable by impersonation; see the comment at C. checked by D)"

echo "== D. shard-admin must appear in ZERO audit records THE RUNTIME MADE =="
echo "  (an install is an administrative act by definition and is audited as"
echo "   shard-admin. The boundary is the byte offset deploy/apply-permission-inventory.sh"
echo "   records when its administrative act ENDS, so the window this reads is"
echo "   everything the RUNTIME did after it -- not the install that set it up.)"
LOG=${AUDIT_LOG:-/var/lib/socialweb/kcp/audit.log}
BOUNDARY=${BOUNDARY:-${LOG}.boundary}
if [[ -r "$LOG" ]]; then
  FROM=1
  if [[ -r "$BOUNDARY" ]]; then
    b=$(head -1 "$BOUNDARY" | tr -dc '0-9')
    size=$(stat -c%s "$LOG" 2>/dev/null || echo 0)
    if [[ -n "$b" && "$b" -ge 0 && "$b" -le "$size" ]]; then
      FROM=$((b + 1))
      echo "  window: bytes ${FROM}.. of $LOG (boundary ${b})"
    else
      echo "  the recorded boundary (${b:-none}) is not within a ${size}-byte log --"
      echo "  reading the whole log, which counts the install's own records too."
    fi
  else
    echo "  window: the whole log (no boundary recorded at $BOUNDARY)"
  fi
  n=$(tail -c +"$FROM" "$LOG" | grep -c '"username":"shard-admin"' || true)
  if [[ "$n" != "0" ]]; then
    echo "SHARD-ADMIN IS STILL BEING USED: $n audit records"
    tail -c +"$FROM" "$LOG" | grep -m3 '"username":"shard-admin"'
    fail=1
  fi
  n=$(tail -c +"$FROM" "$LOG" | grep -c '"username":"system:anonymous".*"decision":"allow"' || true)
  [[ "$n" != "0" ]] && { echo "ANONYMOUS ALLOWED: $n records"; fail=1; }
else
  echo "SKIPPING audit assertion: $LOG unreadable."
  echo "  Arming this is the loud half of the check -- add"
  echo "  --audit-policy-file + --audit-log-path and re-run."
  fail=1
fi

echo "== E. the TENANT mint grant -- a DIFFERENT credential, in a DIFFERENT cluster =="
if [[ -z "${TENANT_CONTEXT:-}" ]]; then
  echo "  SKIPPED: TENANT_CONTEXT unset, so this run cannot reach a tenant cluster."
  echo "  The tenant mint lives in deploy/tenant-grant.yaml: subject"
  echo "  system:serviceaccount:users:$TENANT_SA in ns=users, applied PER CLUSTER by the"
  echo "  cluster provider. It is not reachable from kcp's kubeconfig and a MISSING GRANT"
  echo "  in section A says nothing about it."
else
  out=$($TENANT_KUBECTL --context="$TENANT_CONTEXT" --as="$TENANT_AS" \
        create token "$TENANT_SA" -n users 2>&1)
  if [[ $? -ne 0 ]]; then
    echo "TENANT MINT DENIED: $out"
    echo "  (expected 201. A 403 names the subject; a 404 means the SA is absent.)"
    fail=1
  else
    echo "  tenant mint OK: ${#out} chars"
  fi
fi

echo
if [[ $fail == 0 ]]; then echo "PASS"; else echo "FAIL"; fi
exit $fail
