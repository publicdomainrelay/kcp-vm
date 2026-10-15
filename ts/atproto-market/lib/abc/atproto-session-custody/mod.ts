export const DPOP_NONCE_HEADER = "dpop-nonce";
export const USE_DPOP_NONCE = "use_dpop_nonce";

export type CustodySessionStatus = "active" | "expired";

export interface CustodyStartInput {
  role: string;
  handle?: string;
  did?: string;
  redirectUri?: string;
}

export interface CustodyLoginStart {
  authorizationUrl: string;
  state: string;
  expiresAt: string;
  did: string;
  pds: string;
}

export interface CustodyCompleteInput {
  state: string;
  code: string;
  iss?: string;
}

export interface CustodyLogin {
  did: string;
  handle: string;
  pds: string;
  token: string;
  leaseSeconds: number;
  policies: string[];
}

export interface CustodySession {
  did: string;
  handle: string;
  pds: string;
  issuer: string;
  scope: string;
  expiresAt: string;
  status: CustodySessionStatus;
  statusReason: string;
}

export interface ProofRequest {
  did: string;
  method: string;
  url: string;
  nonce?: string;
}

export interface SessionProof {
  accessToken: string;
  dpopProof: string;
  expiresAt: string;
}

export interface SignOutResult {
  revoked: boolean;
  revokeError?: string;
}

export interface SessionCustody {
  start(input: CustodyStartInput): Promise<CustodyLoginStart>;
  complete(input: CustodyCompleteInput): Promise<CustodyLogin>;
  session(did: string): Promise<CustodySession | null>;
  proof(request: ProofRequest): Promise<SessionProof>;
  signOut(did: string): Promise<SignOutResult>;
}

export interface CustodyIdentity {
  did: string;
  handle: string;
  pds: string;
  policies: string[];
  ttlSeconds: number;
  renewable: boolean;
}

export interface TokenCustody extends SessionCustody {
  identity(): Promise<CustodyIdentity | null>;
  renew(incrementSeconds?: number): Promise<number>;
  revokeToken(): Promise<void>;
}

export interface SessionCustodyConnector {
  own: SessionCustody;
  forToken(token: string): TokenCustody;
  clientMetadata(): Promise<Record<string, unknown>>;
}

export class SessionCustodyError extends Error {
  readonly status: number;
  readonly detail: string;

  constructor(operation: string, status: number, detail: string) {
    super(`session custody ${operation}: ${status} ${detail}`);
    this.name = "SessionCustodyError";
    this.status = status;
    this.detail = detail;
  }
}

export interface NonceBook {
  get(origin: string): string | undefined;
  set(origin: string, nonce: string): void;
}

export function createNonceBook(): NonceBook {
  const nonces = new Map<string, string>();
  return {
    get: (origin) => nonces.get(origin),
    set: (origin, nonce) => {
      nonces.set(origin, nonce);
    },
  };
}

export function originOf(url: string): string {
  return new URL(url).origin;
}

export function htuOf(url: string): string {
  const u = new URL(url);
  u.search = "";
  u.hash = "";
  return u.toString();
}

export interface NonceChallenge {
  status: number;
  wwwAuthenticate?: string | null;
  errorCode?: string;
}

export function isUseDpopNonce(challenge: NonceChallenge): boolean {
  if (challenge.status !== 400 && challenge.status !== 401) return false;
  if (challenge.errorCode === USE_DPOP_NONCE) return true;
  return /error="?use_dpop_nonce"?/.test(challenge.wwwAuthenticate ?? "");
}

export type CustodyWriteOp =
  | { action: "create" | "update"; collection: string; rkey: string; record: Record<string, unknown> }
  | { action: "delete"; collection: string; rkey: string };

export interface CustodyCommit {
  repo: string;
  commit: string;
  rev: string;
  since: null;
  blocks: Uint8Array;
  ops: unknown[];
}

export interface CustodyRecord {
  uri: string;
  cid: string;
  value: Record<string, unknown>;
}

export interface CustodyAgent {
  readonly did: string;
  readonly sessionData: { userDid: string };
  readonly signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  applyWrites(did: string, writes: CustodyWriteOp[]): Promise<CustodyCommit>;
  getRecord(did: string, collection: string, rkey: string): Promise<CustodyRecord | null>;
  listRecords(did: string, collection: string, opts?: { limit?: number }): Promise<{ records: CustodyRecord[] }>;
  getServiceAuth(aud: string, lxm?: string): Promise<string>;
  createRecord(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  putRecord(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
}
