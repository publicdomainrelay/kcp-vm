export const CLUSTER_NSID_PREFIX = "com.publicdomainrelay.temp.cluster.";

export const CLUSTER_NSID = {
  createCluster: `${CLUSTER_NSID_PREFIX}createCluster`,
  listClusters: `${CLUSTER_NSID_PREFIX}listClusters`,
  getKubeconfig: `${CLUSTER_NSID_PREFIX}getKubeconfig`,
  controlPlaneUp: `${CLUSTER_NSID_PREFIX}controlPlaneUp`,
  controlPlaneDown: `${CLUSTER_NSID_PREFIX}controlPlaneDown`,
  deleteCluster: `${CLUSTER_NSID_PREFIX}deleteCluster`,
} as const;

export type ClusterNsid = typeof CLUSTER_NSID[keyof typeof CLUSTER_NSID];

export type ClusterPhase = "Initializing" | "Ready" | "Unavailable" | "Draining";

export interface ClusterSummary {
  name: string;
  phase: ClusterPhase | string;
  running: boolean;
  instance?: string;
  teardownBlocked?: boolean;
  server?: string;
}

export interface ClusterPhaseResult {
  phase: ClusterPhase | string;
}

export interface KubeconfigGrant {
  kubeconfig: string;
  instance?: string;
}

export const CLUSTER_NAME_MAX = 63;

export function isClusterName(name: unknown): name is string {
  return typeof name === "string" &&
    name.length > 0 &&
    name.length <= CLUSTER_NAME_MAX &&
    /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name);
}

export function kubeconfigFilename(cluster: string): string {
  return `${cluster}.kubeconfig.yaml`;
}

export const REQUEST_PROOF_SCHEME = "SWC-Proof";
export const REQUEST_PROOF_TYP = "swc-proof+jwt";
export const REQUEST_PROOF_MAX_SKEW_SEC = 60;

export interface RequestProofClaims {
  iss: string;
  htm: string;
  htu: string;
  iat: number;
  jti: string;
}

export const DAY_SEC = 86_400;

export const DEFAULT_KUBECONFIG_TTL_CHOICES_SEC: readonly number[] = [DAY_SEC, 7 * DAY_SEC, 30 * DAY_SEC];

export function ttlChoicesUpTo(maxSec: number, choices: readonly number[] = DEFAULT_KUBECONFIG_TTL_CHOICES_SEC): number[] {
  const allowed = choices.filter((c) => c <= maxSec);
  return allowed.length > 0 ? allowed : [maxSec];
}

export const K3S_SETUP_STAMP = "/var/lib/setup-k3s.done";

export const NODE_SECRETS_DIR = "/etc/socialweb/node";
export const OPENBAO_TICKET_PATH = `${NODE_SECRETS_DIR}/openbao-ticket`;
export const OPENBAO_CA_PATH = `${NODE_SECRETS_DIR}/openbao-ca.pem`;
export const NODE_CLAIM_DOC_PATH = `${NODE_SECRETS_DIR}/claim.json`;
export const NODE_LINK_ENV_PATH = `${NODE_SECRETS_DIR}/apiserver.env`;
export const NODE_BUNDLE_PATHS = [OPENBAO_TICKET_PATH, OPENBAO_CA_PATH, NODE_CLAIM_DOC_PATH] as const;

export interface NodeLinkEnv {
  node: string;
  advertiseAddress: string;
  apiserverPort: number;
  ticket: string;
}

export const IPV4_ADDRESS = /^([0-9]{1,3}\.){3}[0-9]{1,3}$/;
export const PORT_NUMBER = /^[0-9]{1,5}$/;
export const DUMBPIPE_TICKET = /^[a-z0-9]+$/;

export function renderNodeLinkEnv(env: NodeLinkEnv): string {
  return `NODE_NAME=${env.node}\nADVERTISE_ADDRESS=${env.advertiseAddress}\n` +
    `APISERVER_PORT=${env.apiserverPort}\nAPISERVER_TICKET=${env.ticket}\n`;
}

export function nodeLinkEnv(
  spec: Pick<NodeLinkEnv, "node" | "advertiseAddress" | "apiserverPort">,
  ticket: string,
): string | null {
  if (!DUMBPIPE_TICKET.test(ticket)) return null;
  if (!IPV4_ADDRESS.test(spec.advertiseAddress)) return null;
  if (!PORT_NUMBER.test(String(spec.apiserverPort))) return null;
  return renderNodeLinkEnv({ ...spec, ticket });
}

export const NODE_KV_MOUNT = "socialweb-nodes";
export const OPENBAO_LINK_LOCAL_ADDR = "127.0.0.1:18200";
export const NODE_AUDIENCE_PREFIX = "socialweb.computer/node/";
export const JWT_MOUNT_PREFIX = "jwt-";
export const JWT_MOUNT_HASH_HEX_CHARS = 12;
export const ACCOUNT_HASH_CHARS = 8;

export type ClusterLinkServiceName = "apiserver" | "openbao";

export interface NodeClaimDocument {
  node: string;
  cluster: string;
  jwtMount: string;
  role: string;
  audience: string;
  kvMount: string;
  kvPath: string;
}

export function nodeAudience(clusterKey: string, nodeName: string): string {
  return `${NODE_AUDIENCE_PREFIX}${clusterKey}/${nodeName}`;
}

export function jwtMountName(issuerSha256Hex: string): string {
  return `${JWT_MOUNT_PREFIX}${issuerSha256Hex.slice(0, JWT_MOUNT_HASH_HEX_CHARS)}`;
}

export function accountHashOf(didSha256: Uint8Array): string {
  let n = 0n;
  for (const b of didSha256) n = (n << 8n) | BigInt(b);
  return n.toString(36).slice(0, ACCOUNT_HASH_CHARS);
}

export function clusterKeyOf(accountHash: string, cluster: string): string {
  return `${accountHash}-${cluster}`;
}

export function clusterLinkName(clusterKey: string, service: ClusterLinkServiceName): string {
  return `${clusterKey}-${service}`;
}

export function nodeClaimDocument(clusterKey: string, nodeName: string, issuerSha256Hex: string): NodeClaimDocument {
  return {
    node: nodeName,
    cluster: clusterKey,
    jwtMount: jwtMountName(issuerSha256Hex),
    role: nodeName,
    audience: nodeAudience(clusterKey, nodeName),
    kvMount: NODE_KV_MOUNT,
    kvPath: nodeName,
  };
}
