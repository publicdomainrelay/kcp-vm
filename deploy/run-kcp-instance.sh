#!/bin/bash
set -euo pipefail

BIN="${KCP_BIN:-/usr/local/bin/kcp}"

INSTANCE="${KCP_INSTANCE:?set KCP_INSTANCE to a unique name for this instance, e.g. the worktree name}"
PORT="${KCP_SECURE_PORT:?set KCP_SECURE_PORT to a port this instance owns, or 0 to bind any free port and read it back from ROOT/admin.kubeconfig}"
KINE="${KCP_KINE_ENDPOINT:?set KCP_KINE_ENDPOINT to the kine this instance dials, e.g. http://127.0.0.1:<the port kine reported>; without it kcp starts an embedded etcd on the host-fixed 2379/2380, which no flag can move}"

ROOT="${KCP_ROOT_DIR:-$PWD/.kcp-$INSTANCE}"
BIND="${KCP_BIND_ADDRESS:-127.0.0.1}"
GATES="${KCP_FEATURE_GATES:-WorkspaceMounts=true}"
POLICY="${KCP_AUDIT_POLICY:-$ROOT/audit-policy.yaml}"
AUDIT_LOG="${KCP_AUDIT_LOG:-$ROOT/audit.log}"

command -v "$BIN" >/dev/null 2>&1 || {
  echo >&2 "no kcp at $BIN -- the one-container deployment carries its own kcp"
  exit 1
}

case "$PORT" in
  ''|*[!0-9]*) echo >&2 "KCP_SECURE_PORT must be a number, got '$PORT'"; exit 1 ;;
esac
[ "$PORT" -le 65535 ] || { echo >&2 "KCP_SECURE_PORT=$PORT is above 65535"; exit 1; }

SRC="$(dirname "$0")/kcp-audit-policy.yaml"
[ -r "$SRC" ] || { echo >&2 "cannot read $SRC; an unarmed kcp audits nothing while looking armed"; exit 1; }

install -d -m 0700 "$ROOT"
install -m 0600 "$SRC" "$POLICY"

echo "instance      : $INSTANCE"
echo "root-directory: $ROOT"
PORT_ARGS=(--secure-port="$PORT")
if [ "$PORT" = "0" ]; then
  PORT_ARGS+=(--experimental-bind-free-port)
  echo "secure-port   : any free port (bind $BIND), named in $ROOT/admin.kubeconfig once bound"
else
  echo "secure-port   : $PORT  (bind $BIND)"
fi
echo "etcd-servers  : $KINE   (kine, not embedded -- 2379/2380 are host-fixed)"
echo "feature-gates : $GATES"
echo "audit policy  : $POLICY"
echo "audit log     : $AUDIT_LOG"
echo "kubeconfig    : $ROOT/admin.kubeconfig"
echo

exec "$BIN" start \
  --root-directory="$ROOT" \
  --etcd-servers="$KINE" \
  --bind-address="$BIND" \
  "${PORT_ARGS[@]}" \
  --feature-gates="$GATES" \
  --audit-policy-file="$POLICY" \
  --audit-log-path="$AUDIT_LOG"
