import {
  bidExpiresAt,
  type BidClaim,
  type BidDecision,
  type BidEntry,
  type BidLedger,
  type BidPhaseName,
  type BidRef,
} from "@publicdomainrelay/market-bidder-abc";
import { type Bid, BID_RESOURCE, type BidStatus, parseBid } from "@publicdomainrelay/compute-provider-kcp-common";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";

export const BID_NAME_PREFIX = "bid-";
export const BID_NAME_HEX = 20;
export const BID_API_VERSION = `${BID_RESOURCE.group}/${BID_RESOURCE.version}`;

export async function bidNameOf(rfpUri: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(rfpUri)));
  const hex = Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
  return BID_NAME_PREFIX + hex.slice(0, BID_NAME_HEX);
}

function refOf(r: { uri: string; cid?: string }): BidRef {
  return r.cid ? { uri: r.uri, cid: r.cid } : { uri: r.uri };
}

export function bidEntryOf(bid: Bid): BidEntry {
  const s = bid.status ?? {};
  const spec = bid.spec!;
  return {
    name: bid.metadata?.name ?? "",
    rfp: refOf(spec.rfp),
    requester: spec.requester,
    expiresAt: spec.expiresAt,
    phase: (s.phase ?? "Deciding") as BidPhaseName,
    ...(s.bid ? { bid: refOf(s.bid) } : {}),
    ...(s.accept ? { accept: refOf(s.accept) } : {}),
    ...(s.computeVM ? { computeVM: s.computeVM } : {}),
    ...(s.timings?.publishedAt ? { publishedAt: s.timings.publishedAt } : {}),
  };
}

export function bidObjectOf(name: string, decision: BidDecision, windowMs?: number): Bid {
  return {
    apiVersion: BID_API_VERSION,
    kind: BID_RESOURCE.kind,
    metadata: { name },
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

  return {
    async find(rfpUri) {
      const bid = await bids.get(await bidNameOf(rfpUri));
      return bid ? bidEntryOf(bid) : null;
    },
    async claim(decision): Promise<BidClaim> {
      const name = await bidNameOf(decision.rfp.uri);
      const out = await bids.createIfAbsent(bidObjectOf(name, decision, opts.windowMs));
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
      await transition(rfpUri, ["Deciding", "Published", "Expired"], (s) => ({
        ...s,
        phase: "Won",
        message: "the requester accepted this bid",
        accept: refOf(accept),
        timings: { ...s.timings, acceptedAt: at },
      }));
    },
    async boundTo(rfpUri, computeVM) {
      await transition(rfpUri, ["Won"], (s) => s.computeVM === computeVM ? null : { ...s, computeVM });
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
    async list() {
      const out = await bids.list();
      return out.items.map(bidEntryOf);
    },
  };
}
