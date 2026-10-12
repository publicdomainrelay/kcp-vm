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

go test -count=1 -run 'TestNoFixedPortBehindTheEdge|TestTheRatchetListsOnlyViolationsThatStillExist|TestEveryEdgePortHasAReason|TestAServiceManifestWithoutAnExplicitPortIsAViolation' ./test \
  || fail "no fixed ports behind the edge (deploy/edge-ports.yaml, deploy/fixed-ports-ratchet.yaml)"
echo "check.sh: ok no fixed ports behind the edge ($(grep -c '^  - {file:' deploy/fixed-ports-ratchet.yaml) ratchet entries left)"

crossfailed=""
for target in darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 linux/arm64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  if ! GOOS="$goos" GOARCH="$goarch" go build ./...; then
    crossfailed="$crossfailed build:$target"
    continue
  fi
  if ! GOOS="$goos" GOARCH="$goarch" go vet ./...; then
    crossfailed="$crossfailed vet:$target"
    continue
  fi
  if ! GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -tags release -o /dev/null ./cmd/socialweb-computer; then
    crossfailed="$crossfailed release-build:$target"
    continue
  fi
  echo "check.sh: ok go build and go vet for $target"
done
if [ -n "$crossfailed" ]; then
  echo "check.sh: the tree must build and vet for every GOOS/GOARCH above; an OS-specific file needs its build tag and a twin for the other OSes (plans 0011, 0012)" >&2
  fail "cross-OS gate:$crossfailed"
fi

sizedir="$(mktemp -d)"
for target in $(jq -r '.platforms | to_entries[] | select(.value.status == "release") | .key' internal/versions/versions.json); do
  goos="${target%/*}"
  goarch="${target#*/}"
  if ! GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -tags release -trimpath -o "$sizedir/$goos-$goarch" ./cmd/socialweb-computer; then
    rm -rf "$sizedir"
    fail "release build $target"
  fi
  override=()
  if [ -n "${SOCIALWEB_SIZE_BUDGET_BYTES:-}" ]; then
    override=(-budget-bytes "$SOCIALWEB_SIZE_BUDGET_BYTES")
  fi
  if ! measured="$(go run ./internal/versions/sizegate -platform "$target" -binary "$sizedir/$goos-$goarch" -blobs "internal/assets/blobs/${goos}_${goarch}" -closure "internal/tsbundle/closure/${goos}_${goarch}" "${override[@]}")"; then
    rm -rf "$sizedir"
    fail "size budget $target (internal/versions/versions.json platforms.\"$target\".budget)"
  fi
  rm -f "$sizedir/$goos-$goarch"
  echo "check.sh: ok size budget $target $measured"
done
rm -rf "$sizedir"

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

if [ "$run" != fast ]; then
  hostplat="$(go env GOOS)_$(go env GOARCH)"
  if [ -n "$(ls internal/assets/blobs/"$hostplat"/*.tar.zst 2>/dev/null)" ]; then
    go test -tags release -count=1 -run TestEveryEmbeddedAssetExtractsAndVerifies ./internal/assets || fail "embedded assets extract and verify ($hostplat)"
    echo "check.sh: ok embedded assets extract and verify ($hostplat)"
  elif [ "$run" = full ]; then
    fail "no staged blobs for $hostplat; --full extracts every embedded asset: run scripts/stage-assets.sh"
  else
    echo "check.sh: skipped embedded asset extraction (no staged blobs for $hostplat; run scripts/stage-assets.sh)"
  fi
fi

case "$run" in
  fast)
    go test ./... -count=1 || fail "go test"
    echo "check.sh: ok go test"
    ;;
  local)
    if ! SOCIALWEB_LOCAL=1 go test ./... -count=1 -timeout 60m -v >"$log" 2>&1; then
      grep -E -- '^(--- FAIL|FAIL)' "$log" | head -30 >&2
      fail "go test local"
    fi
    skipped="$(grep -c -- '^--- SKIP' "$log" || true)"
    echo "check.sh: ok go test local ($skipped artifact-gated tests skipped and named below)"
    grep -- '^--- SKIP' "$log" | sed 's/^/  /' || true
    ;;
  full)
    if ! SOCIALWEB_REQUIRE_LIVE=1 go test ./... -count=1 -timeout 60m -v >"$log" 2>&1; then
      echo "check.sh: --full needs live inputs this host must be handed; the failing tests and the input each names:" >&2
      grep -E -- '^(--- FAIL|[[:space:]]+[A-Za-z0-9_./-]+\.go:[0-9]+:)' "$log" | head -60 >&2
      fail "go test full"
    fi
    echo "check.sh: ok go test full (nothing skipped)"
    ;;
esac
