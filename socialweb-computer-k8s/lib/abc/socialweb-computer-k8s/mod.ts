import type { ClusterPhaseResult, ClusterSummary, KubeconfigGrant } from "@publicdomainrelay/socialweb-computer-k8s-common";
import type { ClusterLinkSpec, ClusterLinkStatus, NodeClaimSpec, NodeClaimStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface NodeClaim {
  workspace: string;
  name: string;
  resourceVersion?: string;
  deletionTimestamp?: string;
  finalizers: string[];
  spec: NodeClaimSpec;
  status: NodeClaimStatus;
}

export function nodeClaimKey(claim: Pick<NodeClaim, "workspace" | "name">): string {
  return `${claim.workspace}/${claim.name}`;
}

export interface ClusterGateway {
  list(did: string): Promise<ClusterSummary[]>;
  create(did: string, name: string): Promise<ClusterPhaseResult>;
  kubeconfig(did: string, name: string, ttlSeconds?: number): Promise<KubeconfigGrant>;
  up(did: string, name: string): Promise<ClusterPhaseResult>;
  down(did: string, name: string): Promise<ClusterPhaseResult>;
  remove(did: string, name: string): Promise<ClusterPhaseResult>;
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

export interface ServiceAuthSource {
  serviceAuth(did: string, aud: string, lxm: string): Promise<string>;
}

export interface ProvedRequest {
  method: string;
  path: string;
  authorization: string | undefined;
}

export interface RequestProofVerifier {
  verify(request: ProvedRequest): Promise<{ did: string } | null>;
}

export class ReplayLedger {
  private readonly seen = new Map<string, number>();

  constructor(readonly windowSec: number) {}

  claim(jti: string, nowSec: number): boolean {
    this.evict(nowSec);
    if (this.seen.has(jti)) return false;
    this.seen.set(jti, nowSec + this.windowSec * 2);
    return true;
  }

  get size(): number {
    return this.seen.size;
  }

  private evict(nowSec: number): void {
    for (const [jti, expires] of this.seen) {
      if (expires < nowSec) this.seen.delete(jti);
    }
  }
}

export interface NodeClaimStore {
  list(): Promise<NodeClaim[]>;
  get(claim: Pick<NodeClaim, "workspace" | "name">): Promise<NodeClaim | null>;
  replaceStatus(claim: NodeClaim, status: NodeClaimStatus): Promise<void>;
  addFinalizer(claim: NodeClaim): Promise<void>;
  removeFinalizer(claim: NodeClaim): Promise<void>;
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
