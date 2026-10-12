/**
 * runPolicy — evaluate a policy against a PolicyEvalCtx, memoizing the verdict
 * in the JSON-object cache. Both raw deno and the gha-lite actions go through
 * this, so a verdict computed on one run is reused on the next without hitting
 * the network again (cache flows across runs via request/response context or
 * the GITHUB_CACHE command file).
 *
 * Verdicts are keyed on the full strongRefs of the records present at
 * evaluation time (the rfp/bid/accept the caller passed) plus a hash of the
 * policy args, and carry a TTL (default 30s — the trust graph is mutable, so a
 * verdict is only authoritative for a window; `cacheTtlSec` in policy-args
 * overrides). Record resolutions are cached separately, under their own
 * strongRef, by PolicyEvalCtxImpl.resolve.
 */

import type { Policy, PolicyArgs, PolicyEvalCtx, PolicyResult } from "./types.ts";
import {
  policyCacheKey,
  readPolicyResult,
  writePolicyResult,
  type CacheStore,
} from "./cache.ts";

export interface RunPolicyResult {
  result: PolicyResult;
  /** True when the verdict was served from the cache rather than re-evaluated. */
  fromCache: boolean;
  cacheKey: string;
}

export interface RunPolicyOptions {
  /** Explicit cache key override (from an action input or caller). */
  cacheKey?: string;
  /** Verdict TTL in ms; defaults to cacheTtlSec from policy-args, else 30s. */
  ttlMs?: number;
}

const DEFAULT_TTL_MS = 30_000;

function ttlMsOf(args: PolicyArgs | undefined): number {
  const sec = args?.cacheTtlSec;
  return typeof sec === "number" && Number.isFinite(sec) && sec > 0 ? sec * 1000 : DEFAULT_TTL_MS;
}

/**
 * Evaluate `policy` with `ctx`, using `store` to memoize. Returns the verdict
 * plus the cache key it was stored under and whether it came from cache.
 */
export async function runPolicy(
  store: CacheStore,
  policy: Policy,
  ctx: PolicyEvalCtx,
  opts: RunPolicyOptions = {},
): Promise<RunPolicyResult> {
  const key = opts.cacheKey ?? policyCacheKey(policy.name, { rfp: ctx.rfp, bid: ctx.bid, accept: ctx.accept }, ctx.args);

  const hit = readPolicyResult(store, key);
  if (hit) {
    ctx.log("debug", "policy cache hit", { key });
    return { result: hit, fromCache: true, cacheKey: key };
  }

  const result = await policy.evaluate(ctx);
  writePolicyResult(store, key, result, opts.ttlMs ?? ttlMsOf(ctx.args));
  ctx.log("info", "policy evaluated", { key, allow: result.allow });
  return { result, fromCache: false, cacheKey: key };
}
