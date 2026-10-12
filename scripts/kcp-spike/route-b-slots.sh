#!/usr/bin/env bash
# M0-KCP-SPIKE route B, slot shape (user decision 2026-10-12): release kcp as blue/green slots, one kine per
# slot on one postgres, --enable-leader-election, --shard-base-url at a front. Measures how the bound port of a
# --secure-port=0 slot can be read back with no kcp patch, and which root-dir material a new slot needs.
# Usage: route-b-slots.sh <workdir>
# Stand-in processes only, 127.0.0.1 only; kills only the pids it started.
set -euo pipefail

WORK=${1:?workdir}
KCP_BIN=${KCP_BIN:-/usr/local/bin/kcp}
KINE_BIN=${KINE_BIN:-/usr/local/bin/kine}
PG_BIN_DIR=${PG_BIN_DIR:-/usr/bin}
rm -rf "$WORK"; mkdir -p "$WORK"
SOCK=$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/kcpspike-pg.XXXXXX")

PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  for p in "${PIDS[@]:-}"; do if [ -n "$p" ]; then while kill -0 "$p" 2>/dev/null; do sleep 0.1; done; fi; done
  rm -rf "$SOCK"
}
trap cleanup EXIT

now() { date +%s.%N; }
since() { echo "$(now) - $1" | bc; }
say() { echo "SLOTS $*"; }

free_port() {
  local p
  while :; do
    p=$(( 20000 + RANDOM % 9000 ))
    ss -ltnH "sport = :$p" | grep -q . || { echo "$p"; return; }
  done
}

"$PG_BIN_DIR/initdb" -D "$WORK/pg" -U kine --auth=trust >"$WORK/initdb.log" 2>&1
"$PG_BIN_DIR/postgres" -D "$WORK/pg" -c listen_addresses= -k "$SOCK" -c logging_collector=off >"$WORK/postgres.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 200); do [ -S "$SOCK/.s.PGSQL.5432" ] && break; sleep 0.05; done
sleep 0.5
PG_URL="postgres://kine@/kcp?host=$SOCK&sslmode=disable"

start_kine() {
  local name=$1 log="$WORK/kine-$1.log"
  "$KINE_BIN" --endpoint "$PG_URL" --listen-address 127.0.0.1:0 --metrics-bind-address=0 >"$log" 2>&1 &
  PIDS+=($!)
  for _ in $(seq 1 400); do
    url=$(grep -ao 'Kine available at http://[0-9.:]*' "$log" | awk '{print $4}' | tail -1 || true)
    [ -n "$url" ] && { eval "KINE_$name=$url"; return 0; }
    sleep 0.05
  done
  echo "kine $name did not report; see $log" >&2; return 1
}

FRONT=$(free_port)
SHARD_URL="https://127.0.0.1:$FRONT"
say "front=$SHARD_URL (socat, retargeted on swap)"

port_from_ss() { ss -ltnpH 2>/dev/null | awk -v p="pid=$1," 'index($0,p){split($4,a,":"); print a[length(a)]}' | head -1 || true; }
port_from_log() { grep -ao 'Serving securely on [0-9.]*:[0-9]*' "$1" | head -1 | awk -F: '{print $NF}' || true; }

start_slot() {
  local name=$1 kine=$2 root="$WORK/slot-$1"
  mkdir -p "$root"
  local log="$WORK/kcp-$name.log" t0
  t0=$(now)
  "$KCP_BIN" start --root-directory="$root" --etcd-servers="$kine" --bind-address=127.0.0.1 \
    --secure-port=0 --experimental-bind-free-port --shard-base-url="$SHARD_URL" \
    --enable-leader-election --shutdown-delay-duration=2s --feature-gates=WorkspaceMounts=true \
    >"$log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  eval "PID_$name=$pid"
  local tss= tlog= tr= p_ss= p_log=
  for _ in $(seq 1 3000); do
    if [ -z "$tss" ]; then p_ss=$(port_from_ss "$pid"); [ -n "$p_ss" ] && tss=$(since "$t0"); fi
    if [ -z "$tlog" ]; then p_log=$(port_from_log "$log"); [ -n "$p_log" ] && tlog=$(since "$t0"); fi
    if [ -n "$p_ss" ] && [ "$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:$p_ss/readyz" || true)" = 200 ]; then tr=$(since "$t0"); break; fi
    kill -0 "$pid" 2>/dev/null || { say "$name exited before ready: $(grep -a -m1 -iE 'error|fatal' "$log" | cut -c1-300)"; eval "PORT_$name="; return 0; }
    sleep 0.05
  done
  eval "PORT_$name=$p_ss"
  local kc
  kc=$(sed -n 's|^ *server: \(https://[^/]*\).*|\1|p' "$root/admin.kubeconfig" 2>/dev/null | sort -u | paste -sd, -)
  say "$name pid=$pid port_ss=$p_ss@${tss}s port_log=$p_log@${tlog}s readyz=${tr}s admin.kubeconfig_servers=$kc rss_kb=$(awk '/VmRSS/{print $2}' /proc/$pid/status)"
}


start_kine a
start_slot a "$KINE_a"
socat TCP-LISTEN:"$FRONT",bind=127.0.0.1,reuseaddr,fork TCP:127.0.0.1:"$PORT_a" >"$WORK/front.log" 2>&1 &
FRONT_PID=$!
PIDS+=("$FRONT_PID")
ROOT_A="$WORK/slot-a"
kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" --context root get shards -o jsonpath='{range .items[*]}{.metadata.name} base={.spec.baseURL} vw={.spec.virtualWorkspaceURL}{"\n"}{end}' | sed 's/^/SLOTS shard /'
kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" --context root create serviceaccount spike -n default >/dev/null
SA_TOKEN=$(kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" --context root create token spike -n default)
SHARD_ADMIN_TOKEN=$(kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" config view --raw -o jsonpath='{.users[?(@.name=="shard-admin")].user.token}')
KCP_ADMIN_TOKEN_A=$(kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" config view --raw -o jsonpath='{.users[?(@.name=="kcp-admin")].user.token}')
say "tokens: shard-admin=${#SHARD_ADMIN_TOKEN}c kcp-admin=${#KCP_ADMIN_TOKEN_A}c sa=${#SA_TOKEN}c"

probe() {
  local label=$1 port=$2 token=$3 path=$4 cafile=$5
  local code
  code=$(curl -s --cacert "$cafile" -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "https://127.0.0.1:$port$path" || echo "tls-fail")
  say "probe $label -> $code"
}

copy_shared() {
  local from=$1 to=$2; shift 2
  mkdir -p "$to"
  for f in "$@"; do cp -p "$from/$f" "$to/$f"; done
}

start_kine b
copy_shared "$ROOT_A" "$WORK/slot-b" apiserver.crt apiserver.key sa.key .admin-token-store admin.kubeconfig
start_slot b "$KINE_b"
if [ -n "${PORT_b:-}" ]; then
  probe "b(copied) shard-admin token from a" "$PORT_b" "$SHARD_ADMIN_TOKEN" /clusters/root/api/v1/namespaces "$ROOT_A/apiserver.crt"
  probe "b(copied) sa token minted by a" "$PORT_b" "$SA_TOKEN" /clusters/root/api/v1/namespaces/default/serviceaccounts/spike "$ROOT_A/apiserver.crt"
  probe "b(copied) kcp-admin volatile token of a" "$PORT_b" "$KCP_ADMIN_TOKEN_A" /clusters/root/api/v1/namespaces "$ROOT_A/apiserver.crt"
  probe "a sa token on a" "$PORT_a" "$SA_TOKEN" /clusters/root/api/v1/namespaces/default/serviceaccounts/spike "$ROOT_A/apiserver.crt"
  say "b admin.kubeconfig shard-admin token unchanged: $([ "$(kubectl --kubeconfig "$WORK/slot-b/admin.kubeconfig" config view --raw -o jsonpath='{.users[?(@.name=="shard-admin")].user.token}')" = "$SHARD_ADMIN_TOKEN" ] && echo yes || echo no)"
  say "lease holder: $(kubectl --kubeconfig "$ROOT_A/admin.kubeconfig" --context system:admin get lease -n kube-system kcp-controllers -o jsonpath='{.spec.holderIdentity}' 2>&1 | cut -c1-200)"
fi

start_kine c
copy_shared "$ROOT_A" "$WORK/slot-c" apiserver.crt apiserver.key sa.key .admin-token-store
start_slot c "$KINE_c"

start_kine d
start_slot d "$KINE_d"
if [ -n "${PORT_d:-}" ]; then
  probe "d(fresh root) shard-admin token from a" "$PORT_d" "$SHARD_ADMIN_TOKEN" /clusters/root/api/v1/namespaces "$WORK/slot-d/apiserver.crt"
  probe "d(fresh root) sa token minted by a" "$PORT_d" "$SA_TOKEN" /clusters/root/api/v1/namespaces/default/serviceaccounts/spike "$WORK/slot-d/apiserver.crt"
  probe "d(fresh root) with a's CA" "$PORT_d" "$SHARD_ADMIN_TOKEN" /readyz "$ROOT_A/apiserver.crt"
  kill "$(eval echo \$PID_d)"
fi

kill "$FRONT_PID" 2>/dev/null || true
socat TCP-LISTEN:"$FRONT",bind=127.0.0.1,reuseaddr,fork TCP:127.0.0.1:"$PORT_b" >"$WORK/front-b.log" 2>&1 &
PIDS+=($!)
t=$(now)
kill -TERM "$PID_a"
while kill -0 "$PID_a" 2>/dev/null; do sleep 0.05; done
say "a stopped after $(since "$t")s"
kubectl --kubeconfig "$WORK/slot-b/admin.kubeconfig" --context system:admin --server "$SHARD_URL/clusters/system:admin" delete lease -n kube-system kcp-controllers 2>&1 | sed 's/^/SLOTS lease delete: /'
t=$(now)
for _ in $(seq 1 300); do
  h=$(kubectl --kubeconfig "$WORK/slot-b/admin.kubeconfig" --context system:admin --server "$SHARD_URL/clusters/system:admin" get lease -n kube-system kcp-controllers -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || true)
  [ -n "$h" ] && break
  sleep 0.1
done
say "lease re-acquired by '$h' after $(since "$t")s"
kubectl --kubeconfig "$WORK/slot-b/admin.kubeconfig" --context root --server "$SHARD_URL/clusters/root" get serviceaccount spike -n default -o name 2>&1 | sed 's/^/SLOTS through front after swap: /'
