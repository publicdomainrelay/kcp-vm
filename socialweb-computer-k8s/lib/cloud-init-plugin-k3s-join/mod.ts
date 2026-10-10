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
}

function resolvePaths(opts: K3sJoinModuleOptions): ResolvedPaths {
  const dir = opts.nodeDir ?? NODE_SECRETS_DIR;
  const inDir = (canonical: string) => `${dir}${canonical.slice(NODE_SECRETS_DIR.length)}`;
  return {
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
    dataDir: opts.k3sDataDir ?? "/var/lib/rancher/k3s",
  };
}

export function renderNodeSecretsScript(opts: K3sJoinModuleOptions = {}): string {
  const p = resolvePaths(opts);
  return `#!/bin/bash
set -euo pipefail
say() { echo "socialweb-node-secrets: $*" >&2; }
[ -f ${p.done} ] && [ -s ${p.linkEnv} ] && exit 0
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
chmod 0600 ${p.serverCA} ${p.token} ${p.nodePassword} ${p.k3sNodePassword} ${p.linkEnv}
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
      Service: ["Type=simple", `ExecStart=${NODE_SCRIPTS.openbaoLink}`, ...always],
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
      Service: ["Type=simple", `EnvironmentFile=${p.linkEnv}`, `ExecStart=${NODE_SCRIPTS.apiserverLink}`, ...always],
      Install: install,
    }),
    [NODE_UNITS.k3sAgent]: unit({
      Unit: [
        "Description=socialweb node: k3s agent joined through the apiserver link",
        `After=network-online.target ${NODE_UNITS.nodeSecrets} ${NODE_UNITS.apiserverLink}`,
        `Wants=network-online.target ${NODE_UNITS.nodeSecrets} ${NODE_UNITS.apiserverLink}`,
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
        `ExecStart=${p.k3s} agent --server https://\${ADVERTISE_ADDRESS}:\${APISERVER_PORT} --token-file ${p.token} --node-name \${NODE_NAME} --data-dir ${p.dataDir}`,
        ...always,
      ],
      Install: install,
    }),
  };
}

export function renderResolver(nameservers: string[]): string {
  return `${nameservers.map((n) => `nameserver ${n}`).join("\n")}\noptions timeout:2 attempts:3\n`;
}

function script(path: string, content: string): WriteFileEntry {
  return { path, owner: "root:root", permissions: "0700", content };
}

export function createK3sJoinModule(opts: K3sJoinModuleOptions = {}): UserDataModule {
  const nameservers = opts.dnsNameservers ?? [DEFAULT_DNS_GATEWAY];
  const resolver = renderResolver(nameservers);
  const writeResolver = `printf '%s' '${resolver.replace(/'/g, `'\\''`)}' > ${RESOLV_CONF_PATH}`;
  const units = renderNodeUnits(opts);
  return (): UserDataPatch => ({
    packages: ["jq", "curl"],
    write_files: [
      { path: RESOLV_CONF_PATH, owner: "root:root", permissions: "0644", content: resolver },
      script(NODE_SCRIPTS.openbaoLink, renderOpenBaoLinkScript(opts)),
      script(NODE_SCRIPTS.nodeSecrets, renderNodeSecretsScript(opts)),
      script(NODE_SCRIPTS.apiserverLink, renderApiserverLinkScript(opts)),
      ...NODE_UNIT_ORDER.map((name) => ({
        path: `/etc/systemd/system/${name}`,
        owner: "root:root",
        permissions: "0644",
        content: units[name],
      })),
    ],
    bootcmd: [writeResolver],
    runcmd: [
      writeResolver,
      "systemctl daemon-reload",
      `systemctl enable ${NODE_UNIT_ORDER.join(" ")}`,
      `systemctl start --no-block ${NODE_UNIT_ORDER.join(" ")}`,
    ],
  });
}
