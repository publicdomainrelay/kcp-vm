#!/usr/bin/env bash
set -u

mode="${1:---fast}"
case "$mode" in
  --fast) run=fast ;;
  --local) run=local ;;
  --full) run=full ;;
  *)
    echo "usage: $0 [--fast|--local|--full]" >&2
    echo "  --fast   unit tests only; the loop, seconds" >&2
    echo "  --local  the proofs this host can run (kcp/kine/k3s/postgres); lists what it skipped" >&2
    echo "  --full   everything, and a skip is a failure; needs the live artifacts on this host" >&2
    exit 2
    ;;
esac

fail() {
  echo "check.sh: FAILED $1" >&2
  exit 1
}

unformatted="$(gofmt -l api internal cmd test)"
if [ -n "$unformatted" ]; then
  echo "$unformatted" >&2
  fail gofmt
fi
echo "check.sh: ok gofmt"

go build ./... || fail "go build"
echo "check.sh: ok go build"

go vet ./... || fail "go vet"
echo "check.sh: ok go vet"

staticcheck ./... || fail staticcheck
echo "check.sh: ok staticcheck"

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

case "$run" in
  fast)
    go test ./... -count=1 || fail "go test"
    echo "check.sh: ok go test"
    ;;
  local)
    if ! SOCIALWEB_LOCAL=1 go test ./... -count=1 -v >"$log" 2>&1; then
      grep -E -- '^(--- FAIL|FAIL)' "$log" | head -30 >&2
      fail "go test local"
    fi
    skipped="$(grep -c -- '^--- SKIP' "$log" || true)"
    echo "check.sh: ok go test local ($skipped artifact-gated tests skipped and named below)"
    grep -- '^--- SKIP' "$log" | sed 's/^/  /' || true
    ;;
  full)
    if ! SOCIALWEB_REQUIRE_LIVE=1 go test ./... -count=1 -v >"$log" 2>&1; then
      echo "check.sh: --full needs live inputs this host must be handed; the failing tests and the input each names:" >&2
      grep -E -- '^(--- FAIL|[[:space:]]+[A-Za-z0-9_./-]+\.go:[0-9]+:)' "$log" | head -60 >&2
      fail "go test full"
    fi
    echo "check.sh: ok go test full (nothing skipped)"
    ;;
esac
