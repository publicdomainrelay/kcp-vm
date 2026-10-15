import type {
  ClusterPhaseResult,
  ClusterSummary,
  KubeconfigGrant,
} from "@publicdomainrelay/socialweb-computer-k8s-common";
import type { ClusterLinkSpec, ClusterLinkStatus, NodeClaimSpec, NodeClaimStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface NodeClaim {
  workspace: string;
  name: string;
  uid?: string;
  resourceVersion?: string;
  deletionTimestamp?: string;
  finalizers: string[];
  adoptJoinedNode?: boolean;
  spec: NodeClaimSpec;
  status: NodeClaimStatus;
}

export function nodeClaimKey(claim: Pick<NodeClaim, "workspace" | "name">): string {
  return `${claim.workspace}/${claim.name}`;
}

export interface ClusterCaller {
  did: string;
  serviceAuth(aud: string, lxm: string): Promise<string>;
}

export interface ClusterGateway {
  list(caller: ClusterCaller): Promise<ClusterSummary[]>;
  create(caller: ClusterCaller, name: string): Promise<ClusterPhaseResult>;
  kubeconfig(caller: ClusterCaller, name: string, ttlSeconds?: number, noExpiry?: boolean): Promise<KubeconfigGrant>;
  up(caller: ClusterCaller, name: string): Promise<ClusterPhaseResult>;
  down(caller: ClusterCaller, name: string): Promise<ClusterPhaseResult>;
  remove(caller: ClusterCaller, name: string): Promise<ClusterPhaseResult>;
}

export class ClusterGatewayError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ClusterGatewayError";
  }
}

export interface NodeClaimStore {
  list(): Promise<NodeClaim[]>;
  get(claim: Pick<NodeClaim, "workspace" | "name">): Promise<NodeClaim | null>;
  replaceStatus(claim: NodeClaim, status: NodeClaimStatus): Promise<void>;
  addFinalizer(claim: NodeClaim): Promise<void>;
  removeFinalizer(claim: NodeClaim): Promise<void>;
  watch?(signal: AbortSignal, onChange: () => void): void;
}

export interface ClusterLinkView {
  workspace: string;
  name: string;
  spec?: ClusterLinkSpec;
  status: ClusterLinkStatus;
}

export interface ClusterLinkReader {
  get(workspace: string, name: string): Promise<ClusterLinkView | null>;
}

export interface PinnedDownload {
  name: string;
  sha256: string;
  size: number;
}

export interface PinnedDownloads {
  list(): PinnedDownload[];
  get(sha256: string, name: string): Uint8Array<ArrayBuffer> | null;
}
