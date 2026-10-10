import type { UserDataModule, UserDataPatch, WriteFileEntry } from "@publicdomainrelay/cloud-init-common";
import {
  NODE_CLAIM_DOC_PATH,
  NODE_SECRETS_DIR,
  OPENBAO_CA_PATH,
  OPENBAO_LINK_LOCAL_ADDR,
  OPENBAO_TICKET_PATH,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

export const RESOLV_CONF_PATH = "/etc/resolv.conf";

export const DEFAULT_DNS_GATEWAY = "172.30.0.1";

export const DEFAULT_ACCEPT_PATH = "/root/secrets/publicdomainrelay.com/market/accept.json";

export const NODE_UNITS = {
  openbaoLink: "socialweb-openbao-link.service",
  nodeSecrets: "socialweb-node-secrets.service",
  apiserverLink: "socialweb-apiserver-link.service",
  k3sAgent: "socialweb-k3s-agent.service",
} as const;

export const NODE_UNIT_ORDER = [
  NODE_UNITS.openbaoLink,
  NODE_UNITS.nodeSecrets,
  NODE_UNITS.apiserverLink,
  NODE_UNITS.k3sAgent,
] as const;

export const SECRETS_FETCH_UNIT = "setup-secrets.service";

export const NODE_SCRIPTS = {
  openbaoLink: "/usr/local/bin/socialweb-openbao-link",
  nodeSecrets: "/usr/local/bin/socialweb-node-secrets",
  apiserverLink: "/usr/local/bin/socialweb-apiserver-link",
} as const;

export const NODE_FILES = {
  token: `${NODE_SECRETS_DIR}/token`,
  nodePassword: `${NODE_SECRETS_DIR}/node-password`,
  serverCA: `${NODE_SECRETS_DIR}/server-ca.pem`,
  linkEnv: `${NODE_SECRETS_DIR}/apiserver.env`,
  done: "/var/lib/socialweb-node-secrets.done",
} as const;

export const K3S_NODE_PASSWORD_PATH = "/etc/rancher/node/password";

export const NODE_LOCAL_DNS_ADDRESS = "169.254.20.10";
export const NODE_LOCAL_DNS_LABEL = "socialweb.computer/node-local-dns";
export const NODE_LOCAL_DNS_LINK = "swdns0";

export const POD_CLUSTER_CIDR = "10.42.0.0/16";
export const POD_MTU = 1160;
export const POD_BRIDGE = "cni0";
export const POD_EGRESS_COMMENT = "socialweb-pod-egress";
export const CNI_CONFLIST_NAME = "10-socialweb-bridge.conflist";
export const CNI_WAIT_ATTEMPTS = 90;

export const DIAGNOSE_ATTEMPTS = 30;

export const CNI_CONF_DIR = "/etc/cni/net.d";

export const CNI_BIN_DIR = "/opt/cni/bin";

export const CNI_PLUGINS = ["bridge", "host-local", "portmap", "loopback"] as const;

export const MESHD_PATH = "/usr/local/bin/socialweb-meshd";
export const MESHD_ENV_PATH = `${NODE_SECRETS_DIR}/meshd.env`;
export const MESHD_ARCHES = ["x86_64", "aarch64"] as const;
export type MeshdArch = typeof MESHD_ARCHES[number];

export const SYSCTL_PATH = "/etc/sysctl.d/90-socialweb-node.conf";

export const NODE_NET_UNITS = {
  dnsLink: "socialweb-node-dns-link.service",
  masquerade: "socialweb-pod-masquerade.service",
  cni: "socialweb-pod-cni.service",
  meshdInstall: "socialweb-meshd-install.service",
  meshd: "socialweb-meshd.service",
  diagnose: "socialweb-node-diagnose.service",
} as const;

export const NODE_NET_SCRIPTS = {
  dnsLink: "/usr/local/bin/socialweb-node-dns-link",
  masquerade: "/usr/local/bin/socialweb-pod-masquerade",
  cni: "/usr/local/bin/socialweb-pod-cni",
  meshdInstall: "/usr/local/bin/socialweb-meshd-install",
  diagnose: "/usr/local/bin/socialweb-node-diagnose",
} as const;

export const MESHD_UNIT = `[Unit]
Description=socialweb.computer pod mesh over iroh (socialweb-meshd)
Wants=network-online.target
After=network-online.target

[Service]
Type=notify
NotifyAccess=main
TimeoutStartSec=infinity
EnvironmentFile=/etc/socialweb/node/meshd.env
ExecStart=/usr/local/bin/socialweb-meshd run --kubeconfig /var/lib/rancher/k3s/agent/kubelet.kubeconfig --secret-file /var/lib/socialweb/meshd/secret
Restart=always
RestartSec=2
StateDirectory=socialweb/meshd
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
`;

export const MESHD_UNIT_DROP_IN = `[Unit]
After=${NODE_NET_UNITS.meshdInstall} ${NODE_UNITS.nodeSecrets}
Wants=${NODE_NET_UNITS.meshdInstall} ${NODE_UNITS.nodeSecrets}
StartLimitIntervalSec=0
`;

export const MESHD_DROP_IN_PATH = `/etc/systemd/system/${NODE_NET_UNITS.meshd}.d/10-socialweb-node.conf`;

export interface MeshdPin {
  downloadBase: string;
  sha256: Record<MeshdArch, string>;
}

export interface K3sJoinModuleOptions {
  dnsNameservers?: string[];
  k3sDataDir?: string;
  acceptPath?: string;
  dumbpipePath?: string;
  k3sPath?: string;
  nodeDir?: string;
  k3sNodePasswordPath?: string;
  doneStamp?: string;
  openbaoAddr?: string;
  meshd?: MeshdPin;
  meshdPath?: string;
  cniConfDir?: string;
  cniBinDir?: string;
  kubeletKubeconfig?: string;
  cniWaitAttempts?: number;
  diagnoseAttempts?: number;
}

const SHA256_HEX = /^[0-9a-f]{64}$/;
const HTTP_ORIGIN = /^https?:\/\/[A-Za-z0-9.-]+(:[0-9]{1,5})?$/;

export function validateMeshdPin(pin: MeshdPin): MeshdPin {
  if (!HTTP_ORIGIN.test(pin.downloadBase)) {
    throw new Error(`meshd downloadBase ${JSON.stringify(pin.downloadBase)} is not an http(s) origin with no path`);
  }
  for (const arch of MESHD_ARCHES) {
    if (!SHA256_HEX.test(pin.sha256?.[arch] ?? "")) {
      throw new Error(`meshd sha256 for ${arch} is not 64 lowercase hex characters`);
    }
  }
  return pin;
}

interface ResolvedPaths {
  acceptPath: string;
  dumbpipe: string;
  k3s: string;
  dir: string;
  claim: string;
  ca: string;
  ticket: string;
  token: string;
  nodePassword: string;
  serverCA: string;
  linkEnv: string;
  k3sNodePassword: string;
  done: string;
  openbaoAddr: string;
  dataDir: string;
  meshdEnv: string;
  meshd: string;
  cniConfDir: string;
  cniBinDir: string;
  cniPlugin: string;
  kubeletKubeconfig: string;
  cniWaitAttempts: number;
  diagnoseAttempts: number;
}

function resolvePaths(opts: K3sJoinModuleOptions): ResolvedPaths {
  const dir = opts.nodeDir ?? NODE_SECRETS_DIR;
  const inDir = (canonical: string) => `${dir}${canonical.slice(NODE_SECRETS_DIR.length)}`;
  const dataDir = opts.k3sDataDir ?? "/var/lib/rancher/k3s";
  return {
    meshdEnv: inDir(MESHD_ENV_PATH),
    meshd: opts.meshdPath ?? MESHD_PATH,
    cniConfDir: opts.cniConfDir ?? CNI_CONF_DIR,
    cniBinDir: opts.cniBinDir ?? CNI_BIN_DIR,
    cniPlugin: `${dataDir}/data/current/bin/cni`,
    kubeletKubeconfig: opts.kubeletKubeconfig ?? `${dataDir}/agent/kubelet.kubeconfig`,
    cniWaitAttempts: opts.cniWaitAttempts ?? CNI_WAIT_ATTEMPTS,
    diagnoseAttempts: opts.diagnoseAttempts ?? DIAGNOSE_ATTEMPTS,
    acceptPath: opts.acceptPath ?? DEFAULT_ACCEPT_PATH,
    dumbpipe: opts.dumbpipePath ?? "/usr/local/bin/dumbpipe",
    k3s: opts.k3sPath ?? "/usr/local/bin/k3s",
    dir,
    claim: inDir(NODE_CLAIM_DOC_PATH),
    ca: inDir(OPENBAO_CA_PATH),
    ticket: inDir(OPENBAO_TICKET_PATH),
    token: inDir(NODE_FILES.token),
    nodePassword: inDir(NODE_FILES.nodePassword),
    serverCA: inDir(NODE_FILES.serverCA),
    linkEnv: inDir(NODE_FILES.linkEnv),
    k3sNodePassword: opts.k3sNodePasswordPath ?? K3S_NODE_PASSWORD_PATH,
    done: opts.doneStamp ?? NODE_FILES.done,
    openbaoAddr: opts.openbaoAddr ?? OPENBAO_LINK_LOCAL_ADDR,
    dataDir,
  };
}

export function renderNodeSecretsScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -euo pipefail
say() { echo "socialweb-node-secrets: $*" >&2; }
[ -f ${p.done} ] && [ -s ${p.linkEnv} ] && [ -s ${p.meshdEnv} ] && exit 0
for f in ${p.claim} ${p.ca} ${p.acceptPath}; do
  [ -s "\${f}" ] || { say "\${f} is missing"; exit 1; }
done
field() { jq -er --arg k "$1" '.[$k] | strings | select(length > 0)' ${p.claim}; }
NODE="$(field node)"
JWT_MOUNT="$(field jwtMount)"
ROLE="$(field role)"
AUDIENCE="$(field audience)"
KV_MOUNT="$(field kvMount)"
KV_PATH="$(field kvPath)"
for v in "\${NODE}" "\${JWT_MOUNT}" "\${ROLE}" "\${KV_MOUNT}" "\${KV_PATH}"; do
  printf '%s' "\${v}" | grep -Eqx '[a-z0-9]([a-z0-9.-]*[a-z0-9])?' || { say "claim.json carries an unsafe name"; exit 1; }
done

WIF="$(jq -c '.bid_config.value // .bid_config // {}' ${p.acceptPath})"
TOKEN_PATH="$(printf '%s' "\${WIF}" | jq -r '.token_path // empty')"
URL_PATH="$(printf '%s' "\${WIF}" | jq -r '.url_path // empty')"
URL_ROUTE="$(printf '%s' "\${WIF}" | jq -r '.url_route // "/v1/oidc/issue"')"
[ -n "\${TOKEN_PATH}" ] && [ -s "\${TOKEN_PATH}" ] || { say "no workload identity token"; exit 1; }
[ -n "\${URL_PATH}" ] && [ -s "\${URL_PATH}" ] || { say "no workload identity issuer"; exit 1; }
WID_TOKEN="$(cat "\${TOKEN_PATH}")"
ISSUER_BASE="$(cat "\${URL_PATH}")"
PAYLOAD="$(printf '%s' "\${WID_TOKEN}" | cut -d. -f2 | tr '_-' '/+')"
case $(( \${#PAYLOAD} % 4 )) in
  2) PAYLOAD="\${PAYLOAD}==" ;;
  3) PAYLOAD="\${PAYLOAD}=" ;;
esac
SUBJECT="$(printf '%s' "\${PAYLOAD}" | base64 -d | jq -r .sub)" || true
[ -n "\${SUBJECT}" ] && [ "\${SUBJECT}" != "null" ] || { say "could not read sub from the workload identity token"; exit 1; }

EXCHANGED="$(curl -sf -H "Authorization: Bearer \${WID_TOKEN}" -H "Content-Type: application/json" \\
  -d "$(jq -nc --arg aud "\${AUDIENCE}" --arg sub "\${SUBJECT}" '{aud: $aud, sub: $sub, ttl: 300}')" \\
  "\${ISSUER_BASE}\${URL_ROUTE}" | jq -r '.token // empty')" || true
[ -n "\${EXCHANGED}" ] || { say "token exchange for \${AUDIENCE} failed"; exit 1; }

BAO="https://${p.openbaoAddr}"
BAO_TOKEN="$(curl -sf --cacert ${p.ca} -H "Content-Type: application/json" \\
  -d "$(jq -nc --arg role "\${ROLE}" --arg jwt "\${EXCHANGED}" '{role: $role, jwt: $jwt}')" \\
  "\${BAO}/v1/auth/\${JWT_MOUNT}/login" | jq -r '.auth.client_token // empty')" || true
[ -n "\${BAO_TOKEN}" ] || { say "OpenBao login at auth/\${JWT_MOUNT} as \${ROLE} failed"; exit 1; }

DATA="$(curl -sf --cacert ${p.ca} -H "X-Vault-Token: \${BAO_TOKEN}" "\${BAO}/v1/\${KV_MOUNT}/data/\${KV_PATH}" | jq -c '.data.data // empty')" || true
[ -n "\${DATA}" ] || { say "reading \${KV_MOUNT}/\${KV_PATH} failed"; exit 1; }
need() { printf '%s' "\${DATA}" | jq -er --arg k "$1" '.[$k] | if type == "number" then tostring else strings end | select(length > 0)' >/dev/null || { say "\${KV_MOUNT}/\${KV_PATH} has no $1"; exit 1; }; }
for k in token nodePassword serverCA apiserverTicket advertiseAddress apiserverPort; do need "\${k}"; done
ADVERTISE="$(printf '%s' "\${DATA}" | jq -r '.advertiseAddress')"
PORT="$(printf '%s' "\${DATA}" | jq -r '.apiserverPort | tostring')"
TICKET="$(printf '%s' "\${DATA}" | jq -r '.apiserverTicket')"
printf '%s' "\${ADVERTISE}" | grep -Eqx '([0-9]{1,3}\\.){3}[0-9]{1,3}' || { say "advertiseAddress is not an IPv4 address"; exit 1; }
printf '%s' "\${PORT}" | grep -Eqx '[0-9]{1,5}' || { say "apiserverPort is not a port"; exit 1; }
printf '%s' "\${TICKET}" | grep -Eqx '[a-z0-9]+' || { say "apiserverTicket is not a ticket"; exit 1; }

umask 077
install -d -m 0700 ${p.dir} "$(dirname ${p.k3sNodePassword})"
printf '%s' "\${DATA}" | jq -j '.serverCA' > ${p.serverCA}
CA_HASH="$(sha256sum ${p.serverCA} | cut -d' ' -f1)"
JOIN="$(printf '%s' "\${DATA}" | jq -r '.token' | tr -d '[:space:]')"
case "\${JOIN}" in
  K10*) ;;
  *) JOIN="K10\${CA_HASH}::server:\${JOIN}" ;;
esac
printf '%s' "\${JOIN}" > ${p.token}
printf '%s' "\${DATA}" | jq -j '.nodePassword' | tr -d '\\n' > ${p.nodePassword}
install -m 0600 ${p.nodePassword} ${p.k3sNodePassword}
printf 'NODE_NAME=%s\\nADVERTISE_ADDRESS=%s\\nAPISERVER_PORT=%s\\nAPISERVER_TICKET=%s\\n' \\
  "\${NODE}" "\${ADVERTISE}" "\${PORT}" "\${TICKET}" > ${p.linkEnv}.tmp
mv ${p.linkEnv}.tmp ${p.linkEnv}
printf 'SOCIALWEB_MESHD_NODE_NAME=%s\\nSOCIALWEB_MESHD_KUBECONFIG=%s\\n' "\${NODE}" ${p.kubeletKubeconfig} > ${p.meshdEnv}.tmp
mv ${p.meshdEnv}.tmp ${p.meshdEnv}
chmod 0600 ${p.serverCA} ${p.token} ${p.nodePassword} ${p.k3sNodePassword} ${p.linkEnv} ${p.meshdEnv}
touch ${p.done}
say "fetched the join secrets for \${NODE}"
`;
}

export function renderOpenBaoLinkScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -euo pipefail
[ -s ${p.ticket} ] || { echo "socialweb-openbao-link: ${p.ticket} is missing" >&2; exit 1; }
[ -x ${p.dumbpipe} ] || { echo "socialweb-openbao-link: ${p.dumbpipe} is not installed" >&2; exit 1; }
TICKET="$(tr -d '[:space:]' < ${p.ticket})"
exec ${p.dumbpipe} connect-tcp --addr ${p.openbaoAddr} "\${TICKET}"
`;
}

export function renderApiserverLinkScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -euo pipefail
[ -x ${p.dumbpipe} ] || { echo "socialweb-apiserver-link: ${p.dumbpipe} is not installed" >&2; exit 1; }
: "\${ADVERTISE_ADDRESS:?}" "\${APISERVER_PORT:?}" "\${APISERVER_TICKET:?}"
ip -4 addr show dev lo | grep -q " \${ADVERTISE_ADDRESS}/32 " || ip addr add "\${ADVERTISE_ADDRESS}/32" dev lo
exec ${p.dumbpipe} connect-tcp --addr "\${ADVERTISE_ADDRESS}:\${APISERVER_PORT}" "\${APISERVER_TICKET}"
`;
}

export function renderCniConflist(subnet: string): string {
  return JSON.stringify(
    {
      cniVersion: "1.0.0",
      name: "socialweb-pods",
      plugins: [
        {
          type: "bridge",
          bridge: POD_BRIDGE,
          isGateway: true,
          ipMasq: false,
          hairpinMode: true,
          mtu: POD_MTU,
          ipam: {
            type: "host-local",
            ranges: [[{ subnet }]],
            routes: [{ dst: "0.0.0.0/0" }],
          },
        },
        { type: "portmap", capabilities: { portMappings: true } },
      ],
    },
    null,
    2,
  ) + "\n";
}

export function renderPodCniScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  const conf = `${p.cniConfDir}/${CNI_CONFLIST_NAME}`;
  return `#!/bin/bash
set -euo pipefail
say() { echo "socialweb-pod-cni: $*" >&2; }
: "\${NODE_NAME:?}"
printf '%s' "\${NODE_NAME}" | grep -Eqx '[a-z0-9]([a-z0-9.-]*[a-z0-9])?' || { say "NODE_NAME is not a node name"; exit 1; }
CIDR=""
for _ in $(seq 1 ${p.cniWaitAttempts}); do
  if [ -s ${p.kubeletKubeconfig} ]; then
    CIDR="$(${p.k3s} kubectl --kubeconfig ${p.kubeletKubeconfig} get node "\${NODE_NAME}" -o 'jsonpath={.spec.podCIDR}' 2>/dev/null || true)"
    [ -n "\${CIDR}" ] && break
  fi
  sleep 2
done
[ -n "\${CIDR}" ] || { say "node \${NODE_NAME} has no podCIDR yet"; exit 1; }
printf '%s' "\${CIDR}" | grep -Eqx '([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}' || { say "podCIDR \${CIDR} is not an IPv4 CIDR"; exit 1; }
install -d -m 0755 ${p.cniConfDir} ${p.cniBinDir}
for PLUGIN in ${CNI_PLUGINS.join(" ")}; do
  ln -sfn ${p.cniPlugin} ${p.cniBinDir}/"\${PLUGIN}"
done
printf '${renderCniConflist("%s")}' "\${CIDR}" > ${conf}.tmp
mv ${conf}.tmp ${conf}
say "wrote ${conf} for \${CIDR}"
`;
}

export function renderNodeDiagnoseScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -uo pipefail
say() { echo "socialweb-node-diagnose: $*" >&2; }
for _ in $(seq 1 ${p.diagnoseAttempts}); do
  INFO="$(${p.k3s} crictl info 2>/dev/null)"
  READY="$(printf '%s' "\${INFO}" | jq -r '[.status.conditions[]? | select(.type=="NetworkReady") | .status | tostring][0] // "unknown"' 2>/dev/null)"
  if [ "\${READY}" = "true" ]; then
    say "NetworkReady=true"
    exit 0
  fi
  REASON="$(printf '%s' "\${INFO}" | jq -r '[.status.conditions[]? | select(.type=="NetworkReady") | "\\(.reason // "") \\(.message // "")"][0] // ""' 2>/dev/null)"
  LOAD="$(printf '%s' "\${INFO}" | jq -r '.lastCNILoadStatus // ""' 2>/dev/null)"
  CONF="$(printf '%s' "\${INFO}" | jq -r '.config.cni.confDir // ""' 2>/dev/null)"
  BINS="$(printf '%s' "\${INFO}" | jq -r '(.config.cni.binDirs // [.config.cni.binDir // empty]) | join(",")' 2>/dev/null)"
  say "NetworkReady=\${READY} \${REASON}"
  say "lastCNILoadStatus=\${LOAD}"
  say "confDir=\${CONF} holds: $(ls -A "\${CONF}" 2>&1 | tr '\\n' ' ')"
  for B in \${BINS//,/ }; do
    say "binDirs=\${BINS} \${B} holds: $(ls -A "\${B}" 2>&1 | tr '\\n' ' ')"
  done
  sleep 20
done
say "the node network was still not ready after ${p.diagnoseAttempts} readings"
exit 0
`;
}

export function renderPodMasqueradeScript(): string {
  return `#!/bin/bash
set -euo pipefail
sysctl -qw net.ipv4.ip_forward=1
RULE=(-s ${POD_CLUSTER_CIDR} ! -d ${POD_CLUSTER_CIDR} -m comment --comment ${POD_EGRESS_COMMENT} -j MASQUERADE)
iptables -w -t nat -C POSTROUTING "\${RULE[@]}" 2>/dev/null || iptables -w -t nat -A POSTROUTING "\${RULE[@]}"
`;
}

export function renderDnsLinkScript(): string {
  return `#!/bin/bash
set -euo pipefail
modprobe dummy 2>/dev/null || true
ip link show ${NODE_LOCAL_DNS_LINK} >/dev/null 2>&1 || ip link add ${NODE_LOCAL_DNS_LINK} type dummy
ADDRS="$(ip -4 addr show dev ${NODE_LOCAL_DNS_LINK})"
case "\${ADDRS}" in
  *" ${NODE_LOCAL_DNS_ADDRESS}/32 "*) ;;
  *) ip addr add ${NODE_LOCAL_DNS_ADDRESS}/32 dev ${NODE_LOCAL_DNS_LINK} ;;
esac
ip link set ${NODE_LOCAL_DNS_LINK} up
`;
}

export function renderMeshdInstallScript(opts: K3sJoinModuleOptions): string {
  if (!opts.meshd) throw new Error("renderMeshdInstallScript needs a meshd pin");
  const pin = validateMeshdPin(opts.meshd);
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -euo pipefail
say() { echo "socialweb-meshd-install: $*" >&2; }
BIN=${p.meshd}
case "$(uname -m)" in
  x86_64|amd64) ARCH=x86_64; WANT=${pin.sha256.x86_64} ;;
  aarch64|arm64) ARCH=aarch64; WANT=${pin.sha256.aarch64} ;;
  *) say "no socialweb-meshd build for $(uname -m)"; exit 1 ;;
esac
if [ -x "\${BIN}" ] && [ "$(sha256sum "\${BIN}" | cut -d' ' -f1)" = "\${WANT}" ]; then
  say "socialweb-meshd \${WANT} is already installed"
  exit 0
fi
TMP="$(mktemp "\${BIN}.download.XXXXXX")"
trap 'rm -f "\${TMP}"' EXIT
curl -fsSL --retry 5 --retry-delay 2 -o "\${TMP}" "${pin.downloadBase}/downloads/\${WANT}/socialweb-meshd-\${ARCH}"
GOT="$(sha256sum "\${TMP}" | cut -d' ' -f1)"
[ "\${GOT}" = "\${WANT}" ] || { say "the door served sha256 \${GOT}, not the pinned \${WANT}"; exit 1; }
chmod 0755 "\${TMP}"
mv -f "\${TMP}" "\${BIN}"
trap - EXIT
say "installed socialweb-meshd \${WANT} for \${ARCH} from ${pin.downloadBase}"
`;
}

const CONSOLE = ["StandardOutput=journal+console", "StandardError=journal+console"];

function unit(sections: Record<string, string[]>): string {
  return Object.entries(sections).map(([name, lines]) => [`[${name}]`, ...lines].join("\n")).join("\n\n") + "\n";
}

export function renderNodeUnits(opts: K3sJoinModuleOptions = {}): Record<string, string> {
  const p = resolvePaths(opts);
  const install = ["WantedBy=multi-user.target"];
  const always = ["Restart=always", "RestartSec=5"];
  return {
    [NODE_UNITS.openbaoLink]: unit({
      Unit: [
        "Description=socialweb node: dumbpipe to the cluster OpenBao",
        `After=network-online.target ${SECRETS_FETCH_UNIT}`,
        "Wants=network-online.target",
        "StartLimitIntervalSec=0",
      ],
      Service: ["Type=simple", `ExecStart=${NODE_SCRIPTS.openbaoLink}`, ...always, ...CONSOLE],
      Install: install,
    }),
    [NODE_UNITS.nodeSecrets]: unit({
      Unit: [
        "Description=socialweb node: fetch the join secrets from the cluster OpenBao",
        `After=network-online.target ${SECRETS_FETCH_UNIT} ${NODE_UNITS.openbaoLink}`,
        `Wants=network-online.target ${NODE_UNITS.openbaoLink}`,
        "StartLimitIntervalSec=0",
      ],
      Service: [
        "Type=oneshot",
        "RemainAfterExit=yes",
        `ExecStart=${NODE_SCRIPTS.nodeSecrets}`,
        "Restart=on-failure",
        "RestartSec=5",
        ...CONSOLE,
      ],
      Install: install,
    }),
    [NODE_UNITS.apiserverLink]: unit({
      Unit: [
        "Description=socialweb node: dumbpipe to the cluster apiserver on its advertise address",
        `After=network-online.target ${NODE_UNITS.nodeSecrets}`,
        `Wants=network-online.target ${NODE_UNITS.nodeSecrets}`,
        "StartLimitIntervalSec=0",
      ],
      Service: ["Type=simple", `EnvironmentFile=${p.linkEnv}`, `ExecStart=${NODE_SCRIPTS.apiserverLink}`, ...always, ...CONSOLE],
      Install: install,
    }),
    [NODE_UNITS.k3sAgent]: unit({
      Unit: [
        "Description=socialweb node: k3s agent joined through the apiserver link",
        `After=network-online.target ${NODE_UNITS.nodeSecrets} ${NODE_UNITS.apiserverLink} ${NODE_NET_UNITS.dnsLink} ${NODE_NET_UNITS.masquerade}`,
        `Wants=network-online.target ${NODE_UNITS.nodeSecrets} ${NODE_UNITS.apiserverLink} ${NODE_NET_UNITS.dnsLink} ${NODE_NET_UNITS.masquerade}`,
        "StartLimitIntervalSec=0",
      ],
      Service: [
        "Type=notify",
        `EnvironmentFile=${p.linkEnv}`,
        "KillMode=process",
        "Delegate=yes",
        "LimitNOFILE=1048576",
        "LimitNPROC=infinity",
        "LimitCORE=infinity",
        "TasksMax=infinity",
        "TimeoutStartSec=0",
        "ExecStartPre=-/sbin/modprobe br_netfilter",
        "ExecStartPre=-/sbin/modprobe overlay",
        `ExecStartPre=/usr/bin/test -x ${p.k3s}`,
        `ExecStartPre=/usr/bin/test -s ${p.token}`,
        `ExecStart=${p.k3s} agent --server https://\${ADVERTISE_ADDRESS}:\${APISERVER_PORT} --token-file ${p.token} --node-name \${NODE_NAME} --data-dir ${p.dataDir} --kubelet-arg=cluster-dns=${NODE_LOCAL_DNS_ADDRESS} --node-label=${NODE_LOCAL_DNS_LABEL}=true`,
        ...always,
      ],
      Install: install,
    }),
  };
}

export function renderNodeNetworkUnits(opts: K3sJoinModuleOptions = {}): Record<string, string> {
  const p = resolvePaths(opts);
  const install = ["WantedBy=multi-user.target"];
  const oneshot = (exec: string, extra: string[] = []) => [
    "Type=oneshot",
    "RemainAfterExit=yes",
    ...extra,
    `ExecStart=${exec}`,
    "Restart=on-failure",
    "RestartSec=5",
    ...CONSOLE,
  ];
  const units: Record<string, string> = {
    [NODE_NET_UNITS.diagnose]: unit({
      Unit: [
        "Description=socialweb node: print containerd's CNI status to the console until the node network is ready",
        "StartLimitIntervalSec=0",
      ],
      Service: ["Type=oneshot", `ExecStart=${NODE_NET_SCRIPTS.diagnose}`, ...CONSOLE],
      Install: install,
    }),
    [NODE_NET_UNITS.dnsLink]: unit({
      Unit: [
        "Description=socialweb node: the per-node DNS address on a dummy interface",
        "StartLimitIntervalSec=0",
      ],
      Service: oneshot(NODE_NET_SCRIPTS.dnsLink),
      Install: install,
    }),
    [NODE_NET_UNITS.masquerade]: unit({
      Unit: [
        "Description=socialweb node: forward pod traffic and SNAT only what leaves the cluster CIDR",
        "StartLimitIntervalSec=0",
      ],
      Service: oneshot(NODE_NET_SCRIPTS.masquerade),
      Install: install,
    }),
    [NODE_NET_UNITS.cni]: unit({
      Unit: [
        "Description=socialweb node: bridge CNI on the Node's podCIDR, once the Node has registered",
        `After=${NODE_UNITS.nodeSecrets}`,
        `Wants=${NODE_UNITS.nodeSecrets}`,
        "StartLimitIntervalSec=0",
      ],
      Service: oneshot(NODE_NET_SCRIPTS.cni, [`EnvironmentFile=${p.linkEnv}`, "TimeoutStartSec=infinity"]),
      Install: install,
    }),
  };
  if (opts.meshd) {
    units[NODE_NET_UNITS.meshdInstall] = unit({
      Unit: [
        "Description=socialweb node: install the sha256-pinned socialweb-meshd",
        "After=network-online.target",
        "Wants=network-online.target",
        "StartLimitIntervalSec=0",
      ],
      Service: oneshot(NODE_NET_SCRIPTS.meshdInstall),
      Install: install,
    });
    units[NODE_NET_UNITS.meshd] = MESHD_UNIT;
  }
  return units;
}

export function renderResolver(nameservers: string[]): string {
  return `${nameservers.map((n) => `nameserver ${n}`).join("\n")}\noptions timeout:2 attempts:3\n`;
}

function script(path: string, content: string): WriteFileEntry {
  return { path, owner: "root:root", permissions: "0700", content };
}

function systemdFile(path: string, content: string): WriteFileEntry {
  return { path, owner: "root:root", permissions: "0644", content };
}

export function createK3sJoinModule(opts: K3sJoinModuleOptions = {}): UserDataModule {
  if (opts.meshd) validateMeshdPin(opts.meshd);
  const nameservers = opts.dnsNameservers ?? [DEFAULT_DNS_GATEWAY];
  const resolver = renderResolver(nameservers);
  const writeResolver = `printf '%s' '${resolver.replace(/'/g, `'\\''`)}' > ${RESOLV_CONF_PATH}`;
  const units = renderNodeUnits(opts);
  const netUnits = renderNodeNetworkUnits(opts);
  const started = [...NODE_UNIT_ORDER, ...Object.keys(netUnits)];
  const meshdFiles: WriteFileEntry[] = opts.meshd
    ? [
      script(NODE_NET_SCRIPTS.meshdInstall, renderMeshdInstallScript(opts)),
      systemdFile(MESHD_DROP_IN_PATH, MESHD_UNIT_DROP_IN),
    ]
    : [];
  return (): UserDataPatch => ({
    packages: ["jq", "curl", "iptables"],
    write_files: [
      { path: RESOLV_CONF_PATH, owner: "root:root", permissions: "0644", content: resolver },
      { path: SYSCTL_PATH, owner: "root:root", permissions: "0644", content: "net.ipv4.ip_forward = 1\n" },
      script(NODE_SCRIPTS.openbaoLink, renderOpenBaoLinkScript(opts)),
      script(NODE_SCRIPTS.nodeSecrets, renderNodeSecretsScript(opts)),
      script(NODE_SCRIPTS.apiserverLink, renderApiserverLinkScript(opts)),
      script(NODE_NET_SCRIPTS.dnsLink, renderDnsLinkScript()),
      script(NODE_NET_SCRIPTS.masquerade, renderPodMasqueradeScript()),
      script(NODE_NET_SCRIPTS.cni, renderPodCniScript(opts)),
      script(NODE_NET_SCRIPTS.diagnose, renderNodeDiagnoseScript(opts)),
      ...meshdFiles,
      ...NODE_UNIT_ORDER.map((name) => systemdFile(`/etc/systemd/system/${name}`, units[name])),
      ...Object.entries(netUnits).map(([name, content]) => systemdFile(`/etc/systemd/system/${name}`, content)),
    ],
    bootcmd: [writeResolver],
    runcmd: [
      writeResolver,
      "systemctl daemon-reload",
      `systemctl enable ${started.join(" ")}`,
      `systemctl start --no-block ${started.join(" ")}`,
    ],
  });
}
