/**
 * The JSON-object cache construct, shared by raw-deno policy evaluation
 * (deno-typescript) and the gha-lite bundled actions.
 *
 * Shape mirrors lib/policy-engine-server-gha-lite/src/models.ts and the
 * bundled actions/cache/save|restore actions: a `Cache` is key → entry, each
 * entry maps a workspace-relative path to a file (text or base64). In the
 * gha-lite engine the map rides the GITHUB_CACHE command file — a real temp
 * file seeded by the engine in full mode, an in-memory virtual file in the
 * net-only worker. In raw deno it is a plain in-memory JSON object passed in
 * via request context and returned in the status detail.
 *
 * Every entry is keyed on a full strongRef (URI+CID): record resolutions under
 * `rec/<uri>/<cid>` (immutable — a record's cid is content-addressed, so the
 * entry never goes stale) and policy verdicts under a key built from the
 * strongRefs of the records present at evaluation time. Verdicts carry a TTL
 * (default 30s, mirroring the atproto-market trust cache's negative-operator
 * TTL) because they depend on the mutable vouch/operator graph; record
 * resolutions are permanent. The store is LRU-bounded (`maxEntries`, default
 * 512) so a caller that keeps persisting `.detail.cache` never grows the map
 * without bound. LRU recency is key insertion order, which survives JSON
 * round-trips.
 */

import type { PolicyArgs, PolicyResult } from "./types.ts";

/** A file stored in a cache entry. `data` is text, or base64 when encoding is "base64". */
export interface CacheFile {
  data: string;
  encoding: "text" | "base64";
}

/** One cache entry: workspace-relative path → file. */
export type CacheEntry = Record<string, CacheFile>;

/** The full cache: cache key → entry. */
export type Cache = Record<string, CacheEntry>;

export interface CacheStoreOpts {
  /** Hard cap on entries; the oldest are evicted first. Default 512. */
  maxEntries?: number;
}

/** Read/write handle over the current cache map. */
export interface CacheStore {
  read(): Cache;
  write(cache: Cache): void;
  /** Get an entry, refreshing its LRU recency on a hit. */
  get(key: string): CacheEntry | undefined;
  /** Put an entry, refreshing recency and evicting the oldest while over cap. */
  set(key: string, entry: CacheEntry): void;
  has(key: string): boolean;
  /** Delete an entry. */
  delete(key: string): void;
}

/**
 * A cache store backed by the GITHUB_CACHE command file when present (the
 * gha-lite engine seeds it before each action and reads the map back after),
 * otherwise a plain in-memory JSON object (raw deno). The seed `initial` only
 * applies when no command file is present. LRU recency = key insertion order.
 */
export function createCacheStore(initial?: Cache, opts: CacheStoreOpts = {}): CacheStore {
  const maxEntries = opts.maxEntries ?? 512;
  let cache: Cache = initial && typeof initial === "object" ? initial : {};
  const order: string[] = Object.keys(cache);
  const ghCachePath = Deno.env.get("GITHUB_CACHE");
  if (ghCachePath) {
    try {
      const parsed = JSON.parse(Deno.readTextFileSync(ghCachePath));
      if (parsed && typeof parsed === "object") {
        cache = parsed as Cache;
        order.length = 0;
        order.push(...Object.keys(cache));
      }
    } catch {
      // unreadable or empty — fall through to the seed
    }
  }

  function touch(key: string): void {
    const i = order.indexOf(key);
    if (i >= 0) order.splice(i, 1);
    order.push(key);
  }

  function evict(): void {
    while (order.length > maxEntries) {
      const oldest = order.shift();
      if (oldest === undefined) break;
      delete cache[oldest];
    }
  }

  function persist(): void {
    if (ghCachePath) {
      try {
        Deno.writeTextFileSync(ghCachePath, JSON.stringify(cache));
      } catch {
        // cache still lives in memory; a failed persist is non-fatal
      }
    }
  }

  return {
    read(): Cache {
      return cache;
    },
    write(next: Cache): void {
      cache = next;
      order.length = 0;
      order.push(...Object.keys(next));
      evict();
      persist();
    },
    get(key: string): CacheEntry | undefined {
      const entry = cache[key];
      if (entry !== undefined) touch(key);
      return entry;
    },
    set(key: string, entry: CacheEntry): void {
      cache[key] = entry;
      touch(key);
      evict();
      persist();
    },
    has(key: string): boolean {
      return key in cache;
    },
    delete(key: string): void {
      if (key in cache) {
        delete cache[key];
        const i = order.indexOf(key);
        if (i >= 0) order.splice(i, 1);
      }
    },
  };
}

/** Cache key for a single resolved atproto record. Immutable (content-addressed). */
export function recordCacheKey(ref: { uri: string; cid: string }): string {
  return `rec/${ref.uri}/${ref.cid}`;
}

/** The file a resolved record value is stored under inside its cache entry. */
export const RECORD_VALUE_FILE = "value.json";

/** The file a policy verdict is stored under inside its cache entry. */
export const POLICY_RESULT_FILE = "result.json";

/** The file carrying a verdict entry's TTL metadata. */
export const POLICY_META_FILE = "meta.json";

export interface PolicyMeta {
  cachedAt: number;
  ttlMs: number;
}

/** Stable, order-dependent hash used to fold policy args into a cache key. */
function hashString(s: string): string {
  let h = 5381;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  }
  return (h >>> 0).toString(36);
}

function recPart(r: { uri?: string; cid?: string } | undefined): string {
  return r && r.uri ? `${r.uri}/${r.cid ?? ""}` : "";
}

/**
 * Cache key for a policy verdict over the records present at evaluation time.
 * The full strongRef (URI+CID) of every present record is folded in, so the
 * same stage records always map to the same slot and a different combination
 * never collides. Args are hashed in so different `policy-args` (e.g. a changed
 * maxCpus / allowed nsid list) force a recompute.
 */
export function policyCacheKey(
  policyName: string,
  records: { rfp?: { uri: string; cid: string }; bid?: { uri: string; cid: string }; accept?: { uri: string; cid: string } },
  args?: PolicyArgs,
): string {
  const argSuffix = args && Object.keys(args).length > 0
    ? `:${hashString(JSON.stringify(args))}`
    : "";
  return `policy/${policyName}/${recPart(records.rfp)}|${recPart(records.bid)}|${recPart(records.accept)}${argSuffix}`;
}

/** Read a memoized policy result, or undefined on a miss / corrupt / expired entry.
 * Expired entries are deleted on read. */
export function readPolicyResult(
  store: CacheStore,
  key: string,
  now = Date.now(),
): PolicyResult | undefined {
  const entry = store.get(key);
  if (!entry) return undefined;

  const meta = entry[POLICY_META_FILE];
  if (meta) {
    try {
      const m = JSON.parse(meta.data) as PolicyMeta;
      if (now - m.cachedAt > m.ttlMs) {
        store.delete(key);
        return undefined;
      }
    } catch {
      // corrupt metadata — treat the entry as timeless
    }
  }

  const file = entry[POLICY_RESULT_FILE];
  if (!file) return undefined;
  try {
    const parsed = JSON.parse(file.data) as PolicyResult;
    return parsed && typeof parsed === "object" && typeof parsed.allow === "boolean"
      ? parsed
      : undefined;
  } catch {
    return undefined;
  }
}

/** Write a policy result into the cache under `key`, with a TTL metadata file. */
export function writePolicyResult(
  store: CacheStore,
  key: string,
  result: PolicyResult,
  ttlMs = 30_000,
): void {
  store.set(key, {
    [POLICY_RESULT_FILE]: { data: JSON.stringify(result), encoding: "text" },
    [POLICY_META_FILE]: { data: JSON.stringify({ cachedAt: Date.now(), ttlMs } satisfies PolicyMeta), encoding: "text" },
  });
}
