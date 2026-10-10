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

export const K3S_JOIN_PATH = "/usr/local/bin/socialweb-k3s-join";
export const K3S_JOIN_READY_MARKER = "SOCIALWEB_K3S_JOIN_READY";
export const K3S_JOIN_GO = "GO";
export const K3S_SETUP_STAMP = "/var/lib/setup-k3s.done";

export interface NodeJoinDocument {
  nodeName: string;
  serverUrl: string;
  advertiseAddress: string;
  token: string;
  nodePassword: string;
}

export const NODE_JOIN_KEYS = ["NODE_NAME", "SERVER_URL", "ADVERTISE", "TOKEN", "NODE_PASSWORD"] as const;

function base64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary);
}

export function encodeNodeJoinDocument(doc: NodeJoinDocument): string {
  const values: Record<typeof NODE_JOIN_KEYS[number], string> = {
    NODE_NAME: doc.nodeName,
    SERVER_URL: doc.serverUrl,
    ADVERTISE: doc.advertiseAddress,
    TOKEN: doc.token,
    NODE_PASSWORD: doc.nodePassword,
  };
  return NODE_JOIN_KEYS.map((k) => `${k} ${base64(values[k])}`).join("\n") + "\n\n";
}

export function pinnedJoinToken(serverCASha256Hex: string, token: string): string {
  return `K10${serverCASha256Hex}::server:${token}`;
}
