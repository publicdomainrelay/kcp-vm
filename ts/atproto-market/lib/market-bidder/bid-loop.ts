import type { DispatchRfpInput, RfpGate } from "@publicdomainrelay/market-atproto";
import type { MarketLoopRecorder } from "@publicdomainrelay/market-abc";
import type { BidClaim, BidEntry, BidLedger, BidRef } from "@publicdomainrelay/market-bidder-abc";
import { bidOpen } from "@publicdomainrelay/market-bidder-abc";
import type { HandlerResult } from "@publicdomainrelay/market-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export interface OwnBidRecord {
  uri: string;
  cid: string;
  rfpUri?: string;
}

export const ACCEPT_CLAIM_STALE_MS = 30_000;

export interface BidLoopOptions {
  selfDid: string;
  claimant: string;
  claimStaleMs?: number;
  bids?: BidLedger;
  loop?: MarketLoopRecorder;
  logger: StructuredLoggerInterface;
  listOwnBids(): Promise<OwnBidRecord[]>;
  knownAccept(acceptUri: string, acceptCid: string): boolean;
  ownReceiptFor(acceptUri: string): Promise<OwnReceipt | null>;
}

export interface OwnReceipt {
  uri: string;
  ended?: string;
}

export interface AcceptRefs {
  acceptUri: string;
  acceptCid: string;
  issuerDid: string;
  rfp?: { uri: string };
  bid?: { uri: string };
}

export interface BidLoop {
  readonly ledger: BidLedger | undefined;
  rfpGate: RfpGate;
  restore(): Promise<void>;
  checkAccept(accept: AcceptRefs): Promise<HandlerResult | undefined>;
  acceptSeen(accept: AcceptRefs): Promise<"ours" | "lost" | "unknown">;
  close(): void;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

const nowIso = () => new Date().toISOString();

export function createBidLoop(opts: BidLoopOptions): BidLoop {
  const { bids, loop, logger } = opts;
  const rfpsInFlight = new Map<string, Promise<void>>();
  const expiry = new Map<string, ReturnType<typeof setTimeout>>();

  const findBid = async (rfpUri: string): Promise<BidEntry | null> => {
    if (!bids) return null;
    try {
      return await bids.find(rfpUri);
    } catch (err) {
      logger.warn("bid_read_failed", { rfpUri, error: String(err) });
      return null;
    }
  };

  const expireAt = (rfpUri: string, expiresAt: string): void => {
    if (!bids || expiry.has(rfpUri)) return;
    const timer = setTimeout(() => {
      expiry.delete(rfpUri);
      bids.expired(rfpUri, nowIso()).then(
        () => logger.info("bid_expired", { rfpUri, expiresAt }),
        (err: unknown) => logger.warn("bid_status_failed", { rfpUri, error: String(err) }),
      );
    }, Math.max(0, Date.parse(expiresAt) - Date.now()));
    Deno.unrefTimer(timer);
    expiry.set(rfpUri, timer);
  };

  const forgetExpiry = (rfpUri: string): void => {
    const timer = expiry.get(rfpUri);
    if (timer === undefined) return;
    clearTimeout(timer);
    expiry.delete(rfpUri);
  };

  const ledger: BidLedger | undefined = bids
    ? {
      ...bids,
      async claim(decision) {
        const out = await bids.claim(decision);
        if (out.claimed) expireAt(decision.rfp.uri, out.entry.expiresAt);
        return out;
      },
      async won(rfpUri, accept, at) {
        forgetExpiry(rfpUri);
        await bids.won(rfpUri, accept, at);
      },
      async accepting(rfpUri, claim) {
        const out = await bids.accepting(rfpUri, claim);
        if (out.claimed) forgetExpiry(rfpUri);
        return out;
      },
      async lost(rfpUri, reason, at) {
        forgetExpiry(rfpUri);
        await bids.lost(rfpUri, reason, at);
      },
      async failed(rfpUri, reason, at) {
        forgetExpiry(rfpUri);
        await bids.failed(rfpUri, reason, at);
      },
    }
    : undefined;

  const duplicate = (input: DispatchRfpInput, why: string, entry?: BidEntry | null): Response => {
    logger.info("rfp_duplicate_delivery", {
      rfpUri: input.rfpUri, via: input.req ? "push" : "firehose", why, bid: entry?.name, phase: entry?.phase,
    });
    return json({ ok: true, duplicate: true, ...(entry?.bid ? { bidUri: entry.bid.uri, bidCid: entry.bid.cid } : {}) });
  };

  const rfpGate: RfpGate = async (input, run) => {
    const running = rfpsInFlight.get(input.rfpUri);
    if (running) {
      await running;
      const declined = loop?.recentlyDeclined?.(input.rfpUri);
      return declined ? duplicate(input, `declined: ${declined.reason}`) : duplicate(input, "in flight", await findBid(input.rfpUri));
    }
    const done = Promise.withResolvers<void>();
    rfpsInFlight.set(input.rfpUri, done.promise);
    try {
      const declined = loop?.recentlyDeclined?.(input.rfpUri);
      if (declined) return duplicate(input, `declined at ${declined.at}: ${declined.reason}`);
      const existing = await findBid(input.rfpUri);
      if (existing) return duplicate(input, "a Bid answers it", existing);
      return await run();
    } finally {
      rfpsInFlight.delete(input.rfpUri);
      done.resolve();
    }
  };

  async function restore(): Promise<void> {
    if (!bids) return;
    const now = new Date();
    const entries = await bids.list();
    let own: Map<string, BidRef> | undefined;
    const ownBids = async () => {
      if (own) return own;
      own = new Map();
      for (const r of await opts.listOwnBids()) if (r.rfpUri) own.set(r.rfpUri, { uri: r.uri, cid: r.cid });
      return own;
    };
    const counts = { waiting: 0, recovered: 0, released: 0, expired: 0, failed: 0, settled: 0 };
    for (const e of entries) {
      try {
        if (e.phase === "Deciding") {
          const published = (await ownBids()).get(e.rfp.uri);
          if (published) {
            await bids.published(e.rfp.uri, published, nowIso());
            counts.recovered++;
            logger.info("bid_recovered", { bid: e.name, rfpUri: e.rfp.uri, bidUri: published.uri });
            if (bidOpen({ ...e, phase: "Published" }, now)) expireAt(e.rfp.uri, e.expiresAt);
            else await bids.expired(e.rfp.uri, nowIso());
          } else if (bidOpen(e, now)) {
            await bids.release(e.rfp.uri);
            counts.released++;
            logger.info("bid_released_for_replay", { bid: e.name, rfpUri: e.rfp.uri });
          } else {
            await bids.failed(e.rfp.uri, "the bidder stopped before it published the market.bid record", nowIso());
            counts.failed++;
          }
        } else if (e.phase === "Published") {
          if (bidOpen(e, now)) {
            expireAt(e.rfp.uri, e.expiresAt);
            counts.waiting++;
          } else {
            await bids.expired(e.rfp.uri, nowIso());
            counts.expired++;
          }
        } else {
          counts.settled++;
        }
      } catch (err) {
        logger.warn("bid_restore_failed", { bid: e.name, rfpUri: e.rfp.uri, error: String(err) });
      }
    }
    logger.info("bids_restored", { bids: entries.length, ...counts });
  }

  async function contractEnded(accept: AcceptRefs): Promise<HandlerResult | undefined> {
    let prior: OwnReceipt | null;
    try {
      prior = await opts.ownReceiptFor(accept.acceptUri);
    } catch (err) {
      logger.warn("accept_receipt_lookup_failed", { acceptUri: accept.acceptUri, error: String(err) });
      return { status: 503, body: { error: "RepoUnavailable", message: "the bidder could not read its own receipts" } };
    }
    if (!prior) return undefined;
    logger.warn("accept_contract_ended", { acceptUri: accept.acceptUri, receiptUri: prior.uri, ended: prior.ended });
    return {
      status: 409,
      body: {
        error: "ContractEnded",
        message: `receipt ${prior.uri} already answers this accept${prior.ended ? `; the contract ended: ${prior.ended}` : ""}`,
      },
    };
  }

  async function checkAccept(accept: AcceptRefs): Promise<HandlerResult | undefined> {
    if (opts.knownAccept(accept.acceptUri, accept.acceptCid)) return undefined;
    if (!bids || !accept.rfp?.uri) return await contractEnded(accept);
    if (accept.bid?.uri && !accept.bid.uri.startsWith(`at://${opts.selfDid}/`)) {
      logger.warn("accept_names_other_bidder", { acceptUri: accept.acceptUri, named: accept.bid.uri });
      return { status: 409, body: { error: "UnknownBid", message: "the accept names a bid of another bidder" } };
    }
    const entry = await findBid(accept.rfp.uri);
    if (!entry) {
      logger.warn("accept_without_bid", { acceptUri: accept.acceptUri, rfpUri: accept.rfp.uri });
      return { status: 404, body: { error: "UnknownBid", message: "this bidder holds no Bid for the accepted RFP" } };
    }
    if (entry.requester !== accept.issuerDid) {
      logger.warn("accept_from_other_requester", { acceptUri: accept.acceptUri, bid: entry.name, issuerDid: accept.issuerDid });
      return { status: 403, body: { error: "Forbidden", message: "the accept's author did not post the RFP" } };
    }
    if (accept.bid?.uri && entry.bid && entry.bid.uri !== accept.bid.uri) {
      logger.warn("accept_names_other_bid", { acceptUri: accept.acceptUri, bid: entry.name, named: accept.bid.uri, ours: entry.bid.uri });
      return { status: 409, body: { error: "UnknownBid", message: "the accept names a bid this bidder did not publish" } };
    }
    if (entry.phase === "Won" && entry.accept?.uri !== accept.acceptUri) return wonByOther(accept, entry);
    if (entry.phase !== "Won" && !bidOpen(entry, new Date())) return bidClosed(accept, entry);
    const ended = await contractEnded(accept);
    if (ended) return ended;
    return await claimAccept(accept);
  }

  function wonByOther(accept: AcceptRefs, entry: BidEntry): HandlerResult {
    logger.warn("accept_for_won_bid", { acceptUri: accept.acceptUri, bid: entry.name, accepted: entry.accept?.uri });
    return {
      status: 409,
      body: { error: "ContractEnded", message: `the Bid is Won by accept ${entry.accept?.uri}` },
    };
  }

  function bidClosed(accept: AcceptRefs, entry: BidEntry): HandlerResult {
    logger.warn("accept_for_closed_bid", { acceptUri: accept.acceptUri, bid: entry.name, phase: entry.phase, expiresAt: entry.expiresAt });
    return {
      status: 409,
      body: { error: "BidClosed", message: `the Bid is ${entry.phase}; its window ended at ${entry.expiresAt}` },
    };
  }

  async function claimAccept(accept: AcceptRefs): Promise<HandlerResult | undefined> {
    const rfpUri = accept.rfp!.uri;
    const now = Date.now();
    let out: BidClaim;
    try {
      out = await ledger!.accepting(rfpUri, {
        accept: { uri: accept.acceptUri, cid: accept.acceptCid },
        claimant: opts.claimant,
        at: new Date(now).toISOString(),
        staleBefore: new Date(now - (opts.claimStaleMs ?? ACCEPT_CLAIM_STALE_MS)).toISOString(),
      });
    } catch (err) {
      logger.warn("accept_claim_failed", { acceptUri: accept.acceptUri, rfpUri, error: String(err) });
      return { status: 503, body: { error: "BidUnavailable", message: "the bidder could not write its Bid" } };
    }
    const e = out.entry;
    if (out.claimed) {
      logger.info("accept_claimed", { acceptUri: accept.acceptUri, bid: e.name, claimant: opts.claimant });
      return undefined;
    }
    if (e.phase === "Won" && e.accept?.uri === accept.acceptUri) {
      logger.info("accept_in_progress", { acceptUri: accept.acceptUri, bid: e.name, claimedBy: e.claimedBy, since: e.acceptedAt });
      return {
        status: 503,
        body: {
          error: "AcceptInProgress",
          message: `${e.claimedBy ?? "another bidder process"} is starting the contract for this accept since ${e.acceptedAt}`,
        },
      };
    }
    if (e.phase === "Won") return wonByOther(accept, e);
    return bidClosed(accept, e);
  }

  async function acceptSeen(accept: AcceptRefs): Promise<"ours" | "lost" | "unknown"> {
    if (!bids || !accept.rfp?.uri) return "unknown";
    const entry = await findBid(accept.rfp.uri);
    if (!entry) return "unknown";
    const otherBidder = !!accept.bid?.uri && !accept.bid.uri.startsWith(`at://${opts.selfDid}/`);
    if (accept.bid?.uri && (otherBidder || (entry.bid && entry.bid.uri !== accept.bid.uri))) {
      await ledger!.lost(accept.rfp.uri, `the requester accepted ${accept.bid.uri}`, nowIso()).catch((err: unknown) =>
        logger.warn("bid_status_failed", { rfpUri: accept.rfp?.uri, error: String(err) })
      );
      logger.info("bid_lost", { bid: entry.name, rfpUri: accept.rfp.uri, accepted: accept.bid.uri });
      return "lost";
    }
    return "ours";
  }

  function close(): void {
    for (const timer of expiry.values()) clearTimeout(timer);
    expiry.clear();
  }

  return { ledger, rfpGate, restore, checkAccept, acceptSeen, close };
}
