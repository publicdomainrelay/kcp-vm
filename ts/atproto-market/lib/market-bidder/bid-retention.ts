import type { BidEntry, BidLedger } from "@publicdomainrelay/market-bidder-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { isRateLimitError } from "@publicdomainrelay/pds-write-budget-abc";

export const DEFAULT_BID_RECORD_TTL_MS = 24 * 60 * 60 * 1000;
export const DEFAULT_BID_RETENTION_SCAN_MS = 10 * 60 * 1000;
export const DEFAULT_BID_RETENTION_PAGES = 50;
export const DEFAULT_BID_RETENTION_FIRST_MS = 30_000;
export const DEFAULT_BID_RETENTION_DELETES = 100;

export interface OwnBidRecordPage {
  records: Array<{ uri: string; cid: string; value: Record<string, unknown> }>;
  cursor?: string;
}

export const RECEIPT_COLLECTION = "com.publicdomainrelay.temp.market.receipt";
export const EVENT_COLLECTION = "com.publicdomainrelay.temp.market.event";

export interface BidRetentionOptions {
  bids: BidLedger;
  selfDid: string;
  ttlMs?: number;
  intervalMs?: number;
  maxPages?: number;
  maxDeletes?: number;
  listOwnBidRecords(cursor: string | undefined): Promise<OwnBidRecordPage>;
  listOwnRecords?(collection: string, cursor: string | undefined): Promise<OwnBidRecordPage>;
  deleteRecord(collection: string, rkey: string): Promise<void>;
  keepBidUri?(uri: string): boolean;
  sharedUris?(): ReadonlySet<string>;
  logger: StructuredLoggerInterface;
  now?: () => number;
}

export interface BidRetentionReport {
  scanned: number;
  deleted: number;
  dependents: number;
  trail: number;
  kept: number;
  halted?: boolean;
}

export interface BidRetention {
  sweep(): Promise<BidRetentionReport>;
  start(): void;
  stop(): void;
}

function refOf(value: unknown): string | undefined {
  const uri = (value as { uri?: unknown } | undefined)?.uri;
  return typeof uri === "string" ? uri : undefined;
}

export function parseAtUri(uri: string): { did: string; collection: string; rkey: string } | undefined {
  const m = /^at:\/\/([^/]+)\/([^/]+)\/([^/]+)$/.exec(uri);
  return m ? { did: m[1], collection: m[2], rkey: m[3] } : undefined;
}

export function createBidRetention(opts: BidRetentionOptions): BidRetention {
  const ttlMs = opts.ttlMs ?? DEFAULT_BID_RECORD_TTL_MS;
  const maxPages = opts.maxPages ?? DEFAULT_BID_RETENTION_PAGES;
  const maxDeletes = opts.maxDeletes ?? DEFAULT_BID_RETENTION_DELETES;
  const now = opts.now ?? Date.now;
  let timer: ReturnType<typeof setInterval> | undefined;
  let first: ReturnType<typeof setTimeout> | undefined;
  let running: Promise<BidRetentionReport> | undefined;

  async function removeOwn(uri: string | undefined): Promise<void> {
    if (!uri) return;
    const at = parseAtUri(uri);
    if (!at || at.did !== opts.selfDid) return;
    await opts.deleteRecord(at.collection, at.rkey).catch((err: unknown) => {
      if (isRateLimitError(err) || !/404|not ?found|RecordNotFound/i.test(String(err))) throw err;
      opts.logger.warn("bid_record_already_absent", { uri, error: String(err) });
    });
  }

  function due(record: { value: Record<string, unknown> }, entry: BidEntry | undefined): boolean {
    if (entry) {
      if (!entry.settledAt) return false;
      return now() - Date.parse(entry.settledAt) >= ttlMs;
    }
    const created = Date.parse(String(record.value.createdAt ?? ""));
    return Number.isFinite(created) && now() - created >= ttlMs;
  }

  function addDependents(into: Set<string>, record: { value: Record<string, unknown> }): void {
    for (const uri of [refOf(record.value.payload), refOf(record.value.bidConfig)]) if (uri) into.add(uri);
  }

  async function listAll(list: NonNullable<BidRetentionOptions["listOwnRecords"]>, collection: string): Promise<OwnBidRecordPage["records"] | undefined> {
    const out: OwnBidRecordPage["records"] = [];
    let cursor: string | undefined;
    for (let page = 0; page < maxPages; page++) {
      const got = await list(collection, cursor);
      out.push(...got.records);
      cursor = got.cursor;
      if (!cursor) return out;
    }
    return undefined;
  }

  async function sweepTrail(
    list: NonNullable<BidRetentionOptions["listOwnRecords"]>,
    liveBids: Set<string>,
    report: BidRetentionReport,
  ): Promise<boolean> {
    const receipts = await listAll(list, RECEIPT_COLLECTION);
    const events = await listAll(list, EVENT_COLLECTION);
    if (!receipts || !events) return false;
    const liveReceipts = new Set<string>();
    const goneReceipts: string[] = [];
    for (const r of receipts) {
      const bidUri = refOf(r.value.bid);
      const created = Date.parse(String(r.value.createdAt ?? ""));
      const old = !Number.isFinite(created) || now() - created >= ttlMs;
      if (bidUri && !liveBids.has(bidUri) && old) goneReceipts.push(r.uri);
      else liveReceipts.add(r.uri);
    }
    const shared = opts.sharedUris?.() ?? new Set<string>();
    let trailDeletes = 0;
    for (const e of events) {
      const receiptUri = refOf(e.value.receipt);
      if (!receiptUri || liveReceipts.has(receiptUri)) continue;
      if (trailDeletes >= maxDeletes * 4) return false;
      try {
        await removeOwn(e.uri);
        const payload = refOf(e.value.payload);
        if (payload && !shared.has(payload)) await removeOwn(payload);
        report.trail++;
        trailDeletes++;
      } catch (err) {
        opts.logger.warn("bid_record_retention_failed", { uri: e.uri, error: String(err), halted: isRateLimitError(err) });
        if (isRateLimitError(err)) return true;
      }
    }
    for (const uri of goneReceipts) {
      if (trailDeletes >= maxDeletes * 4) return false;
      try {
        await removeOwn(uri);
        report.trail++;
        trailDeletes++;
      } catch (err) {
        opts.logger.warn("bid_record_retention_failed", { uri, error: String(err), halted: isRateLimitError(err) });
        if (isRateLimitError(err)) return true;
      }
    }
    return false;
  }

  async function run(): Promise<BidRetentionReport> {
    const report: BidRetentionReport = { scanned: 0, deleted: 0, kept: 0, dependents: 0, trail: 0 };
    const byRfp = new Map<string, BidEntry>();
    for (const e of await opts.bids.list()) byRfp.set(e.rfp.uri, e);
    const records: OwnBidRecordPage["records"] = [];
    let cursor: string | undefined;
    for (let page = 0; page < maxPages; page++) {
      const out = await opts.listOwnBidRecords(cursor);
      records.push(...out.records);
      cursor = out.cursor;
      if (!cursor) break;
    }
    const scanComplete = cursor === undefined;
    const liveBids = new Set<string>();
    const live = new Set<string>();
    const dueRecords: OwnBidRecordPage["records"] = [];
    for (const record of records) {
      report.scanned++;
      const rfpUri = refOf(record.value.rfp);
      const entry = rfpUri ? byRfp.get(rfpUri) : undefined;
      if (opts.keepBidUri?.(record.uri) || !due(record, entry)) {
        report.kept++;
        liveBids.add(record.uri);
        addDependents(live, record);
      } else {
        dueRecords.push(record);
      }
    }
    const orphaned = new Set<string>();
    let halted = false;
    for (const record of dueRecords) {
      if (halted || report.deleted >= maxDeletes) {
        liveBids.add(record.uri);
        addDependents(live, record);
        continue;
      }
      try {
        await removeOwn(record.uri);
        report.deleted++;
        addDependents(orphaned, record);
      } catch (err) {
        report.kept++;
        liveBids.add(record.uri);
        addDependents(live, record);
        halted = isRateLimitError(err);
        opts.logger.warn("bid_record_retention_failed", { bidUri: record.uri, error: String(err), halted });
      }
    }
    const shared = opts.sharedUris?.() ?? new Set<string>();
    for (const uri of orphaned) {
      if (halted) break;
      if (live.has(uri) || shared.has(uri)) continue;
      try {
        await removeOwn(uri);
        report.dependents++;
      } catch (err) {
        halted = isRateLimitError(err);
        opts.logger.warn("bid_record_retention_failed", { uri, error: String(err), halted });
      }
    }
    if (!halted && scanComplete && opts.listOwnRecords) halted = await sweepTrail(opts.listOwnRecords, liveBids, report);
    if (halted) report.halted = true;
    if (report.deleted > 0 || report.dependents > 0 || report.trail > 0) opts.logger.info("bid_records_removed", { ...report, ttlMs });
    return report;
  }

  function sweep(): Promise<BidRetentionReport> {
    running ??= run().finally(() => {
      running = undefined;
    });
    return running;
  }

  return {
    sweep,
    start() {
      if (timer !== undefined) return;
      const run = () => sweep().catch((err: unknown) => opts.logger.warn("bid_record_sweep_failed", { error: String(err) }));
      timer = setInterval(run, opts.intervalMs ?? DEFAULT_BID_RETENTION_SCAN_MS);
      first = setTimeout(run, DEFAULT_BID_RETENTION_FIRST_MS);
      Deno.unrefTimer(timer as unknown as number);
      Deno.unrefTimer(first as unknown as number);
    },
    stop() {
      if (timer !== undefined) clearInterval(timer);
      if (first !== undefined) clearTimeout(first);
      timer = undefined;
      first = undefined;
    },
  };
}
