#!/usr/bin/env bash
set -euo pipefail

# Starts the OpenBao the provider issues workload certificates from, in
# development mode: in-memory storage, unsealed, and a root token on the command
# line. That is enough for the demo -- the namespaces, the intermediates and the
# leaves under them are what is being shown -- and it is why a restart empties
# the hierarchy and every workload's certificate has to be reissued.
#
# The binary is the bao on PATH if there is one, and otherwise a build of the
# checkout pinned in third_party/openbao, which takes a few minutes the first
# time. Set BAO_BIN to choose one explicitly.

REPO=$(cd "$(dirname "$0")/.." && pwd)
DIR=${OPENBAO_DIR:-$REPO/.kcp-openbao}
ADDR=${OPENBAO_LISTEN:-127.0.0.1:8200}
TOKEN=${OPENBAO_TOKEN:-root}
LOG=$DIR/bao.log
PIDFILE=$DIR/bao.pid

mkdir -p "$DIR"

if [ -s "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "openbao is already running on $ADDR (pid $(cat "$PIDFILE"))"
  exit 0
fi

BIN=${BAO_BIN:-}
if [ -z "$BIN" ]; then
  if command -v bao >/dev/null 2>&1; then
    BIN=$(command -v bao)
  else
    BIN=$DIR/bao
    if [ ! -f "$REPO/third_party/openbao/go.mod" ]; then
      echo "no bao on PATH and third_party/openbao is empty: run" >&2
      echo "  git submodule update --init third_party/openbao" >&2
      echo "or point BAO_BIN at a bao." >&2
      exit 1
    fi
    echo "no bao on PATH: building the pinned checkout into $BIN (this takes a few minutes)"
    (cd "$REPO/third_party/openbao" && go build -o "$BIN" .)
  fi
fi

nohup "$BIN" server -dev \
  -dev-root-token-id="$TOKEN" \
  -dev-listen-address="$ADDR" \
  -log-level=warn >"$LOG" 2>&1 &
echo $! >"$PIDFILE"
echo "started $BIN (pid $(cat "$PIDFILE")), log $LOG"

for _ in $(seq 1 120); do
  if "$BIN" status -address="http://$ADDR" >/dev/null 2>&1; then
    echo "openbao is up at http://$ADDR"
    echo
    echo "start the provider against it with:"
    echo "  --openbao-addr http://$ADDR --openbao-token $TOKEN"
    exit 0
  fi
  sleep 1
done

echo "openbao did not answer on http://$ADDR within 120s; see $LOG" >&2
exit 1
