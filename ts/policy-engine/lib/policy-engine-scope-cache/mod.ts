/**
 * policy-engine-scope-cache — the host-side scope verdict cache.
 *
 * The hot path / checkScope never run a policy engine per event. Instead the
 * host caches the scope-mode workflow's verdict per (policy identity,
 * counterparty DID, args) and serves it synchronously; a miss runs the scope
 * lane once. Firehose trust events (`badgeBlueKeys` / `vouch`) invalidate every
 * cached verdict for the affected DIDs, so a changed operator or vouch cannot
 * leave a stale "no" (or "yes") behind.
 *
 * Negatives (allow=false) carry a short TTL (default 30s) because an
 * association record may appear between RFP submit and re-check; positives live
 * longer (default 5 min) — the trust graph is mutable but slowly.
 */

import type { PolicyArgs, PolicyResult } from "@publicdomainrelay/policy-common";

/** What a cached scope verdict is keyed on: the policy being asked about. */
export type PolicyIdentity =
  | { kind: "ref"; uri: string; cid: string }
  | { kind: "name"; name: string };

export interface ScopeCacheOptions {
  /** TTL for allow=true verdicts. Default 5 min. */
  positiveTtlMs?: number;
  /** TTL for allow=false verdicts. Default 30s. */
  negativeTtlMs?: number;
  /** Hard cap on cached verdicts (LRU). Default 1024. */
  maxEntries?: number;
  log?: (level: string, msg: string, meta?: Record<string, unknown>) => void;
}

export interface ScopeCache {
  /** Cached verdict, or undefined on a miss / expired entry. */
  get(identity: PolicyIdentity, counterpartyDid: string, args: PolicyArgs): PolicyResult | undefined;
  /** Store a verdict (positives live longer than negatives). */
  set(identity: PolicyIdentity, counterpartyDid: string, args: PolicyArgs, result: PolicyResult): void;
  /** Firehose trust event → drop cached scope verdicts for both DIDs. */
  applyEvent(e: { did: string; rkey: string }): void;
  stats(): { entries: number };
}

interface Entry {
  result: PolicyResult;
  cachedAt: number;
  ttlMs: number;
}

const DEFAULT_POSITIVE_TTL_MS = 5 * 60_000;
const DEFAULT_NEGATIVE_TTL_MS = 30_000;
const DEFAULT_MAX_ENTRIES = 1024;

/** Stable, order-dependent hash (djb2) used to fold args into a cache key. */
function hashString(s: string): string {
  let h = 5381;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  }
  return (h >>> 0).toString(36);
}

/** Cache key: policy identity + counterparty DID + args hash. */
export function scopeCacheKey(
  identity: PolicyIdentity,
  counterpartyDid: string,
  args: PolicyArgs,
): string {
  const id = identity.kind === "ref"
    ? `r:${identity.uri}/${identity.cid}`
    : `n:${identity.name}`;
  const argSuffix = args && Object.keys(args).length > 0
    ? `:${hashString(JSON.stringify(args))}`
    : "";
  return `${id}|${counterpartyDid}${argSuffix}`;
}

/** LRU-bounded scope verdict cache with per-DID invalidation. */
export function createScopeCache(opts: ScopeCacheOptions = {}): ScopeCache {
  const positiveTtlMs = opts.positiveTtlMs ?? DEFAULT_POSITIVE_TTL_MS;
  const negativeTtlMs = opts.negativeTtlMs ?? DEFAULT_NEGATIVE_TTL_MS;
  const maxEntries = opts.maxEntries ?? DEFAULT_MAX_ENTRIES;
  const log = opts.log ?? (() => {});

  const entries = new Map<string, Entry>();
  const byDid = new Map<string, Set<string>>();

  function touch(key: string): void {
    const e = entries.get(key);
    if (e) {
      entries.delete(key);
      entries.set(key, e);
    }
  }

  function evict(): void {
    while (entries.size > maxEntries) {
      const oldest = entries.keys().next().value as string | undefined;
      if (oldest === undefined) break;
      entries.delete(oldest);
    }
  }

  function indexByDid(counterpartyDid: string, key: string): void {
    let s = byDid.get(counterpartyDid);
    if (!s) {
      s = new Set();
      byDid.set(counterpartyDid, s);
    }
    s.add(key);
  }

  function dropKey(key: string): void {
    entries.delete(key);
  }

  return {
    get(identity, counterpartyDid, args) {
      const key = scopeCacheKey(identity, counterpartyDid, args);
      const e = entries.get(key);
      if (!e) return undefined;
      if (Date.now() - e.cachedAt > e.ttlMs) {
        dropKey(key);
        return undefined;
      }
      touch(key);
      return e.result;
    },

    set(identity, counterpartyDid, args, result) {
      const key = scopeCacheKey(identity, counterpartyDid, args);
      const ttlMs = result.allow ? positiveTtlMs : negativeTtlMs;
      entries.set(key, { result, cachedAt: Date.now(), ttlMs });
      touch(key);
      evict();
      indexByDid(counterpartyDid, key);
    },

    applyEvent({ did, rkey }) {
      for (const other of [did, rkey]) {
        if (!other) continue;
        const keys = byDid.get(other);
        if (!keys || keys.size === 0) continue;
        for (const k of keys) dropKey(k);
        byDid.delete(other);
        log("debug", "scope cache invalidated", { did: other, dropped: keys.size });
      }
    },

    stats() {
      return { entries: entries.size };
    },
  };
}
