import type { Logger } from "@publicdomainrelay/logger";

export class UnauthorizedException extends Error {
  constructor(msg: string) { super(msg); this.name = "UnauthorizedException"; }
}

export function parseAudience(aud: string): { actx: string; api: string } {
  const rest = aud.startsWith("api://") ? aud.slice(6) : null;
  if (!rest) throw new UnauthorizedException(`aud does not start with api://: ${aud}`);
  const qIdx = rest.indexOf("?");
  if (qIdx < 0) throw new UnauthorizedException(`aud missing ?actx=: ${aud}`);
  const api = rest.slice(0, qIdx);
  const params = new URLSearchParams(rest.slice(qIdx + 1));
  const actx = params.get("actx");
  if (!actx) throw new UnauthorizedException(`aud missing actx param: ${aud}`);
  return { actx, api };
}

export interface JwkStore {
  getJwkPem(issuer: string): string | null | Promise<string | null>;
  saveJwkPem(issuer: string, pem: string): void | Promise<void>;
  getPublishedJwkPems?(issuer: string): string[] | Promise<string[]>;
}

export interface OIDCTokenData {
  actx: string;
  api: string;
  aud: string;
  sub: string;
  claims: Record<string, unknown>;
  asString: string;
}

export interface NonceStore {
  createProvisioningNonce(nonce: string, dropletId: string): Promise<void>;
  getProvisioningNonceDropletId(nonce: string): Promise<string>;
  dropletOfNonce(nonce: string): Promise<string | undefined>;
  bindReportedHostKey(nonce: string, publicKey: string): Promise<string>;
  reportedHostKey(nonce: string): Promise<string | undefined>;
}

export interface GuestKv {
  read(path: string): Promise<Record<string, unknown> | null>;
  write(path: string, data: Record<string, unknown>): Promise<void>;
  list(path: string): Promise<string[]>;
  remove(path: string): Promise<void>;
}

export interface KvNonceStoreOptions {
  kv: GuestKv;
  prefix: string;
  entry: string;
  now?: () => Date;
}

interface ProvisioningEntry {
  nonce: string;
  hostKey?: string;
  consumedAt?: string;
}

function entryOf(raw: Record<string, unknown> | null): ProvisioningEntry | null {
  if (!raw || typeof raw.nonce !== "string" || !raw.nonce) return null;
  return {
    nonce: raw.nonce,
    ...(typeof raw.hostKey === "string" ? { hostKey: raw.hostKey } : {}),
    ...(typeof raw.consumedAt === "string" ? { consumedAt: raw.consumedAt } : {}),
  };
}

export function createKvNonceStore(opts: KvNonceStoreOptions): NonceStore {
  const { kv, prefix, entry } = opts;
  const now = opts.now ?? (() => new Date());
  const guestByNonce = new Map<string, string>();
  const chains = new Map<string, Promise<unknown>>();
  const pathOf = (id: string) => `${prefix}/${id}/${entry}`;

  function serial<T>(id: string, work: () => Promise<T>): Promise<T> {
    const next = (chains.get(id) ?? Promise.resolve()).catch(() => {}).then(work);
    chains.set(id, next);
    next.finally(() => {
      if (chains.get(id) === next) chains.delete(id);
    }).catch(() => {});
    return next;
  }

  async function guestOf(nonce: string): Promise<string | undefined> {
    const held = guestByNonce.get(nonce);
    if (held !== undefined) return held;
    for (const name of await kv.list(prefix)) {
      const id = name.replace(/\/+$/, "");
      if (!id || [...guestByNonce.values()].includes(id)) continue;
      const found = entryOf(await kv.read(pathOf(id)));
      if (found) guestByNonce.set(found.nonce, id);
    }
    return guestByNonce.get(nonce);
  }

  async function entryFor(nonce: string): Promise<{ id: string; entry: ProvisioningEntry } | undefined> {
    const id = await guestOf(nonce);
    if (id === undefined) return undefined;
    const found = entryOf(await kv.read(pathOf(id)));
    if (!found || found.nonce !== nonce) {
      guestByNonce.delete(nonce);
      return undefined;
    }
    return { id, entry: found };
  }

  return {
    async createProvisioningNonce(nonce, dropletId) {
      await serial(dropletId, async () => {
        await kv.write(pathOf(dropletId), { nonce });
        guestByNonce.set(nonce, dropletId);
      });
    },
    async getProvisioningNonceDropletId(nonce) {
      const id = await guestOf(nonce);
      if (id === undefined) throw new Error(`Nonce ${nonce} not found`);
      return await serial(id, async () => {
        const found = entryOf(await kv.read(pathOf(id)));
        if (!found || found.nonce !== nonce || found.consumedAt) throw new Error(`Nonce ${nonce} not found`);
        await kv.write(pathOf(id), { ...found, consumedAt: now().toISOString() });
        return id;
      });
    },
    async dropletOfNonce(nonce) {
      return (await entryFor(nonce))?.id;
    },
    async bindReportedHostKey(nonce, publicKey) {
      const id = await guestOf(nonce);
      if (id === undefined) throw new Error("no guest holds this provisioning token");
      return await serial(id, async () => {
        const found = entryOf(await kv.read(pathOf(id)));
        if (!found || found.nonce !== nonce) throw new Error("no guest holds this provisioning token");
        if (found.hostKey) return found.hostKey;
        await kv.write(pathOf(id), { ...found, hostKey: publicKey });
        return publicKey;
      });
    },
    async reportedHostKey(nonce) {
      return (await entryFor(nonce))?.entry.hostKey;
    },
  };
}

export interface ProvisioningDataInit {
  nonce: string;
  token: OIDCTokenData;
  userData: string;
}

export interface ProvisioningData extends ProvisioningDataInit {
  associateWithDroplet(dropletId: string): Promise<void>;
}

export type DropletGetter = (
  id: string,
) => Record<string, unknown> | undefined | Promise<Record<string, unknown> | undefined>;

export type IssuersForAudience = (api: string, actx: string) => Promise<string[]> | string[];

export interface ReportedHostKey {
  type: string;
  publicKey: string;
  fingerprint?: string;
}

export interface HostKeysReported {
  nonce: string;
  keys: ReportedHostKey[];
}

export interface HostKeysReportAck {
  ok: boolean;
  status?: number;
  reason?: string;
}

export class HostKeyConflictException extends Error {
  constructor(msg: string) { super(msg); this.name = "HostKeyConflictException"; }
}

export interface OidcIssuerOptions {
  getIssuerUrl: () => string;
  getDroplet?: DropletGetter;
  plcDirectoryUrl?: string;
  log?: Logger;
  store?: JwkStore;
  nonceStore?: NonceStore;
  trustedIssuerUrls?: string[];
  defaultTtlSeconds?: number;
  reportedHostKeyWaitMs?: number;
  keyRefreshMs?: number;
  onHostKeys?: (report: HostKeysReported) => Promise<HostKeysReportAck>;
}

export interface AuthToken {
  sub: string;
  actx: string;
  asString: string;
  claims: Record<string, unknown>;
}

export interface OidcProvisioningEnricher {
  enrich(userData: string | null, teamUuid: string): Promise<{
    userData: string;
    nonce: string;
    associateWithDroplet(dropletId: string): Promise<void>;
  }>;
}

export interface OidcIssuer {
  app: { fetch: (req: Request) => Response | Promise<Response> };
  getIssuerUrl(): string;
  getSigningKey(): Promise<CryptoKeyPair>;
  getPublicJwk(): Promise<JsonWebKey & { kid?: string }>;
  createToken(actx: string, claims: Record<string, unknown>, api?: string): Promise<OIDCTokenData>;
  validateToken(token: string, getIssuers?: IssuersForAudience): Promise<OIDCTokenData>;
  raiseIfUnauthorized(service: string, scope: string, token: string, path: string, method: string): Promise<AuthToken>;
  createProvisioningData(
    teamUuid: string,
    userData: string | null,
    opts?: { ttl?: number; nonceLen?: number },
  ): Promise<ProvisioningData>;
  provisioningEnricher: OidcProvisioningEnricher;
  bindReportedHostKey(token: string, signature: string, ed25519PublicKey: string): Promise<string>;
}
