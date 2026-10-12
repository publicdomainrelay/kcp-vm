#!/usr/bin/env bash
# M0-KCP-SPIKE route B: release kcp in its own process over an external kine store.
# Usage: route-b.sh <workdir> [runs]
# Binds 127.0.0.1 only, every listener on port 0; postgres listens on a unix socket only.
# Kills only the pids it started.
set -euo pipefail

WORK=${1:?workdir}
RUNS=${2:-3}
KCP_BIN=${KCP_BIN:-/usr/local/bin/kcp}
KINE_BIN=${KINE_BIN:-/usr/local/bin/kine}
PG_BIN_DIR=${PG_BIN_DIR:-/usr/bin}
AUDIT_POLICY=${AUDIT_POLICY:-$(cd "$(dirname "$0")/../.." && pwd)/deploy/kcp-audit-policy.yaml}

mkdir -p "$WORK"
RESULTS="$WORK/results.tsv"
[ -f "$RESULTS" ] || printf 'store\trun\tphase\tkubeconfig_s\tlog_port_s\treadyz_s\tport\trss_kb_ready\trss_kb_30s\thwm_kb_30s\tkine_rss_kb\tlisteners\tstop_s\n' >"$RESULTS"

PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null || true
  done
  for p in "${PIDS[@]:-}"; do
    [ -n "$p" ] && { while kill -0 "$p" 2>/dev/null; do sleep 0.1; done; } || true
  done
}
trap cleanup EXIT

now() { date +%s.%N; }
since() { echo "$(now) - $1" | bc; }
rss() { awk '/VmRSS/{print $2}' "/proc/$1/status" 2>/dev/null || echo -; }
hwm() { awk '/VmHWM/{print $2}' "/proc/$1/status" 2>/dev/null || echo -; }
listeners() { ss -ltnpH 2>/dev/null | awk -v p="pid=$1," 'index($0,p){print $4}' | sort | paste -sd, -; }

start_kine() {
  local endpoint=$1 log=$2
  "$KINE_BIN" --endpoint "$endpoint" --listen-address 127.0.0.1:0 --metrics-bind-address=0 >"$log" 2>&1 &
  KINE_PID=$!
  PIDS+=("$KINE_PID")
  KINE_URL=
  for _ in $(seq 1 200); do
    KINE_URL=$(grep -ao 'Kine available at http://[0-9.:]*' "$log" | awk '{print $4}' | tail -1 || true)
    [ -n "$KINE_URL" ] && return 0
    sleep 0.05
  done
  echo "kine did not report a url; see $log" >&2
  return 1
}

stop_pid() {
  local p=$1 t
  t=$(now)
  kill -TERM "$p" 2>/dev/null || true
  while kill -0 "$p" 2>/dev/null; do sleep 0.05; done
  since "$t"
}

run_kcp() {
  local store=$1 run=$2 phase=$3 root=$4
  local log="$root.$phase.$run.log"
  local stale_port=
  stale_port=$(sed -n 's|^ *server: https://127\.0\.0\.1:\([0-9]*\)$|\1|p' "$root/admin.kubeconfig" 2>/dev/null | head -1 || true)
  local t0 tk= tl= tr= port= code
  t0=$(now)
  "$KCP_BIN" start \
    --root-directory="$root" \
    --etcd-servers="$KINE_URL" \
    --bind-address=127.0.0.1 \
    --secure-port=0 \
    --experimental-bind-free-port \
    --feature-gates=WorkspaceMounts=true \
    --audit-policy-file="$AUDIT_POLICY" \
    --audit-log-path="$root/audit.log" \
    >"$log" 2>&1 &
  local kp=$!
  PIDS+=("$kp")
  for _ in $(seq 1 1800); do
    if [ -z "$tl" ] && grep -aq 'Serving securely on' "$log"; then tl=$(since "$t0"); fi
    port=$(sed -n 's|^ *server: https://127\.0\.0\.1:\([0-9]*\)$|\1|p' "$root/admin.kubeconfig" 2>/dev/null | head -1 || true)
    if [ -z "$tk" ] && [ -n "$port" ] && [ "$port" != "$stale_port" ]; then tk=$(since "$t0"); fi
    if [ -n "$tk" ]; then
      code=$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:$port/readyz" || true)
      if [ "$code" = 200 ]; then tr=$(since "$t0"); break; fi
    fi
    if ! kill -0 "$kp" 2>/dev/null; then echo "kcp exited; see $log" >&2; return 1; fi
    sleep 0.05
  done
  local r1 r30 h30 kr l st
  r1=$(rss "$kp")
  l=$(listeners "$kp")
  sleep 30
  r30=$(rss "$kp"); h30=$(hwm "$kp"); kr=$(rss "$KINE_PID")
  st=$(stop_pid "$kp")
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$store" "$run" "$phase" "$tk" "$tl" "$tr" "$port" "$r1" "$r30" "$h30" "$kr" "$l" "$st" | tee -a "$RESULTS"
}

measure_store() {
  local store=$1 endpoint_fn=$2
  for run in $(seq 1 "$RUNS"); do
    local d="$WORK/$store-$run"
    rm -rf "$d"; mkdir -p "$d/root"
    "$endpoint_fn" "$d"
    start_kine "$ENDPOINT" "$d/kine.log"
    run_kcp "$store" "$run" fresh "$d/root"
    run_kcp "$store" "$run" restart "$d/root"
    stop_pid "$KINE_PID" >/dev/null
    if [ -n "${PG_PID:-}" ]; then stop_pid "$PG_PID" >/dev/null; PG_PID=; rm -rf "$(readlink "$d/pgsock")"; fi
  done
}

sqlite_endpoint() { ENDPOINT="sqlite://$1/kine.db?_journal=WAL&cache=shared&_busy_timeout=30000"; }

postgres_endpoint() {
  local d=$1 sock
  sock=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/kcpspike-pg.XXXXXX")
  ln -sfn "$sock" "$d/pgsock"
  "$PG_BIN_DIR/initdb" -D "$d/pg" -U kine --auth=trust >"$d/initdb.log" 2>&1
  "$PG_BIN_DIR/postgres" -D "$d/pg" -c listen_addresses= -k "$sock" -c logging_collector=off -c fsync=on >"$d/postgres.log" 2>&1 &
  PG_PID=$!
  PIDS+=("$PG_PID")
  for _ in $(seq 1 200); do [ -S "$sock/.s.PGSQL.5432" ] && break; sleep 0.05; done
  sleep 0.5
  ENDPOINT="postgres://kine@/kine?host=$sock&sslmode=disable"
}

STORES=${STORES:-sqlite postgres}
for store in $STORES; do
  if [ "$store" = postgres ] && [ ! -x "$PG_BIN_DIR/postgres" ]; then echo "postgres not found; skipped" >&2; continue; fi
  measure_store "$store" "${store}_endpoint"
done

echo "results: $RESULTS"
