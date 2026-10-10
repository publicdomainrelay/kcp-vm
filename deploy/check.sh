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

gen="$(mktemp -d)"
go run ./cmd/socialweb-apigen -root . -out "$gen" || { rm -rf "$gen"; fail "apigen"; }
drift=0
compared=0
for generated in $(cd "$gen" && find . -type f | sort); do
  rel="${generated#./}"
  compared=$((compared + 1))
  if ! diff -u "$rel" "$gen/$rel" >&2; then
    drift=1
  fi
done
rm -rf "$gen"
if [ "$compared" -eq 0 ]; then
  fail "apigen generated nothing to compare"
fi
if [ "$drift" -ne 0 ]; then
  echo "check.sh: the files above are generated from api/v1alpha1 and differ from what it generates now; run: go generate ./api/..." >&2
  fail "apigen drift"
fi
echo "check.sh: ok apigen ($compared generated files, no drift from api/v1alpha1)"

go vet ./... || fail "go vet"
echo "check.sh: ok go vet"

staticcheck ./... || fail staticcheck
echo "check.sh: ok staticcheck"

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

rust_check() {
  if ! command -v cargo >/dev/null 2>&1; then
    if [ "$run" = full ]; then
      fail "cargo not found; --full builds and tests rust/socialweb-meshd"
    fi
    echo "check.sh: skipped cargo test (cargo not found, so rust/socialweb-meshd is not tested on this host)"
    return
  fi
  if cargo fmt --version >/dev/null 2>&1; then
    cargo fmt --check --manifest-path rust/Cargo.toml || fail "cargo fmt"
    echo "check.sh: ok cargo fmt"
  else
    echo "check.sh: skipped cargo fmt (rustfmt not installed)"
  fi
  gates=""
  if [ "$run" != fast ]; then
    if sudo -n true >/dev/null 2>&1; then
      gates="SOCIALWEB_MESHD_NETNS=1 SOCIALWEB_MESHD_K3S=1"
    elif [ "$run" = full ]; then
      fail "sudo -n unavailable; --full runs the meshd netns and k3s proofs, which need CAP_NET_ADMIN"
    fi
  fi
  env $gates cargo test --locked --manifest-path rust/Cargo.toml || fail "cargo test"
  if [ -n "$gates" ]; then
    echo "check.sh: ok cargo test (rust/, with the netns and k3s proofs)"
  else
    echo "check.sh: ok cargo test (rust/; the netns and k3s proofs need SOCIALWEB_MESHD_NETNS=1 / SOCIALWEB_MESHD_K3S=1 and sudo -n)"
  fi
}
rust_check

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
