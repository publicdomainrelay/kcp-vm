#!/usr/bin/env bash
# Install the External Secrets Operator into a tenant cluster.
#
# ESO's own upstream install IS the source of truth for the controller, its CRDs, its webhook and
# its RBAC: it ships as one declarative `external-secrets.yaml` (the rendered Helm chart) beside
# the chart. Re-authoring 25 CRDs here would be a fork, so this repo pins the upstream version and
# applies that bundle unchanged. The OpenBao wiring is NOT in the bundle and is applied separately
# by internal/eso, which rewrites the placeholders in deploy/eso/*.yaml from the cluster's own
# vault address and mints the client certificate. Nothing here authors a secret value.
#
# Usage:
#   KUBECONFIG=/path/to/tenant.kubeconfig ESO_VERSION=v2.11.0 ./deploy/eso/install.sh
#
# ESO_BUNDLE=/path/to/external-secrets.yaml uses a local cached copy instead of downloading.
# ESO_SKIP_ROLLOUT_WAIT=1 applies the bundle and returns without awaiting the controller rollout.
# The CRDs and webhooks are established by the apply alone, and a caller that decides operator
# presence by reading a ClusterSecretStore needs nothing a node provides, so a pass that runs
# before the node-scale pass sets this and does not pay the rollout wait.
set -euo pipefail

ESO_VERSION="${ESO_VERSION:-v2.11.0}"
NAMESPACE="${ESO_NAMESPACE:-default}"
KUBECTL="${KUBECTL:-kubectl}"
ESO_SKIP_ROLLOUT_WAIT="${ESO_SKIP_ROLLOUT_WAIT:-0}"

bundle="${ESO_BUNDLE:-}"
tmp=""
cleanup() {
  if [ -n "$tmp" ]; then
    rm -f "$tmp"
  fi
}
trap cleanup EXIT

if [ -z "$bundle" ]; then
  if ! command -v curl >/dev/null 2>&1; then
    echo "install.sh: curl is not on PATH and ESO_BUNDLE names no local copy of the bundle" >&2
    exit 1
  fi
  tmp="$(mktemp)"
  url="https://github.com/external-secrets/external-secrets/releases/download/${ESO_VERSION}/external-secrets.yaml"
  echo "install.sh: fetching ${url}"
  curl --fail --silent --show-error --location --output "$tmp" "$url"
  bundle="$tmp"
fi

echo "install.sh: applying ${ESO_VERSION} from ${bundle}"
# ESO's validating webhooks default to failurePolicy: Fail. On a tenant cluster with zero nodes its
# controller, cert-controller and webhook pods are all Pending, so the apiserver has no endpoint to
# call and REJECTS every ClusterSecretStore and ExternalSecret with "no endpoints available". This
# bundle sets those webhooks to failurePolicy: Ignore, which is the trade: CRD validation becomes
# best-effort while the controller is down, so a malformed store is admitted and rejected only once
# the controller returns. The vault-facing objects are what a zero-node cluster needs admitted.
# --server-side: client-side apply stores the whole object in the last-applied-configuration
# annotation, and ESO's two largest CRDs exceed the apiserver's 262144-byte annotation limit, so a
# plain kubectl apply -f refuses them.
# ESO_NAMESPACE moves the whole install, not just the rollout wait. The upstream
# bundle names `default` in SIXTEEN places -- the metadata of every namespaced
# object, the namespace of each ClusterRoleBinding's subject, and the webhook's
# service namespace -- and defines no Namespace object of its own, so applying it
# as written puts the controller in `default` whatever ESO_NAMESPACE says. That
# mattered because the tenant grant admits the controller by
# `system:serviceaccount:<namespace>:external-secrets`: with the namespace set to
# anything else, the grant admitted a controller that was not installed and denied
# the one that was.
if [ "$NAMESPACE" != "default" ]; then
  "$KUBECTL" create namespace "$NAMESPACE" --dry-run=client -o yaml | "$KUBECTL" apply -f - >/dev/null
fi
sed -e 's/^\( *failurePolicy: \)Fail$/\1Ignore/' \
  -e "s/\( *namespace: \)default$/\1${NAMESPACE}/" "$bundle" |
  "$KUBECTL" apply --server-side --force-conflicts -f -

if [ "$ESO_SKIP_ROLLOUT_WAIT" = "1" ]; then
  echo "install.sh: ESO_SKIP_ROLLOUT_WAIT=1, so the controller rollout is not awaited; the CRDs are applied and the caller's own probe decides."
else
  echo "install.sh: waiting for the ESO controller to be available"
  if ! "$KUBECTL" -n "$NAMESPACE" rollout status deployment/external-secrets --timeout=180s; then
    echo "install.sh: the ESO controller is not Running yet. On a tenant cluster with no node there" >&2
    echo "install.sh: is nowhere to schedule its pod; the CRDs and webhooks are installed and the" >&2
    echo "install.sh: OpenBao store can be applied now. The controller runs once a node exists." >&2
  fi
fi

echo "install.sh: ESO ${ESO_VERSION} is installed. Apply the OpenBao store with internal/eso after the cluster vault is up."
