/**
 * PolicyEvalCtxImpl — a standalone, self-contained implementation of the
 * `PolicyEvalCtx` shape that a TrustPolicy.evaluate() / WorkPolicy.evaluate()
 * receives. All host-brokered capabilities are implemented against live
 * ATProto network data, the same way the sandbox host bridge does:
 *
 *   resolve(ref)            → com.atproto.repo.getRecord on the strongRef uri
 *   resolveOperatorDid(did) → badgeBlueKeys scan for bidder/requester_associate
 *   getVouchedDids(did)     → sh.tangled.graph.vouch listing (rkey = vouched did)
 *
 * There is no single `service`: the PDS for every repo is derived from the DID
 * via the PLC directory (`IdResolver` + `getPdsEndpoint`, mirroring
 * atproto-market's `listRecordsPublic`), and public reads are plain HTTP
 * fetches — no auth needed. Every method stays overridable (pass
 * resolve/resolveOperatorDid/getVouchedDids in the constructor) so
 * offline/unit scenarios can still inject plain data.
 *
 * `createPolicyCtx` is the record-centric entrypoint: it takes the market
 * records the caller has at its stage (`rfp`/`bid`/`accept`, each optional),
 * hydrates any missing record bodies over the network (cached under their
 * strongRef), and derives `subjectDid` / `rootRequesterDid` / `demand` /
 * `offer` from which records are present.
 */

import { getPdsEndpoint } from "@atproto/common-web";
import { IdResolver } from "@atproto/identity";
import {
  recordCacheKey,
  RECORD_VALUE_FILE,
  type CacheStore,
} from "./cache.ts";
import {
  BADGE_BLUE_KEYS_NSID,
  BIDDER_ASSOCIATE,
  REQUESTER_ASSOCIATE,
  VOUCH_NSID,
  splitAtUri,
} from "./constants.ts";
import type {
  Policy,
  PolicyArgs,
  PolicyEvalCtx,
  PolicyPerspective,
  PolicyRecord,
  PolicyResult,
  StrongRef,
} from "./types.ts";

export interface PolicyEvalCtxImplOpts {
  policyName?: string;
  args?: PolicyArgs;
  perspective?: PolicyPerspective;
  selfDid: string;
  /** Explicit overrides — normally derived from the records by createPolicyCtx. */
  subjectDid?: string;
  rootRequesterDid?: string;
  counterpartyDid?: string;
  policyRef?: StrongRef;
  demand?: PolicyEvalCtx["demand"];
  offer?: PolicyEvalCtx["offer"];
  /** The market records present at this stage (each optional). */
  rfp?: PolicyRecord;
  bid?: PolicyRecord;
  accept?: PolicyRecord;
  /** Cache store for record resolutions (rec/<uri>/<cid>) and verdict memoization. */
  store?: CacheStore;
  /** PLC directory URL for DID→PDS resolution. Default: https://plc.directory. */
  plcUrl?: string;
  /** Override resolve entirely. Default: getRecord against the repo's PDS, cached. */
  resolve?: (ref: StrongRef) => Promise<Record<string, unknown>>;
  /** Override resolveOperatorDid entirely. Default: badgeBlueKeys scan via the repo's PDS. */
  resolveOperatorDid?: (did: string) => Promise<string | null>;
  /** Override getVouchedDids entirely. Default: vouch listing via the repo's PDS. */
  getVouchedDids?: (did: string) => Promise<Set<string>>;
  /** Override the default structured logger. */
  log?: (level: string, msg: string, meta?: Record<string, unknown>) => void;
}

export class PolicyEvalCtxImpl implements PolicyEvalCtx {
  readonly policyName: string;
  readonly args: PolicyArgs;
  readonly perspective: PolicyPerspective;
  readonly selfDid: string;
  readonly counterpartyDid: string;
  readonly subjectDid: string;
  readonly rootRequesterDid: string;
  readonly policyRef?: StrongRef;
  readonly demand?: PolicyEvalCtx["demand"];
  readonly offer?: PolicyEvalCtx["offer"];
  readonly rfp?: PolicyRecord;
  readonly bid?: PolicyRecord;
  readonly accept?: PolicyRecord;

  readonly #resolver: IdResolver;
  readonly #store: CacheStore | undefined;
  readonly #resolve: (ref: StrongRef) => Promise<Record<string, unknown>>;
  readonly #resolveOperatorDid: (did: string) => Promise<string | null>;
  readonly #getVouchedDids: (did: string) => Promise<Set<string>>;
  readonly #log: ((level: string, msg: string, meta?: Record<string, unknown>) => void) | undefined;

  constructor(opts: PolicyEvalCtxImplOpts) {
    const self = opts.selfDid;
    this.policyName = opts.policyName ?? "unset";
    this.args = opts.args ?? {};
    this.perspective = opts.perspective ?? "requester";
    this.selfDid = self;
    this.subjectDid = opts.subjectDid ?? "";
    this.counterpartyDid = opts.counterpartyDid ?? opts.subjectDid ?? self;
    this.rootRequesterDid = opts.rootRequesterDid ?? "";
    this.policyRef = opts.policyRef;
    this.demand = opts.demand;
    this.offer = opts.offer;
    this.rfp = opts.rfp;
    this.bid = opts.bid;
    this.accept = opts.accept;

    this.#resolver = new IdResolver({ plcUrl: opts.plcUrl ?? "https://plc.directory" });
    this.#store = opts.store;
    this.#log = opts.log;

    this.#resolve = opts.resolve ?? ((ref) => this.#resolveCached(ref));
    this.#resolveOperatorDid = opts.resolveOperatorDid ?? ((did) => this.#resolveOperatorFromBadgeBlueKeys(did));
    this.#getVouchedDids = opts.getVouchedDids ?? ((did) => this.#vouchedDidsFromGraph(did));
  }

  /** Satisfies PolicyEvalCtx.resolve — getRecord on the strongRef, cached under rec/<uri>/<cid> when a store is present. */
  async resolve(ref: StrongRef): Promise<Record<string, unknown>> {
    return this.#resolve(ref);
  }

  /** Satisfies PolicyEvalCtx.resolveOperatorDid — operator of a DID via badgeBlueKeys. */
  async resolveOperatorDid(did: string): Promise<string | null> {
    return this.#resolveOperatorDid(did);
  }

  /** Satisfies PolicyEvalCtx.getVouchedDids — vouch/follow set via the Tangled graph. */
  async getVouchedDids(did: string): Promise<Set<string>> {
    return this.#getVouchedDids(did);
  }

  /** Satisfies PolicyEvalCtx.log — JSON structured, always stderr so callers
   * (the raw-deno CLI, the gha-lite actions) keep stdout for their machine
   * readable output. In the gha-lite net-only worker console.* are shimmed to
   * the same channel, so the distinction is harmless there. */
  log(level: string, msg: string, meta?: Record<string, unknown>): void {
    this.#emit(level, msg, meta);
  }

  /** Run any policy factory (the sibling ${policyName}.ts files) against this ctx. */
  async evaluate(policy: Policy): Promise<PolicyResult> {
    return policy.evaluate(this);
  }

  /** Route through the injected logger when present, else JSON-stderr. */
  #emit(level: string, msg: string, meta?: Record<string, unknown>): void {
    if (this.#log) {
      this.#log(level, msg, meta);
      return;
    }
    const line = JSON.stringify({ ts: new Date().toISOString(), level, msg, ...(meta ?? {}) });
    console.error(line);
  }

  // ── Live ATProto reads (plain HTTP, DID→PDS resolved per repo) ────────────

  /** Resolve a DID to its PDS service endpoint via the PLC directory. */
  async #pdsFor(did: string): Promise<string> {
    const doc = await this.#resolver.did.resolve(did);
    const pds = doc ? getPdsEndpoint(doc) : undefined;
    if (!pds) throw new Error(`no PDS endpoint for ${did}`);
    return pds;
  }

  /** getRecord against the repo's PDS, memoized in the store under rec/<uri>/<cid>. */
  async #resolveCached(ref: StrongRef): Promise<Record<string, unknown>> {
    const store = this.#store;
    if (store) {
      const key = recordCacheKey(ref);
      const entry = store.get(key);
      const file = entry?.[RECORD_VALUE_FILE];
      if (file) {
        try {
          return JSON.parse(file.data) as Record<string, unknown>;
        } catch {
          // corrupt — refetch
        }
      }
      const value = await this.#getRecord(ref);
      store.set(key, { [RECORD_VALUE_FILE]: { data: JSON.stringify(value), encoding: "text" } });
      return value;
    }
    return this.#getRecord(ref);
  }

  /** com.atproto.repo.getRecord via the repo's PDS. */
  async #getRecord(ref: StrongRef): Promise<Record<string, unknown>> {
    const { repo, collection, rkey } = splitAtUri(ref.uri);
    const pds = await this.#pdsFor(repo);
    const url = new URL(`${pds}/xrpc/com.atproto.repo.getRecord`);
    url.searchParams.set("repo", repo);
    url.searchParams.set("collection", collection);
    url.searchParams.set("rkey", rkey);
    const res = await fetch(url);
    if (!res.ok) throw new Error(`getRecord ${ref.uri} failed: ${res.status}`);
    const data = await res.json() as { value?: unknown };
    return (data.value ?? {}) as Record<string, unknown>;
  }

  /** Paginated com.atproto.repo.listRecords via the repo's PDS (PDS caps pages at 100). */
  async #listRecords(
    repo: string,
    collection: string,
    limit = 100,
  ): Promise<Array<{ uri: string; value: Record<string, unknown> }>> {
    const pds = await this.#pdsFor(repo);
    const all: Array<{ uri: string; value: Record<string, unknown> }> = [];
    let cursor: string | undefined;
    const pageLimit = Math.min(limit, 100);
    do {
      const url = new URL(`${pds}/xrpc/com.atproto.repo.listRecords`);
      url.searchParams.set("repo", repo);
      url.searchParams.set("collection", collection);
      url.searchParams.set("limit", String(pageLimit));
      if (cursor) url.searchParams.set("cursor", cursor);
      const res = await fetch(url);
      if (!res.ok) break;
      const data = await res.json() as { records: Array<{ uri: string; value: unknown }>; cursor?: string };
      for (const r of data.records) {
        all.push({ uri: r.uri, value: r.value as Record<string, unknown> });
      }
      cursor = data.cursor;
    } while (cursor && all.length < limit);
    return all.slice(0, limit);
  }

  /**
   * Operator of a DID: a badgeBlueKeys record whose challenge is the subject's
   * own DID and whose keyId is the operator. Bidders mint bidder_associate,
   * requesters mint requester_associate — both point at the same operator, so
   * either resolves it. A subject with no association has no separate operator.
   */
  async #resolveOperatorFromBadgeBlueKeys(did: string): Promise<string | null> {
    try {
      const records = await this.#listRecords(did, BADGE_BLUE_KEYS_NSID, 100);
      for (const r of records) {
        const v = r.value;
        if (v.challenge !== did) continue;
        const service = v.service;
        if (service !== BIDDER_ASSOCIATE && service !== REQUESTER_ASSOCIATE) continue;
        const keyId = v.keyId;
        if (typeof keyId === "string" && keyId.startsWith("did:")) return keyId;
      }
    } catch (err) {
      this.#emit("warn", "resolveOperatorDid failed", { did, error: String(err) });
    }
    return null;
  }

  /** Vouch set: sh.tangled.graph.vouch records whose rkey is the vouched DID. */
  async #vouchedDidsFromGraph(did: string): Promise<Set<string>> {
    const vouched = new Set<string>();
    try {
      const records = await this.#listRecords(did, VOUCH_NSID, 100);
      for (const r of records) {
        const v = r.value;
        if (v.kind === "denounce") continue;
        const rkey = r.uri.split("/").pop() ?? "";
        if (rkey.startsWith("did:")) vouched.add(rkey);
      }
    } catch (err) {
      this.#emit("warn", "getVouchedDids failed", { did, error: String(err) });
    }
    return vouched;
  }

  toJSON(): Record<string, unknown> {
    return {
      policyName: this.policyName,
      args: this.args,
      perspective: this.perspective,
      selfDid: this.selfDid,
      counterpartyDid: this.counterpartyDid,
      subjectDid: this.subjectDid,
      rootRequesterDid: this.rootRequesterDid,
      policyRef: this.policyRef,
      demand: this.demand,
      offer: this.offer,
      rfp: this.rfp,
      bid: this.bid,
      accept: this.accept,
    };
  }
}

// ── Record-centric factory ───────────────────────────────────────────────────

export interface CreatePolicyCtxInput {
  policyName?: string;
  args?: PolicyArgs;
  perspective: PolicyPerspective;
  /** The evaluator's own DID — the one thing no market record names. */
  selfDid: string;
  /** The market records present at this stage (each optional). */
  rfp?: PolicyRecord | null;
  bid?: PolicyRecord | null;
  accept?: PolicyRecord | null;
  policyRef?: StrongRef;
  /** Cache store for record resolutions + verdict memoization. */
  store?: CacheStore;
  /** PLC directory URL for DID→PDS resolution. Default: https://plc.directory. */
  plcUrl?: string;
  resolve?: (ref: StrongRef) => Promise<Record<string, unknown>>;
  resolveOperatorDid?: (did: string) => Promise<string | null>;
  getVouchedDids?: (did: string) => Promise<Set<string>>;
  log?: (level: string, msg: string, meta?: Record<string, unknown>) => void;
}

function authorOf(r: { uri: string } | undefined): string {
  return r ? splitAtUri(r.uri).repo : "";
}

/** The counterparty being evaluated: the bidder on the requester side, the requester on the bidder side. */
function deriveSubject(
  perspective: PolicyPerspective,
  rfp?: PolicyRecord,
  bid?: PolicyRecord,
  accept?: PolicyRecord,
): string {
  if (perspective === "requester") return authorOf(bid);
  return authorOf(rfp ?? accept);
}

function deriveRootRequester(perspective: PolicyPerspective, selfDid: string, subjectDid: string): string {
  return perspective === "requester" ? selfDid : subjectDid;
}

/** The bidder-side workload, from the RFP record's payload strongRef. */
function deriveDemand(rfp?: PolicyRecord): PolicyEvalCtx["demand"] {
  if (!rfp) return undefined;
  const payload = rfp.value?.payload as StrongRef | undefined;
  if (!payload || !payload.uri || !payload.cid) return undefined;
  return {
    rfpRef: { uri: rfp.uri, cid: rfp.cid },
    payloadRef: payload,
    payloadNsid: splitAtUri(payload.uri).collection,
    payload: rfp.payload,
  };
}

/** The requester-side bid, from the bid record's payload strongRef. */
function deriveOffer(bid?: PolicyRecord): PolicyEvalCtx["offer"] {
  if (!bid) return undefined;
  const payload = bid.value?.payload as StrongRef | undefined;
  if (!payload || !payload.uri || !payload.cid) return undefined;
  return {
    bidRef: { uri: bid.uri, cid: bid.cid },
    payloadRef: payload,
    payloadNsid: splitAtUri(payload.uri).collection,
    payload: bid.payload,
  };
}

/**
 * Build a PolicyEvalCtx from the records present at the caller's stage.
 * Hydrates any record without a `value` over the network (cached under its
 * strongRef when a store is supplied), then derives subjectDid /
 * rootRequesterDid / demand / offer from which records exist.
 */
export async function createPolicyCtx(input: CreatePolicyCtxInput): Promise<PolicyEvalCtxImpl> {
  // A scratch impl provides the (caching) resolve used to hydrate records.
  const scratch = new PolicyEvalCtxImpl({
    policyName: input.policyName,
    args: input.args,
    perspective: input.perspective,
    selfDid: input.selfDid,
    plcUrl: input.plcUrl,
    store: input.store,
    resolve: input.resolve,
    resolveOperatorDid: input.resolveOperatorDid,
    getVouchedDids: input.getVouchedDids,
    log: input.log,
  });

  const hydrate = async (r?: PolicyRecord | null): Promise<PolicyRecord | undefined> => {
    if (!r) return undefined;
    return r.value ? r : { ...r, value: await scratch.resolve({ uri: r.uri, cid: r.cid }) };
  };
  const rfp = await hydrate(input.rfp);
  const bid = await hydrate(input.bid);
  const accept = await hydrate(input.accept);

  const subjectDid = deriveSubject(input.perspective, rfp, bid, accept);
  const rootRequesterDid = deriveRootRequester(input.perspective, input.selfDid, subjectDid);

  return new PolicyEvalCtxImpl({
    policyName: input.policyName,
    args: input.args,
    perspective: input.perspective,
    selfDid: input.selfDid,
    subjectDid,
    rootRequesterDid,
    counterpartyDid: subjectDid,
    policyRef: input.policyRef,
    demand: deriveDemand(rfp),
    offer: deriveOffer(bid),
    rfp,
    bid,
    accept,
    plcUrl: input.plcUrl,
    store: input.store,
    resolve: input.resolve,
    resolveOperatorDid: input.resolveOperatorDid,
    getVouchedDids: input.getVouchedDids,
    log: input.log,
  });
}
