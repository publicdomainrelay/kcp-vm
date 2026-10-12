import {
  claimSecretsIngressKeyRef,
  type ClusterPhaseResult,
  type ClusterSummary,
  type KubeconfigGrant,
} from "@publicdomainrelay/socialweb-computer-k8s-common";
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

export interface StoredClaimKey {
  privateKeyHex: string;
  keyRef: string;
}

export interface ClaimKeyStore {
  readonly durable: boolean;
  ensure(claimName: string, candidateHex: string): Promise<StoredClaimKey>;
  read(claimName: string): Promise<StoredClaimKey | null>;
  remove(claimName: string): Promise<void>;
}

export function createMemoryClaimKeyStore(): ClaimKeyStore {
  const keys = new Map<string, string>();
  const stored = (claimName: string, privateKeyHex: string): StoredClaimKey => ({
    privateKeyHex,
    keyRef: claimSecretsIngressKeyRef(claimName),
  });
  return {
    durable: false,
    ensure(claimName, candidateHex) {
      if (!keys.has(claimName)) keys.set(claimName, candidateHex);
      return Promise.resolve(stored(claimName, keys.get(claimName)!));
    },
    read(claimName) {
      const hex = keys.get(claimName);
      return Promise.resolve(hex === undefined ? null : stored(claimName, hex));
    },
    remove(claimName) {
      keys.delete(claimName);
      return Promise.resolve();
    },
  };
}
