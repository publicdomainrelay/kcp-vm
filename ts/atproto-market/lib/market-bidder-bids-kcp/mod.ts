import {
  acceptClaimable,
  BidCapacityError,
  bidExpiresAt,
  DEFAULT_TERMINATED_RECEIPTS_CAPACITY,
  type BidClaim,
  type BidDecision,
  type BidEntry,
  type BidLedger,
  type BidPhaseName,
  type BidRef,
  type ContractStore,
  type Tombstone,
} from "@publicdomainrelay/market-bidder-abc";
import { type Bid, BID_RESOURCE, COMPUTE_VM_RESOURCE, type BidStatus, parseBid } from "@publicdomainrelay/compute-provider-kcp-common";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import { isQuotaExceeded } from "@publicdomainrelay/kcp-client-common";

export const BID_NAME_PREFIX = "bid-";
export const BID_NAME_HEX = 20;
export const BID_API_VERSION = `${BID_RESOURCE.group}/${BID_RESOURCE.version}`;
export const DEFAULT_TOMBSTONE_TTL_MS = 30 * 24 * 60 * 60 * 1000;
export const TTL_ANNOTATION = "socialweb.computer/ttl-after-finished";
export const DEFAULT_BID_TTL = "24h";
export const DEFAULT_QUOTA_CACHE_MS = 2_000;

export async function bidNameOf(rfpUri: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(rfpUri)));
  const hex = Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
  return BID_NAME_PREFIX + hex.slice(0, BID_NAME_HEX);
}

function refOf(r: { uri: string; cid?: string }): BidRef {
  return r.cid ? { uri: r.uri, cid: r.cid } : { uri: r.uri };
}

function settledAtOf(status: BidStatus): string | undefined {
  let at: string | undefined;
  for (const c of status.conditions ?? []) {
    if ((c.type !== "Complete" && c.type !== "Failed") || c.status !== "True") continue;
    if (typeof c.lastTransitionTime === "string" && (at === undefined || c.lastTransitionTime > at)) at = c.lastTransitionTime;
  }
  return at;
}

export function bidEntryOf(bid: Bid): BidEntry {
  const s = bid.status ?? {};
  const spec = bid.spec!;
  const settledAt = settledAtOf(s);
  return {
    ...(settledAt ? { settledAt } : {}),
    name: bid.metadata?.name ?? "",
    rfp: refOf(spec.rfp),
    requester: spec.requester,
    expiresAt: spec.expiresAt,
    phase: (s.phase ?? "Deciding") as BidPhaseName,
    ...(s.bid ? { bid: refOf(s.bid) } : {}),
    ...(s.accept ? { accept: refOf(s.accept) } : {}),
    ...(s.claimedBy ? { claimedBy: s.claimedBy } : {}),
    ...(s.timings?.acceptedAt ? { acceptedAt: s.timings.acceptedAt } : {}),
    ...(s.computeVM ? { computeVM: s.computeVM } : {}),
    ...(s.timings?.publishedAt ? { publishedAt: s.timings.publishedAt } : {}),
    ...(s.terminated ? { terminated: tombstoneOf(s.terminated, s.accept) } : {}),
  };
}

function tombstoneOf(t: NonNullable<BidStatus["terminated"]>, accept: BidStatus["accept"]): Tombstone {
  return {
    receiptUri: t.receiptUri,
    ...(t.receiptCid ? { receiptCid: t.receiptCid } : {}),
    ...(accept ? { acceptUri: accept.uri, ...(accept.cid ? { acceptCid: accept.cid } : {}) } : {}),
    acceptAuthor: t.acceptAuthor,
    reason: t.reason,
    at: t.at,
  };
}

export function bidObjectOf(name: string, decision: BidDecision, windowMs?: number, ttl: string = DEFAULT_BID_TTL): Bid {
  return {
    apiVersion: BID_API_VERSION,
    kind: BID_RESOURCE.kind,
    metadata: { name, annotations: { [TTL_ANNOTATION]: ttl } },
    spec: {
      rfp: refOf(decision.rfp),
      requester: decision.requester,
      ...(decision.payloadNsid ? { payloadNsid: decision.payloadNsid } : {}),
      offer: decision.offer,
      provider: decision.provider,
      policy: {
        ...(decision.policy.scope ? { scope: decision.policy.scope } : {}),
        ...(decision.policy.attached?.length ? { attached: decision.policy.attached.map(refOf) } : {}),
        verdict: decision.policy.verdict,
      },
      expiresAt: bidExpiresAt(decision.decidedAt, windowMs),
    },
  };
}

export interface KcpBidLedgerOptions {
  kcp: KcpClient;
  workspace?: string;
  windowMs?: number;
  ttl?: string;
  quotaCacheMs?: number;
  bidQuota?: number;
  computeVMQuota?: number;
}

interface Pending {
  rfpSeenAt: string;
  decidedAt: string;
}

export function createKcpBidLedger(opts: KcpBidLedgerOptions): BidLedger {
  const bids = kcpResourceClient<Bid>(opts.kcp, { resource: BID_RESOURCE, workspace: opts.workspace }, parseBid);
  const pending = new Map<string, Pending>();

  async function transition(rfpUri: string, from: BidPhaseName[], next: (s: BidStatus) => BidStatus | null): Promise<void> {
    const name = await bidNameOf(rfpUri);
    await bids.patchStatus(name, (cur) => {
      const phase = (cur.status?.phase ?? "Deciding") as BidPhaseName;
      if (!from.includes(phase)) return null;
      return next(cur.status ?? {});
    });
  }

  function decisionTimings(name: string): Pending | Record<string, never> {
    const p = pending.get(name);
    pending.delete(name);
    return p ?? {};
  }


  let gauge: { at: number; full: string | undefined } | undefined;

  async function countOf(resource: typeof BID_RESOURCE): Promise<number> {
    const out = await opts.kcp.list({ resource, workspace: opts.workspace }, { limit: 1 });
    if (out.remainingItemCount !== undefined) return out.items.length + out.remainingItemCount;
    if (!out.continueToken) return out.items.length;
    return (await opts.kcp.list({ resource, workspace: opts.workspace })).items.length;
  }

  async function fullQuota(fresh = false): Promise<string | undefined> {
    const now = Date.now();
    if (!fresh && gauge && now - gauge.at < (opts.quotaCacheMs ?? DEFAULT_QUOTA_CACHE_MS)) return gauge.full;
    let full: string | undefined;
    try {
      for (const [limit, resource] of [[opts.bidQuota, BID_RESOURCE], [opts.computeVMQuota, COMPUTE_VM_RESOURCE]] as const) {
        if (!limit || limit <= 0) continue;
        if (await countOf(resource) >= limit) {
          full = `${resource.plural}.${resource.group}`;
          break;
        }
      }
    } catch {
      full = undefined;
    }
    gauge = { at: now, full };
    return full;
  }

  return {
    capacity: () => fullQuota(),
    async find(rfpUri) {
      const bid = await bids.get(await bidNameOf(rfpUri));
      return bid ? bidEntryOf(bid) : null;
    },
    async claim(decision): Promise<BidClaim> {
      const name = await bidNameOf(decision.rfp.uri);
      const full = await bids.get(name).then((existing) => existing ? undefined : fullQuota(true));
      if (full) throw new BidCapacityError(full);
      let out;
      try {
        out = await bids.createIfAbsent(bidObjectOf(name, decision, opts.windowMs, opts.ttl));
      } catch (err) {
        if (isQuotaExceeded(err)) throw new BidCapacityError(`${BID_RESOURCE.plural}.${BID_RESOURCE.group}`, err);
        throw err;
      }
      if (out.created) pending.set(name, { rfpSeenAt: decision.rfpSeenAt, decidedAt: decision.decidedAt });
      return { claimed: out.created, entry: bidEntryOf(out.object) } as BidClaim;
    },
    async published(rfpUri, bid, at) {
      const timings = decisionTimings(await bidNameOf(rfpUri));
      await transition(rfpUri, ["Deciding"], (s) => ({
        ...s,
        phase: "Published",
        message: "the market.bid record is published",
        bid: refOf(bid),
        timings: { ...s.timings, ...timings, publishedAt: at },
      }));
    },
    async failed(rfpUri, reason, at) {
      const timings = decisionTimings(await bidNameOf(rfpUri));
      await transition(rfpUri, ["Deciding"], (s) => ({
        ...s,
        phase: "Failed",
        message: reason.slice(0, 1_000),
        timings: { ...s.timings, ...timings, finishedAt: at },
      }));
    },
    async won(rfpUri, accept, at) {
      await transition(rfpUri, ["Deciding", "Published"], (s) => ({
        ...s,
        phase: "Won",
        message: "the requester accepted this bid",
        accept: refOf(accept),
        timings: { ...s.timings, acceptedAt: at },
      }));
    },
    async accepting(rfpUri, claim): Promise<BidClaim> {
      const name = await bidNameOf(rfpUri);
      let wrote = false;
      const out = await bids.patchStatus(name, (cur) => {
        wrote = false;
        if (!acceptClaimable(bidEntryOf(cur), claim)) return null;
        wrote = true;
        const s = cur.status ?? {};
        return {
          ...s,
          phase: "Won",
          message: `the requester accepted this bid; ${claim.claimant} starts the contract`,
          accept: refOf(claim.accept),
          claimedBy: claim.claimant,
          timings: { ...s.timings, acceptedAt: claim.at },
        };
      });
      if (!out) throw new Error(`Bid ${name} is gone`);
      return { claimed: wrote, entry: bidEntryOf(out) } as BidClaim;
    },
    async boundTo(rfpUri, computeVM) {
      await transition(rfpUri, ["Won"], (s) => s.computeVM === computeVM ? null : { ...s, computeVM });
    },
    async terminated(rfpUri, tombstone) {
      await transition(rfpUri, ["Won"], (s) => s.terminated?.receiptUri === tombstone.receiptUri ? null : {
        ...s,
        terminated: {
          receiptUri: tombstone.receiptUri,
          ...(tombstone.receiptCid ? { receiptCid: tombstone.receiptCid } : {}),
          acceptAuthor: tombstone.acceptAuthor,
          reason: tombstone.reason.slice(0, 1_000),
          at: tombstone.at,
        },
        timings: { ...s.timings, finishedAt: tombstone.at },
      });
    },
    async lost(rfpUri, reason, at) {
      await transition(rfpUri, ["Deciding", "Published", "Expired"], (s) => ({
        ...s,
        phase: "Lost",
        message: reason.slice(0, 1_000),
        timings: { ...s.timings, finishedAt: at },
      }));
    },
    async expired(rfpUri, at) {
      await transition(rfpUri, ["Published"], (s) => ({
        ...s,
        phase: "Expired",
        message: "no accept came inside the bid window",
        timings: { ...s.timings, finishedAt: at },
      }));
    },
    async release(rfpUri) {
      const name = await bidNameOf(rfpUri);
      const bid = await bids.get(name);
      if (!bid || (bid.status?.phase ?? "Deciding") !== "Deciding") return false;
      pending.delete(name);
      return await bids.delete(name, bid.metadata?.uid ? { uid: bid.metadata.uid, resourceVersion: bid.metadata.resourceVersion } : undefined);
    },
    async list() {
      const out = await bids.list();
      return out.items.map(bidEntryOf);
    },
  };
}

export interface BidTombstoneStoreOptions {
  ledger: BidLedger;
  logger: { info(message: string, data?: Record<string, unknown>): void; warn(message: string, data?: Record<string, unknown>): void };
  tombstoneTtlMs?: number;
  tombstoneCapacity?: number;
  now?: () => number;
}

export function createBidTombstoneStore(opts: BidTombstoneStoreOptions): Pick<ContractStore, "load" | "saveTombstone"> {
  const ttlMs = opts.tombstoneTtlMs ?? DEFAULT_TOMBSTONE_TTL_MS;
  const capacity = opts.tombstoneCapacity ?? DEFAULT_TERMINATED_RECEIPTS_CAPACITY;
  const now = opts.now ?? Date.now;
  return {
    async load() {
      const cutoff = now() - ttlMs;
      const tombstones = (await opts.ledger.list())
        .flatMap((e) => e.terminated ? [e.terminated] : [])
        .filter((t) => Date.parse(t.at) >= cutoff)
        .sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
      return { contracts: [], tombstones: tombstones.slice(Math.max(0, tombstones.length - capacity)) };
    },
    async saveTombstone(tombstone) {
      if (!tombstone.acceptUri) {
        opts.logger.warn("bidder_tombstone_without_accept", { receiptUri: tombstone.receiptUri });
        return;
      }
      const bid = (await opts.ledger.list()).find((e) =>
        e.accept !== undefined && e.accept.uri === tombstone.acceptUri &&
        (!tombstone.acceptCid || !e.accept.cid || e.accept.cid === tombstone.acceptCid)
      );
      if (!bid) {
        opts.logger.warn("bidder_tombstone_without_bid", { receiptUri: tombstone.receiptUri, acceptUri: tombstone.acceptUri });
        return;
      }
      await opts.ledger.terminated(bid.rfp.uri, tombstone);
      opts.logger.info("bidder_tombstone_on_bid", { bid: bid.name, receiptUri: tombstone.receiptUri, reason: tombstone.reason });
    },
  };
}
