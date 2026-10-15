import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import type { KcpList } from "@publicdomainrelay/kcp-client-common";
import { createKcpRestClient } from "@publicdomainrelay/kcp-client-rest";
import {
  COMPUTE_VM_RESOURCE,
  type GuestIdentityGrant,
  GUEST_IDENTITY_GRANT_RESOURCE,
  parseGuestIdentityGrant,
} from "@publicdomainrelay/compute-provider-kcp-common";
import {
  type GuestGrantWish,
  type GuestIdentityGrants,
  type GuestIdentitySubject,
  grantWishesFromRbac,
  type RbacRecordLike,
} from "@publicdomainrelay/guest-identity-abc";
import { GUEST_ENTITY_PREFIX, GUEST_KCP_API, guestAudience } from "@publicdomainrelay/guest-identity-common";
import { listRBACRecords, resolvePDS } from "@publicdomainrelay/rbac-atproto";

export const FIELD_MANAGER_GUEST_IDENTITY_GRANT = "guest-identity-grants-kcp";

const RETRY_MS = 5_000;
const DEFAULT_RESYNC_MS = 30_000;
const RETRY_ATTEMPTS = 24;

export interface KcpGrantCredentials {
  server: string;
  workspace: string;
  token?: string;
  tokenFile?: string;
  caFile?: string;
}

export interface GuestIdentityGrantClient {
  apply(grant: GuestIdentityGrant): Promise<GuestIdentityGrant>;
  list(): Promise<KcpList<GuestIdentityGrant>>;
  delete(name: string): Promise<boolean>;
}

export function createGuestIdentityGrantClient(opts: {
  credentials: KcpGrantCredentials;
  kcp?: KcpClient;
  fetch?: typeof fetch;
}): GuestIdentityGrantClient {
  const creds = opts.credentials;
  const kcp = opts.kcp ??
    createKcpRestClient(
      { server: creds.server, token: creds.token, tokenFile: creds.tokenFile, caFile: creds.caFile },
      { fetch: opts.fetch },
    );
  const grants = kcpResourceClient(kcp, { resource: GUEST_IDENTITY_GRANT_RESOURCE, workspace: creds.workspace }, parseGuestIdentityGrant);
  return {
    apply: (grant) =>
      grants.patch(grant.metadata!.name!, {
        type: "apply",
        body: grant,
        fieldManager: FIELD_MANAGER_GUEST_IDENTITY_GRANT,
        force: true,
      }),
    list: () => grants.list(),
    delete: (name) => grants.delete(name),
  };
}

export interface GuestIdentityGrantsOptions {
  grants: GuestIdentityGrantClient;
  logger: StructuredLoggerInterface;
  issuer: () => string;
  plcDirectoryUrl: string;
  resyncMs?: number;
}

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

interface GuestState {
  guest: GuestIdentitySubject;
  running: boolean;
  dirty: boolean;
  forgotten: boolean;
  timer?: ReturnType<typeof setTimeout>;
  poll?: ReturnType<typeof setInterval>;
}

function specKey(wish: GuestGrantWish): string {
  return JSON.stringify({
    api: wish.api,
    actx: wish.actx,
    scopes: [...wish.scopes].sort(),
    claims: [...wish.claims].sort((a, b) => a.name.localeCompare(b.name)),
  });
}

function heldKey(grant: GuestIdentityGrant): string {
  const s = grant.spec;
  return specKey({ api: s?.api ?? "", actx: s?.actx ?? "", scopes: s?.scopes ?? [], claims: s?.claims ?? [] });
}

export function createGuestIdentityGrants(opts: GuestIdentityGrantsOptions): GuestIdentityGrants {
  const { grants, logger } = opts;
  const guests = new Map<string, GuestState>();

  async function nameOf(computevm: string, wish: GuestGrantWish): Promise<string> {
    return `${computevm}-${(await sha256Hex(guestAudience(wish.api, wish.actx))).slice(0, 12)}`;
  }

  function wishesOf(state: GuestState, records: RbacRecordLike[] | undefined): GuestGrantWish[] {
    const own: GuestGrantWish = { api: GUEST_KCP_API, actx: state.guest.computevm, scopes: ["on-network"], claims: [] };
    if (!records) return [own];
    return [own, ...grantWishesFromRbac(records, { issuer: opts.issuer(), subject: state.guest.subject })];
  }

  async function syncOnce(state: GuestState): Promise<boolean> {
    const { guest } = state;
    let records: RbacRecordLike[] | undefined;
    try {
      const pds = await resolvePDS(guest.requesterDid, opts.plcDirectoryUrl);
      records = (await listRBACRecords(pds, guest.requesterDid)).map((r) => r.value);
    } catch (err) {
      logger.warn("guest_identity_rbac_unreadable", { computevm: guest.computevm, requester: guest.requesterDid, error: String(err) });
    }
    const wishes = wishesOf(state, records);
    logger.info("guest_identity_synced", { computevm: guest.computevm, records: records?.length ?? null, wishes: wishes.length });
    const held = new Map<string, GuestIdentityGrant>();
    for (const g of (await grants.list()).items) if (g.spec?.computevm === guest.computevm) held.set(g.metadata?.name ?? "", g);
    const wanted = new Set<string>();
    const ownerReferences = guest.ownerUid ? [{
      apiVersion: `${COMPUTE_VM_RESOURCE.group}/${COMPUTE_VM_RESOURCE.version}`,
      kind: COMPUTE_VM_RESOURCE.kind,
      name: guest.computevm,
      uid: guest.ownerUid,
      blockOwnerDeletion: true,
    }] : undefined;
    for (const wish of wishes) {
      const name = await nameOf(guest.computevm, wish);
      wanted.add(name);
      const current = held.get(name);
      if (current && heldKey(current) === specKey(wish)) continue;
      await grants.apply({
        apiVersion: `${GUEST_IDENTITY_GRANT_RESOURCE.group}/${GUEST_IDENTITY_GRANT_RESOURCE.version}`,
        kind: GUEST_IDENTITY_GRANT_RESOURCE.kind,
        metadata: { name, ...(ownerReferences ? { ownerReferences } : {}) },
        spec: {
          computevm: guest.computevm,
          entity: `${GUEST_ENTITY_PREFIX}${guest.computevm}`,
          api: wish.api,
          actx: wish.actx,
          scopes: wish.scopes,
          ...(wish.claims.length ? { claims: wish.claims } : {}),
        },
      });
      logger.info("guest_identity_granted", { computevm: guest.computevm, api: wish.api, actx: wish.actx });
    }
    if (records) {
      for (const name of held.keys()) {
        if (wanted.has(name)) continue;
        await grants.delete(name);
        logger.info("guest_identity_revoked", { computevm: guest.computevm, grant: name });
      }
    }
    return records !== undefined;
  }

  async function run(state: GuestState, attempt = 0, strict = false): Promise<void> {
    if (state.running) {
      state.dirty = true;
      return;
    }
    state.running = true;
    let complete = false;
    try {
      do {
        state.dirty = false;
        try {
          complete = await syncOnce(state);
        } catch (err) {
          if (strict) throw err;
          logger.warn("guest_identity_sync_failed", { computevm: state.guest.computevm, error: String(err) });
          complete = false;
        }
      } while (state.dirty && !state.forgotten);
    } finally {
      state.running = false;
    }
    if (!complete && !state.forgotten && attempt + 1 < RETRY_ATTEMPTS) {
      state.timer = setTimeout(() => {
        run(state, attempt + 1).catch((err: unknown) =>
          logger.warn("guest_identity_sync_failed", { computevm: state.guest.computevm, error: String(err) })
        );
      }, RETRY_MS);
      Deno.unrefTimer(state.timer as unknown as number);
    }
  }

  return {
    async register(guest) {
      const state: GuestState = { guest, running: false, dirty: false, forgotten: false };
      guests.set(guest.computevm, state);
      await run(state, 0, true);
      state.poll = setInterval(() => {
        run(state).catch((err: unknown) =>
          logger.warn("guest_identity_sync_failed", { computevm: guest.computevm, error: String(err) })
        );
      }, opts.resyncMs ?? DEFAULT_RESYNC_MS);
      Deno.unrefTimer(state.poll as unknown as number);
    },
    forget(computevm) {
      const state = guests.get(computevm);
      if (!state) return;
      state.forgotten = true;
      if (state.timer !== undefined) clearTimeout(state.timer);
      if (state.poll !== undefined) clearInterval(state.poll);
      guests.delete(computevm);
    },
    requesterRbacChanged(requesterDid) {
      for (const state of guests.values()) {
        if (state.guest.requesterDid !== requesterDid) continue;
        run(state).catch((err: unknown) =>
          logger.warn("guest_identity_sync_failed", { computevm: state.guest.computevm, error: String(err) })
        );
      }
    },
  };
}
