import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export type StrongRef = { $type: "com.atproto.repo.strongRef"; uri: string; cid: string };

export type VM = {
  cpus: number;
  mem: string;
  disk: string;
  network: string;
  role: string;
  user_data: string;
  location?: { country?: string; region?: string };
  _uri?: string;
  _cid?: string;
  _accept?: { uri: string; cid: string };
};

export interface DropletSpec {
  region?: string;
  size?: string;
  image?: string;
}

export interface ProvisionResult {
  providerId: string | number;
  metadata: Record<string, unknown>;
}

export interface ComputeAtproto {
  getAgentDid(): string;
  createRecord(collection: string, record: Record<string, unknown>): Promise<StrongRef>;
  createSharedRecord(collection: string, record: Record<string, unknown>): Promise<StrongRef>;
  deleteRecord(collection: string, rkey: string): Promise<void>;
}

export interface ComputeProviderCtx {
  logger: StructuredLoggerInterface;
  atproto: ComputeAtproto;
}

export interface GuestGone {
  id: string | number;
  reason: string;
  exitCode?: number;
  message?: string;
}

export interface GuestHostKey {
  type: string;
  publicKey: string;
  fingerprint?: string;
}

export interface GuestHostKeysReport {
  id: string | number;
  keys: GuestHostKey[];
  observedAt: string;
}

export interface GuestHostKeysAck {
  ok: boolean;
  status?: number;
  reason?: string;
  uri?: string;
}

export type GuestHostKeysHandler = (report: GuestHostKeysReport) => Promise<GuestHostKeysAck>;

export type AdoptResult =
  | { state: "running" }
  | { state: "gone"; gone: GuestGone };

export interface ComputeProvider {
  readonly name: string;
  adopt?(id: string | number): Promise<AdoptResult>;
  provision(
    vm: VM,
    requesterDid: string,
    spec?: DropletSpec,
  ): Promise<ProvisionResult>;
  destroy(id: string | number): Promise<void>;
  createBidConfig(nowIso: string): Promise<StrongRef>;
  injectAcceptBundle(
    userData: string,
    bundle: Record<string, unknown>,
  ): string;
  getDroplet?(id: string): Record<string, unknown> | undefined;
  onGuestGone?(listener: (gone: GuestGone) => void): () => void;
  onHostKeys?(handler: GuestHostKeysHandler): () => void;
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
}

export class GuestHostKeysRelay {
  #handler: GuestHostKeysHandler | undefined;

  onHostKeys(handler: GuestHostKeysHandler): () => void {
    this.#handler = handler;
    return () => {
      if (this.#handler === handler) this.#handler = undefined;
    };
  }

  async deliver(id: string | number, keys: GuestHostKey[], observedAt: string): Promise<GuestHostKeysAck & { id: string | number }> {
    if (!this.#handler) return { ok: false, status: 503, reason: "no bidder listens for host keys", id };
    return { ...(await this.#handler({ id, keys, observedAt })), id };
  }
}
