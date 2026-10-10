#!/bin/bash
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"

SSH_TARGET="${SSH_TARGET:-root@socialweb.computer}"
DOMAIN="${DOMAIN:-socialweb.computer}"
ORIGIN="${ORIGIN:-k8s.${DOMAIN}}"
KCP_ORIGIN="${KCP_ORIGIN:-kcp.${DOMAIN}}"
SERVICE_NAME="${SERVICE_NAME:-socialweb-k8s}"
IMAGE="${KCP_K8S_IMAGE:-socialweb-k8s:local}"
VOLUME="${SOCIALWEB_STATE_VOLUME:-socialweb-k8s-state}"
DOOR_PORT="${DOOR_PORT:-8788}"
KCP_PORT="${KCP_PORT:-6443}"
GATEWAY_TLS_PORT="${GATEWAY_TLS_PORT:-8443}"
INGRESS_PROXY_HOST="${INGRESS_PROXY_HOST:-xrpc.fedproxy.com}"
GATEWAY_AUDIENCE="${GATEWAY_AUDIENCE:-did:web:${ORIGIN}}"
KUBECONFIG_MAX_TTL="${KUBECONFIG_MAX_TTL:-720h}"
NODE_POLICY="${NODE_POLICY:-tangled-vouch}"
DEFAULT_NODE_POLICY_ARGS='{"firstFree":true}'
NODE_POLICY_ARGS="${NODE_POLICY_ARGS:-$DEFAULT_NODE_POLICY_ARGS}"
RELAY_URL="${RELAY_URL:-}"
MARKET_DENY_BIDDER_DIDS="${MARKET_DENY_BIDDER_DIDS:-did:plc:syysktwibqyqokny3pzht4rn}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SSH_PORT="${SSH_PORT:-1997}"
SSH_KEY="${SSH_KEY:-}"
SSH_OPTS=(-p "$SSH_PORT" -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=20
  -o ControlMaster=auto -o "ControlPath=${HOME}/.ssh/cm-swc-%r@%h:%p" -o ControlPersist=10m -o ServerAliveInterval=15)
if [ -n "$SSH_KEY" ]; then
  SSH_OPTS+=(-i "$SSH_KEY" -o IdentitiesOnly=yes)
fi

say() {
  echo "deploy: $*"
}

if [ "$SKIP_BUILD" != "1" ]; then
  say "building $IMAGE"
  KCP_K8S_IMAGE="$IMAGE" "$REPO/deploy/control-plane-container/build.sh" k8s
fi

say "making sure $SSH_TARGET has a container runtime"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'command -v docker >/dev/null || { export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -y -q docker.io && systemctl enable --now docker; }; docker version --format "{{.Server.Version}}"'

say "shipping $IMAGE to $SSH_TARGET"
docker save "$IMAGE" | gzip -1 | ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'gunzip | docker load'

q() {
  printf "%q" "$1"
}

REMOTE_ENV="SERVICE_NAME=$(q "$SERVICE_NAME") IMAGE=$(q "$IMAGE") VOLUME=$(q "$VOLUME") ORIGIN=$(q "$ORIGIN") KCP_ORIGIN=$(q "$KCP_ORIGIN") DOOR_PORT=$(q "$DOOR_PORT") KCP_PORT=$(q "$KCP_PORT") GATEWAY_TLS_PORT=$(q "$GATEWAY_TLS_PORT") INGRESS_PROXY_HOST=$(q "$INGRESS_PROXY_HOST") GATEWAY_AUDIENCE=$(q "$GATEWAY_AUDIENCE") KUBECONFIG_MAX_TTL=$(q "$KUBECONFIG_MAX_TTL") NODE_POLICY=$(q "$NODE_POLICY") NODE_POLICY_ARGS=$(q "$NODE_POLICY_ARGS") RELAY_URL=$(q "$RELAY_URL") MARKET_DENY_BIDDER_DIDS=$(q "$MARKET_DENY_BIDDER_DIDS")"

say "starting $SERVICE_NAME on $SSH_TARGET"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$REMOTE_ENV bash -s" <<'REMOTE_EOF'
set -euo pipefail

say() {
  echo "remote: $*"
}

command -v docker >/dev/null || { say "no docker on this host"; exit 1; }
command -v caddy >/dev/null || { say "no caddy on this host: the public names are served by its automatic HTTPS"; exit 1; }

ENV_DIR="/etc/${SERVICE_NAME}"
ENV_FILE="${ENV_DIR}/container.env"
install -d -m 0700 "$ENV_DIR"
umask 077
cat > "$ENV_FILE" <<ENV
PUBLIC_ORIGIN=https://${ORIGIN}
SOCIALWEB_CONTAINER_BOOTSTRAP=0
SOCIALWEB_CONTAINER_KCP_BIND=0.0.0.0
SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE=market
SOCIALWEB_CLUSTERPROVIDER_MARKET_DENY_BIDDER_DIDS=${MARKET_DENY_BIDDER_DIDS}
SOCIALWEB_GATEWAY_AUDIENCES=${GATEWAY_AUDIENCE}
SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE=${GATEWAY_AUDIENCE}
SOCIALWEB_GATEWAY_DEVICE_KEY_MAX_TTL=${KUBECONFIG_MAX_TTL}
SOCIALWEB_GATEWAY_KCP_PUBLIC_HOST=https://${KCP_ORIGIN}
SOCIALWEB_GATEWAY_PUBLIC_MINT_URL=https://${ORIGIN}/v1/credential
INGRESS_PROXY_HOST=${INGRESS_PROXY_HOST}
NODE_POLICY=${NODE_POLICY}
NODE_POLICY_ARGS=${NODE_POLICY_ARGS}
RELAY_URL=${RELAY_URL}
ENV
chmod 0600 "$ENV_FILE"

docker volume inspect "$VOLUME" >/dev/null 2>&1 || docker volume create "$VOLUME" >/dev/null
docker rm -f "$SERVICE_NAME" >/dev/null 2>&1 || true
docker run -d --name "$SERVICE_NAME" --restart unless-stopped \
  --cgroup-parent system.slice \
  -v "$VOLUME:/var/lib/socialweb" \
  --env-file "$ENV_FILE" \
  -p "127.0.0.1:${DOOR_PORT}:8788" \
  -p "127.0.0.1:${KCP_PORT}:6443" \
  -p "127.0.0.1:${GATEWAY_TLS_PORT}:8443" \
  "$IMAGE" >/dev/null

say "waiting for the door to answer"
ready=0
for _ in $(seq 1 120); do
  if curl -fsS "http://127.0.0.1:${DOOR_PORT}/api/config" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! docker inspect -f '{{.State.Running}}' "$SERVICE_NAME" 2>/dev/null | grep -q true; then
    break
  fi
  sleep 2
done
if [ "$ready" != "1" ]; then
  say "the door never answered; last log lines:"
  docker logs --tail 60 "$SERVICE_NAME" 2>&1 || true
  exit 1
fi
say "the door answers"

CA_DIR="/etc/caddy/${SERVICE_NAME}"
install -d -m 0755 "$CA_DIR"
docker exec "$SERVICE_NAME" cat /var/lib/socialweb/kcp/apiserver.crt > "$CA_DIR/kcp.crt"
docker exec "$SERVICE_NAME" cat /var/lib/socialweb/tls/tls.crt > "$CA_DIR/gateway.crt"
chmod 0644 "$CA_DIR"/*.crt

install -d -m 0755 /etc/caddy/sites
cat > "/etc/caddy/sites/${ORIGIN}.caddy" <<SITE
${ORIGIN} {
  handle /v1/credential {
    reverse_proxy https://127.0.0.1:${GATEWAY_TLS_PORT} {
      transport http {
        tls_trusted_ca_certs ${CA_DIR}/gateway.crt
        tls_server_name 127.0.0.1
      }
    }
  }
  handle {
    reverse_proxy http://127.0.0.1:${DOOR_PORT}
  }
}
SITE
cat > "/etc/caddy/sites/${KCP_ORIGIN}.caddy" <<SITE
${KCP_ORIGIN} {
  reverse_proxy https://127.0.0.1:${KCP_PORT} {
    flush_interval -1
    transport http {
      tls_trusted_ca_certs ${CA_DIR}/kcp.crt
      tls_server_name localhost
    }
  }
}
SITE
if ! grep -qE '^[[:space:]]*import[[:space:]]+/etc/caddy/sites/\*\.caddy' /etc/caddy/Caddyfile; then
  cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak.$(date +%s)"
  printf '\nimport /etc/caddy/sites/*.caddy\n' >> /etc/caddy/Caddyfile
fi
caddy validate --config /etc/caddy/Caddyfile >/dev/null
systemctl reload caddy || systemctl restart caddy

say "https://${ORIGIN} -> door, https://${KCP_ORIGIN} -> kcp front"
REMOTE_EOF

say "done: https://${ORIGIN} and https://${KCP_ORIGIN}"
