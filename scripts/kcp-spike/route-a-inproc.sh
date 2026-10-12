#!/usr/bin/env bash
# M0-KCP-SPIKE route A: run the in-process spike binary on kine sqlite stores (port 0 everywhere):
# one kcp with stop + restart in the same process, then two kcps in one process.
# Usage: route-a-inproc.sh <spike-binary> <workdir>
set -uo pipefail
BIN=${1:?spike binary}
WORK=${2:?workdir}
KINE_BIN=${KINE_BIN:-/usr/local/bin/kine}
rm -rf "$WORK"; mkdir -p "$WORK"
PIDS=()
trap 'for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done' EXIT
kine() {
  local log="$WORK/kine-$1.log"
  "$KINE_BIN" --endpoint "sqlite://$WORK/kine-$1.db" --listen-address 127.0.0.1:0 --metrics-bind-address=0 >"$log" 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 200); do
    u=$(grep -ao 'Kine available at http://[0-9.:]*' "$log" | awk '{print $4}' | tail -1)
    [ -n "$u" ] && { KINE_URL=$u; return; }
    sleep 0.05
  done
}
kine one; K1=$KINE_URL
timeout 300 "$BIN" -root "$WORK/single" -etcd-servers "$K1" -instances 1 -restart -settle 30s >"$WORK/single.out" 2>"$WORK/single.err"
echo "single exit=$?"; grep -a '^RESULT\|^SPIKE' "$WORK/single.out" "$WORK/single.err" | cut -c1-300
kine two; K2=$KINE_URL; kine three; K3=$KINE_URL
timeout 300 "$BIN" -root "$WORK/double" -etcd-servers "$K2,$K3" -instances 2 -settle 10s >"$WORK/double.out" 2>"$WORK/double.err"
echo "double exit=$?"; grep -a '^RESULT\|^SPIKE' "$WORK/double.out" "$WORK/double.err" | cut -c1-300
grep -a -m5 -E '^panic|duplicate|already registered|fatal error' "$WORK/double.err" | cut -c1-300
