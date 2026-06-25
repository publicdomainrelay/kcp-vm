// Pure interfaces for the compute requester. Zero I/O, no Deno.*, no fetch, no crypto.
// Depends on market-common + serve/logger types only (type-level imports).

import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RelayRef, ServeHandle } from "@publicdomainrelay/serve";

export interface CollectedBid {
  did: string;
  uri: string;
  cid: string;
  record: Record<string, unknown>;
}

export interface ContractFlowOptions {
  vmName?: string;
  bidWindowSec?: number;
  /** Additional bidder DIDs beyond those discovered via relay + vouch. */
  extraBidderDids?: string[];
  /** DIDs to exclude from bidding, even if discovered. */
  denyBidderDids?: string[];
  skipSsh?: boolean;
  execProgram?: string;
  noDelete?: boolean;
  vmReadyTimeoutSec?: number;
  onSshStart?: () => void;
  onSshEnd?: () => void | Promise<void>;
  /** Dispatcher host for the relay (used to derive the VM FQDN). */
  dispatcherHost?: string;
}

export interface PDSOptions {
  logger: StructuredLoggerInterface;
  serve: ServeHandle;
  privateKeyHex?: string;
  plcDirectoryUrl?: string;
  dispatcherHost?: string;
  label?: string;
}

export interface RequesterPDS {
  did: string;
  serve: ServeHandle;
  relay: RelayRef;
  /** Valid only after beginServe(); reads lazily off the relay. */
  proxyRef: string;
  /** Valid only after beginServe(). */
  relaySubdomain: string;
  beginServe(): Promise<void>;
  pendingBids: Map<string, CollectedBid[]>;
  createRepoRecord(collection: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord(
    collection: string,
    record: Record<string, unknown>,
    aKp: { did(): string; privateKey: { bytes: Uint8Array; toBytes?(): Uint8Array } },
    issuer?: string,
  ): Promise<{ uri: string; cid: string }>;
  resolveBidderEndpoint(endpointUrl: string): Promise<{ targetUrl: string; audDid: string } | null>;
  callBidder(
    targetBase: string,
    nsid: string,
    lxm: string,
    audDid: string,
    body: Record<string, unknown>,
  ): Promise<{ status: number; ok: boolean; body: unknown }>;
  attestationKp: { did(): string; privateKey: { bytes: Uint8Array; toBytes?(): Uint8Array } };
  privateKeyHex: string;
}

export interface SshSessionProvider {
  generateKeypair(vmName: string): Promise<{ publicKey: string; privateKeyPath: string }>;
  pollReady(privateKeyPath: string, fqdn: string, timeoutMs: number): Promise<boolean>;
  runSession(privateKeyPath: string, fqdn: string, program: string): Promise<number>;
}

export interface ContractFlowResult {
  event: string;
  vmUri?: string;
  vmCid?: string;
  rfpUri?: string;
  rfpCid?: string;
  acceptUri?: string;
  acceptCid?: string;
  bidUri?: string;
  bidCid?: string;
  winnerDid?: string;
  receiptUri?: string;
  receiptCid?: string;
  submitEventRef?: string;
  receiptOk?: boolean;
  bids?: number;
  error?: string;
}

export interface ConsoleBuffer {
  pause(): void;
  resume(): Promise<void>;
}
