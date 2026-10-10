import type {
  PolicyArgs,
  PolicyPerspective,
  PolicyRecord,
  PolicyResult,
  StrongRef,
} from "@publicdomainrelay/policy-common";
import type { PolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import type { PolicyIdentity, ScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import { scopeCacheKey } from "@publicdomainrelay/policy-engine-scope-cache";
import { ScopeRefreshState } from "@publicdomainrelay/scope-refresh-abc";

export interface ScopeRefreshInput {
  ref?: StrongRef;
  policyRecord?: PolicyRecord;
  perspective: PolicyPerspective;
  selfDid: string;
  counterpartyDid: string;
  args: PolicyArgs;
}

export interface ScopeRefresherOptions {
  scopeCache: ScopeCache;
  evaluator?: PolicyEvaluator;
  positiveTtlMs?: number;
  refreshAtFraction?: number;
  idleTtlMs?: number;
  now?: () => number;
  setTimer?: (fn: () => void, ms: number) => number;
  clearTimer?: (handle: number) => void;
  unrefTimer?: (handle: number) => void;
  log?: (event: string, meta?: Record<string, unknown>) => void;
}

export interface ScopeRefresher {
  setEvaluator(evaluator: PolicyEvaluator): void;
  noteUse(input: ScopeRefreshInput): void;
  invalidate(e: { did: string; rkey?: string }): void;
  close(): void;
  stats(): { entries: number; pending: number; refreshes: number; attached: boolean };
}

const DEFAULT_POSITIVE_TTL_MS = 5 * 60_000;
const DEFAULT_REFRESH_AT_FRACTION = 0.8;
const DEFAULT_IDLE_TTL_MS = 30 * 60_000;

function defaultSetTimer(fn: () => void, ms: number): number {
  return setTimeout(fn, ms) as unknown as number;
}

function defaultClearTimer(handle: number): void {
  clearTimeout(handle);
}

function defaultUnrefTimer(handle: number): void {
  Deno.unrefTimer?.(handle);
}

function identityOf(input: ScopeRefreshInput): PolicyIdentity {
  return {
    kind: "ref",
    uri: input.ref?.uri ?? input.policyRecord?.uri ?? "",
    cid: input.ref?.cid ?? input.policyRecord?.cid ?? "",
  };
}

export function createScopeRefresher(opts: ScopeRefresherOptions): ScopeRefresher {
  const { scopeCache } = opts;
  const positiveTtlMs = opts.positiveTtlMs ?? DEFAULT_POSITIVE_TTL_MS;
  const refreshAtFraction = opts.refreshAtFraction ?? DEFAULT_REFRESH_AT_FRACTION;
  const idleTtlMs = opts.idleTtlMs ?? DEFAULT_IDLE_TTL_MS;
  const now = opts.now ?? (() => Date.now());
  const setTimer = opts.setTimer ?? defaultSetTimer;
  const clearTimer = opts.clearTimer ?? defaultClearTimer;
  const unrefTimer = opts.unrefTimer ?? defaultUnrefTimer;
  const log = opts.log ?? (() => {});
  const window = {
    refreshIntervalMs: Math.max(1, Math.floor(positiveTtlMs * refreshAtFraction)),
    idleTtlMs,
  };

  let evaluator = opts.evaluator;
  let state = new ScopeRefreshState<ScopeRefreshInput>(window);
  const pending = new Map<string, { handle: number; generation: number }>();
  let refreshes = 0;
  let closed = false;

  function clearPending(key: string): void {
    const armed = pending.get(key);
    if (!armed) return;
    pending.delete(key);
    clearTimer(armed.handle);
  }

  function schedule(key: string, generation: number): void {
    const handle = setTimer(() => {
      void refresh(key, generation);
    }, window.refreshIntervalMs);
    unrefTimer(handle);
    pending.set(key, { handle, generation });
  }

  async function refresh(key: string, generation: number): Promise<void> {
    pending.delete(key);
    if (closed) return;
    const entry = state.current(key, generation);
    if (!entry) return;
    if (state.idle(entry, now())) {
      state.drop(key);
      log("scope_refresh_idle", {
        counterpartyDid: entry.counterpartyDid,
        idleTtlMs: window.idleTtlMs,
      });
      return;
    }
    if (!evaluator) {
      state.drop(key);
      log("scope_refresh_unattached", { counterpartyDid: entry.counterpartyDid });
      return;
    }
    const input = entry.payload;
    const running = evaluator;
    scopeCache.applyEvent({ did: entry.counterpartyDid, rkey: "" });

    let result: PolicyResult;
    try {
      result = await running.scope({
        ...(input.ref ? { ref: input.ref } : {}),
        ...(input.policyRecord ? { policyRecord: input.policyRecord } : {}),
        perspective: input.perspective,
        selfDid: input.selfDid,
        counterpartyDid: input.counterpartyDid,
        args: input.args,
      });
    } catch (err) {
      state.drop(key);
      scopeCache.applyEvent({ did: entry.counterpartyDid, rkey: "" });
      log("scope_refresh_error", { counterpartyDid: entry.counterpartyDid, error: String(err) });
      return;
    }

    refreshes++;
    if (!state.current(key, generation)) {
      scopeCache.applyEvent({ did: entry.counterpartyDid, rkey: "" });
      log("scope_refresh_invalidated", { counterpartyDid: entry.counterpartyDid });
      return;
    }
    if (!result.allow) {
      state.drop(key);
      log("scope_refresh_denied", { counterpartyDid: entry.counterpartyDid });
      return;
    }
    log("scope_refresh", {
      counterpartyDid: entry.counterpartyDid,
      entries: state.size(),
      allow: true,
    });
    schedule(key, generation);
  }

  return {
    setEvaluator(next) {
      evaluator = next;
    },

    noteUse(input) {
      if (closed) return;
      const key = scopeCacheKey(identityOf(input), input.counterpartyDid, input.args);
      const entry = state.use(key, input.counterpartyDid, input, now());
      if (!pending.has(key)) schedule(key, entry.generation);
    },

    invalidate({ did, rkey }) {
      let dropped = 0;
      for (const other of [did, rkey]) {
        if (!other) continue;
        for (const key of state.keysForDid(other)) {
          clearPending(key);
          state.drop(key);
          dropped++;
        }
      }
      scopeCache.applyEvent({ did, rkey: rkey ?? "" });
      log("scope_refresh_invalidated", { did, rkey: rkey ?? "", dropped });
    },

    close() {
      closed = true;
      for (const { handle } of pending.values()) clearTimer(handle);
      pending.clear();
      state = new ScopeRefreshState<ScopeRefreshInput>(window);
    },

    stats() {
      return {
        entries: state.size(),
        pending: pending.size,
        refreshes,
        attached: evaluator !== undefined,
      };
    },
  };
}
