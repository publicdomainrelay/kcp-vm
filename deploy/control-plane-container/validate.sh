#!/bin/bash
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${KCP_CONTROLPLANE_IMAGE:-socialweb-control-plane:local}"
NODEIMAGE_CONFIG="${NODEIMAGE_CONFIG:-/home/johnandersen777/kcp-image/nodeimage.json}"
TEST="${KCP_VALIDATE_TEST:-TestTheControlPlaneReachesReadyAtZeroNodes}"

require_positive_pass() {
  local log="$1"
  local pass_line skip_line notests_line
  pass_line="$(grep -E "^--- PASS:.*(${TEST})" "$log" | head -n 1 || true)"
  skip_line="$(grep -E "^--- SKIP:" "$log" | head -n 1 || true)"
  notests_line="$(grep -F '[no tests to run]' "$log" | head -n 1 || true)"
  if [[ -n "$skip_line" ]]; then
    echo "FAIL: the driven test printed a SKIP instead of a PASS, so the control plane was not proven: $skip_line" >&2
    echo "go test exits 0 on SKIP, so this gate reads the verdict itself instead of trusting that exit code." >&2
    return 1
  fi
  if [[ -z "$pass_line" ]]; then
    echo "FAIL: no passing verdict matching '--- PASS: $TEST' was printed, so the control plane was not proven." >&2
    if [[ -n "$notests_line" ]]; then
      echo "go test reported: $notests_line" >&2
    else
      echo "Expected a line matching '--- PASS: ...$TEST' and saw none." >&2
    fi
    return 1
  fi
  echo "PROOF: $pass_line"
  return 0
}

if [[ "${1:-}" == "--gate-check" ]]; then
  require_positive_pass "${2:-/dev/null}" || exit 1
  exit 0
fi

TOOLCHAIN="${KCP_GO_TOOLCHAIN:-$(go env GOROOT)}"
MODCACHE="${KCP_GO_MODCACHE:-$(go env GOMODCACHE)}"
BUILDCACHE="${KCP_GO_BUILDCACHE:-$(go env GOCACHE)}"
STATE="${KCP_PROOF_STATE:-/var/tmp/socialweb-control-plane-proof-state}"

install -d -m 0700 "$STATE"

echo "== this container's own report on what it was and was not given =="
docker rm -f kcp-checkhost >/dev/null 2>&1 || true
CHECKHOST_OUT="$(docker run --rm --name kcp-checkhost "$IMAGE" \
  socialweb-computer-kcp-check-host -config "$NODEIMAGE_CONFIG" 2>&1 || true)"
printf '%s\n' "$CHECKHOST_OUT"

CHECKHOST_SUMMARY=""
CHECKHOST_TOTAL=""
CHECKHOST_HOLDS=""
CHECKHOST_MISSING=""
CHECKHOST_PASSED_IN=""
CHECKHOST_SUMMARY_RE='^([0-9]+) checks: ([0-9]+) HOLDS, ([0-9]+) MISSING, ([0-9]+) MUST BE PASSED IN$'
while IFS= read -r checkhost_line; do
  if [[ "$checkhost_line" =~ $CHECKHOST_SUMMARY_RE ]]; then
    CHECKHOST_SUMMARY="$checkhost_line"
    CHECKHOST_TOTAL="${BASH_REMATCH[1]}"
    CHECKHOST_HOLDS="${BASH_REMATCH[2]}"
    CHECKHOST_MISSING="${BASH_REMATCH[3]}"
    CHECKHOST_PASSED_IN="${BASH_REMATCH[4]}"
  fi
done <<< "$CHECKHOST_OUT"

if [[ -z "$CHECKHOST_SUMMARY" ]]; then
  echo "FAIL: check-host printed no summary line, so this run cannot say what the container was and" >&2
  echo "was not given. Its exit code is EXPECTED to be non-zero here and is deliberately NOT read:" >&2
  echo "the image is the control-plane half only, so it honestly reports MISSING and MUST BE PASSED IN" >&2
  echo "and finishes non-zero. An exit-code allowlist for that would swallow every OTHER ending too --" >&2
  echo "a crash, a missing binary, an OOM kill -- and print identically. A summary line is the positive" >&2
  echo "assertion instead: it can only be absent when the binary did not run to its verdict. Expected a" >&2
  echo "line of exactly this form (the format is cmd/socialweb-computer-kcp-check-host/report.go's own):" >&2
  echo "  N checks: A HOLDS, B MISSING, C MUST BE PASSED IN" >&2
  exit 1
fi

if (( CHECKHOST_TOTAL != CHECKHOST_HOLDS + CHECKHOST_MISSING + CHECKHOST_PASSED_IN )); then
  echo "FAIL: check-host's own counts do not add up: $CHECKHOST_SUMMARY" >&2
  echo "Every check is exactly one of HOLDS, MISSING or MUST BE PASSED IN, so the total is their sum." >&2
  echo "A line that does not add up lost or double-counted a check, and its per-category numbers" >&2
  echo "cannot be read as evidence while it does." >&2
  exit 1
fi

echo
echo "== the control plane, driven by $TEST inside the container =="
docker rm -f kcp-zeronode >/dev/null 2>&1 || true
set +e
docker run --rm --name kcp-zeronode \
  -v "$REPO:/src:ro" \
  -v "$TOOLCHAIN:/go/toolchain:ro" \
  -v "$MODCACHE:/go/pkg/mod" \
  -v "$BUILDCACHE:/go/cache" \
  -v "$STATE:/home/kcp/proof-state" \
  -e PATH=/go/toolchain/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  -e GOPATH=/go \
  -e GOMODCACHE=/go/pkg/mod \
  -e GOCACHE=/go/cache \
  -e GOTOOLCHAIN=local \
  -e GOPROXY=off \
  -e GOFLAGS=-mod=mod \
  -e SOCIALWEB_LOCAL=1 \
  -e PROOF_STATE_DIR=/home/kcp/proof-state/state \
  -w /src \
  "$IMAGE" \
  go test ./internal/clusterprovider -run "$TEST" -v -count=1 2>&1 | tee "$STATE/go-test.log"
ZERONODE_RC="${PIPESTATUS[0]}"
set -e
if ! require_positive_pass "$STATE/go-test.log"; then
  echo "go test's own exit code was $ZERONODE_RC; its full output is $STATE/go-test.log" >&2
  exit 1
fi
if (( ZERONODE_RC != 0 )); then
  echo "FAIL: a passing verdict was printed but go test exited $ZERONODE_RC" >&2
  exit 1
fi
echo "the cluster's own state, including every component's log, is under $STATE"
