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
set -euo pipefail

ESO_VERSION="${ESO_VERSION:-v2.11.0}"
NAMESPACE="${ESO_NAMESPACE:-default}"
KUBECTL="${KUBECTL:-kubectl}"

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
sed 's/^\( *failurePolicy: \)Fail$/\1Ignore/' "$bundle" | "$KUBECTL" apply --server-side --force-conflicts -f -

echo "install.sh: waiting for the ESO controller to be available"
if ! "$KUBECTL" -n "$NAMESPACE" rollout status deployment/external-secrets --timeout=180s; then
  echo "install.sh: the ESO controller is not Running yet. On a tenant cluster with no node there" >&2
  echo "install.sh: is nowhere to schedule its pod; the CRDs and webhooks are installed and the" >&2
  echo "install.sh: OpenBao store can be applied now. The controller runs once a node exists." >&2
fi

echo "install.sh: ESO ${ESO_VERSION} is installed. Apply the OpenBao store with internal/eso after the cluster vault is up."
