import type { UserDataModule, UserDataPatch } from "@publicdomainrelay/cloud-init-common";
import {
  K3S_JOIN_GO,
  K3S_JOIN_PATH,
  K3S_JOIN_READY_MARKER,
  K3S_SETUP_STAMP,
  NODE_JOIN_KEYS,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

export const RESOLV_CONF_PATH = "/etc/resolv.conf";

export const DEFAULT_DNS_GATEWAY = "172.30.0.1";

export const K3S_JOIN_SSHD_DROPIN = "/etc/ssh/sshd_config.d/20-socialweb-k3s-join.conf";

export interface K3sJoinModuleOptions {
  setupWaitSec?: number;
  dnsNameservers?: string[];
  k3sDataDir?: string;
  setupStamp?: string;
  tokenPath?: string;
  nodePasswordPath?: string;
}

export function renderK3sJoinScript(opts: K3sJoinModuleOptions = {}): string {
  const setupWaitSec = opts.setupWaitSec ?? 900;
  const dataDir = opts.k3sDataDir ?? "/var/lib/rancher/k3s";
  const stamp = opts.setupStamp ?? K3S_SETUP_STAMP;
  const tokenPath = opts.tokenPath ?? "/kcp/token";
  const passwordPath = opts.nodePasswordPath ?? "/etc/rancher/node/password";
  const keys = NODE_JOIN_KEYS.join(" ");
  return `#!/bin/bash
set -euo pipefail
declare -A V
while IFS='=' read -r key value; do
  [ -z "\${key}" ] && break
  case " ${keys} " in
    *" \${key} "*) V[\${key}]=$(printf '%s' "\${value}" | base64 -d) ;;
    *) echo "socialweb-k3s-join: unknown key \${key}" >&2; exit 64 ;;
  esac
done
for k in ${keys}; do
  [ -n "\${V[\${k}]:-}" ] || { echo "socialweb-k3s-join: missing \${k}" >&2; exit 64; }
done
for _ in $(seq 1 ${setupWaitSec}); do
  [ -f ${stamp} ] && command -v k3s >/dev/null && break
  sleep 1
done
command -v k3s >/dev/null || { echo "socialweb-k3s-join: k3s was not installed within ${setupWaitSec}s" >&2; exit 69; }
mkdir -p "$(dirname ${tokenPath})" "$(dirname ${passwordPath})"
umask 077
printf '%s' "\${V[TOKEN]}" > ${tokenPath}
printf '%s' "\${V[NODE_PASSWORD]}" > ${passwordPath}
umask 022
ip -4 addr show dev lo | grep -q " \${V[ADVERTISE]}/32 " || ip addr add "\${V[ADVERTISE]}/32" dev lo
echo "${K3S_JOIN_READY_MARKER}"
IFS= read -r go || true
[ "\${go}" = "${K3S_JOIN_GO}" ] || { echo "socialweb-k3s-join: no go-ahead" >&2; exit 65; }
exec k3s agent --server "\${V[SERVER_URL]}" --token-file ${tokenPath} --node-name "\${V[NODE_NAME]}" --data-dir ${dataDir}
`;
}

export function renderResolver(nameservers: string[]): string {
  return `${nameservers.map((n) => `nameserver ${n}`).join("\n")}\noptions timeout:2 attempts:3\n`;
}

export function createK3sJoinModule(opts: K3sJoinModuleOptions = {}): UserDataModule {
  const nameservers = opts.dnsNameservers ?? [DEFAULT_DNS_GATEWAY];
  const resolver = renderResolver(nameservers);
  return (): UserDataPatch => ({
    write_files: [
      {
        path: RESOLV_CONF_PATH,
        owner: "root:root",
        permissions: "0644",
        content: resolver,
      },
      {
        path: K3S_JOIN_SSHD_DROPIN,
        owner: "root:root",
        permissions: "0644",
        content: "AllowTcpForwarding remote\nGatewayPorts clientspecified\n",
      },
      {
        path: K3S_JOIN_PATH,
        owner: "root:root",
        permissions: "0755",
        content: renderK3sJoinScript(opts),
      },
    ],
    bootcmd: [
      `printf '%s' '${resolver.replace(/'/g, `'\\''`)}' > ${RESOLV_CONF_PATH}`,
    ],
    runcmd: [
      `printf '%s' '${resolver.replace(/'/g, `'\\''`)}' > ${RESOLV_CONF_PATH}`,
      "systemctl reload ssh || systemctl reload sshd || true",
    ],
  });
}
