#!/usr/bin/env bash
set -euo pipefail

# Starts kine and kcp under ROOT. Both bind port 0 by default and the port
# each one got is read back from the process itself -- kine's single LISTEN
# socket, kcp's admin.kubeconfig checked against its LISTEN socket -- and
# written to ROOT/kine.endpoint and ROOT/kcp.port, so two of these on one host
# never race for a port and nothing ever binds, closes and rebinds one.
#
# ROOT             state directory (default <repo>/.kcp-demo, the same default
#                  stop-kcp.sh uses)
# KINE_ENDPOINT    http://127.0.0.1:<port>; port 0 (the default) binds any
# KCP_SECURE_PORT  kcp's secure port; 0 (the default) binds any through
#                  --experimental-bind-free-port

REPO=$(cd "$(dirname "$0")/../.." && pwd)
ROOT=${ROOT:-$REPO/.kcp-demo}
KINE_ENDPOINT=${KINE_ENDPOINT:-http://127.0.0.1:0}
KCP_SECURE_PORT=${KCP_SECURE_PORT:-0}
KCP_BIN=${KCP_BIN:-kcp}
KINE_BIN=${KINE_BIN:-kine}
KCP_FEATURE_GATES=${KCP_FEATURE_GATES:-WorkspaceMounts=true}

mkdir -p "$ROOT"

listen_ports() {
  ss -Hltnp 2>/dev/null | awk -v want="pid=$1," 'index($0, want) { n = split($4, a, ":"); print a[n] }' | sort -u
}

alive() {
  [ -s "$ROOT/$1.pid" ] && kill -0 "$(cat "$ROOT/$1.pid")" 2>/dev/null
}

wait_listen() {
  local pid=$1 tries=$2 ports
  for _ in $(seq 1 "$tries"); do
    ports=$(listen_ports "$pid")
    if [ -n "$ports" ]; then
      echo "$ports"
      return 0
    fi
    kill -0 "$pid" 2>/dev/null || return 1
    sleep 0.25
  done
  return 1
}

kubeconfig_port() {
  [ -f "$ROOT/admin.kubeconfig" ] || return 1
  sed -n 's|^ *server: https://127\.0\.0\.1:\([0-9]*\).*|\1|p' "$ROOT/admin.kubeconfig" | head -1
}

kcp_ready() {
  local port
  port=$(cat "$ROOT/kcp.port" 2>/dev/null) || return 1
  [ -n "$port" ] && [ "$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:${port}/readyz" 2>/dev/null)" = "200" ]
}

if alive kine && [ -s "$ROOT/kine.endpoint" ]; then
  KINE_ENDPOINT=$(cat "$ROOT/kine.endpoint")
else
  rm -f "$ROOT/kine.endpoint"
  "$KINE_BIN" \
    --endpoint "sqlite://$ROOT/kine.db" \
    --listen-address "${KINE_ENDPOINT#http://}" \
    --metrics-bind-address=0 \
    >"$ROOT/kine.log" 2>&1 &
  echo "$!" >"$ROOT/kine.pid"
  if ! kine_port=$(wait_listen "$(cat "$ROOT/kine.pid")" 120); then
    echo "kine did not start listening; see $ROOT/kine.log" >&2
    exit 1
  fi
  KINE_ENDPOINT="http://127.0.0.1:$(echo "$kine_port" | head -1)"
  echo "$KINE_ENDPOINT" >"$ROOT/kine.endpoint"
fi

if ! alive kcp; then
  rm -f "$ROOT/kcp.port" "$ROOT/admin.kubeconfig"
  port_args=(--secure-port="$KCP_SECURE_PORT")
  if [ "$KCP_SECURE_PORT" = "0" ]; then
    port_args+=(--experimental-bind-free-port)
  fi
  "$KCP_BIN" start \
    --root-directory="$ROOT" \
    --etcd-servers="$KINE_ENDPOINT" \
    --bind-address=127.0.0.1 \
    "${port_args[@]}" \
    --feature-gates="$KCP_FEATURE_GATES" \
    >"$ROOT/kcp.log" 2>&1 &
  echo "$!" >"$ROOT/kcp.pid"
  kcp_pid=$(cat "$ROOT/kcp.pid")
  bound=""
  for _ in $(seq 1 240); do
    if kc_port=$(kubeconfig_port) && [ -n "$kc_port" ] && listen_ports "$kcp_pid" | grep -qx "$kc_port"; then
      bound=$kc_port
      break
    fi
    kill -0 "$kcp_pid" 2>/dev/null || break
    sleep 0.25
  done
  if [ -z "$bound" ]; then
    echo "kcp never named a port it holds in $ROOT/admin.kubeconfig; see $ROOT/kcp.log" >&2
    exit 1
  fi
  if [ "$KCP_SECURE_PORT" != "0" ] && [ "$bound" != "$KCP_SECURE_PORT" ]; then
    echo "kcp was asked for port $KCP_SECURE_PORT and holds $bound" >&2
    exit 1
  fi
  echo "$bound" >"$ROOT/kcp.port"
fi

for _ in $(seq 1 360); do kcp_ready && break; sleep 0.5; done
if ! kcp_ready; then
  echo "kcp did not become ready; see $ROOT/kcp.log" >&2
  exit 1
fi

echo "kcp ready"
echo "kcp port: $(cat "$ROOT/kcp.port")"
echo "kine endpoint: $KINE_ENDPOINT"
echo "kubeconfig: $ROOT/admin.kubeconfig"
