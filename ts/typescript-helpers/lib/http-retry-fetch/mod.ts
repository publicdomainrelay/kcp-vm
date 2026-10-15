import {
  DEFAULT_RETRY_POLICY,
  decideRetry,
  describeSummary,
  type RetryPolicy,
  type RetrySummary,
} from "@publicdomainrelay/http-retry-common";

export interface RetryEvent {
  label: string;
  attempt: number;
  status: number | null;
  delayMs: number;
  waitedMs: number;
  reason: string;
}

export interface GiveUpEvent {
  label: string;
  attempts: number;
  status: number | null;
  waitedMs: number;
  reason: string;
}

export interface RetryFetchOptions {
  label?: string;
  policy?: Partial<RetryPolicy>;
  signal?: AbortSignal;
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
  now?: () => number;
  random?: () => number;
  onRetry?: (event: RetryEvent) => void;
  onGiveUp?: (event: GiveUpEvent) => void;
}

export class RetryExhaustedError extends Error {
  readonly summary: RetrySummary;
  constructor(cause: unknown, summary: RetrySummary) {
    const base = cause instanceof Error ? cause.message : String(cause);
    const tail = describeSummary(summary);
    super(tail === "" ? base : `${base} (${tail})`, { cause });
    this.name = "RetryExhaustedError";
    this.summary = summary;
  }
}

const summaries = new WeakMap<Response, RetrySummary>();

export function retrySummaryOf(res: Response): RetrySummary | undefined {
  return summaries.get(res);
}

export function retryNoteOf(res: Response): string {
  const summary = summaries.get(res);
  return summary ? describeSummary(summary) : "";
}

export function sleepFor(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

export async function fetchWithRetry(
  send: (attempt: number) => Promise<Response>,
  opts: RetryFetchOptions = {},
): Promise<Response> {
  const policy: RetryPolicy = { ...DEFAULT_RETRY_POLICY, ...opts.policy };
  const sleep = opts.sleep ?? sleepFor;
  const now = opts.now ?? Date.now;
  const random = opts.random ?? Math.random;
  const label = opts.label ?? "request";
  let waitedMs = 0;
  for (let attempt = 1;; attempt++) {
    let res: Response | null = null;
    let failure: unknown = null;
    try {
      res = await send(attempt);
    } catch (err) {
      if (opts.signal?.aborted) throw err;
      failure = err;
    }
    const status = res ? res.status : null;
    if (res && res.ok) {
      if (attempt > 1) summaries.set(res, { attempts: attempt, waitedMs, lastStatus: status, gaveUp: null });
      return res;
    }
    const decision = decideRetry({
      attempt,
      status,
      headers: res?.headers,
      waitedMs,
      nowMs: now(),
      policy,
      random: random(),
    });
    if (!decision.retry) {
      const summary: RetrySummary = {
        attempts: attempt,
        waitedMs,
        lastStatus: status,
        gaveUp: attempt === 1 && status !== null ? null : decision.reason,
      };
      if (attempt > 1 || status === null) {
        opts.onGiveUp?.({ label, attempts: attempt, status, waitedMs, reason: decision.reason });
      }
      if (res) {
        summaries.set(res, summary);
        return res;
      }
      throw new RetryExhaustedError(failure, summary);
    }
    opts.onRetry?.({ label, attempt, status, delayMs: decision.delayMs, waitedMs, reason: decision.reason });
    await res?.body?.cancel().catch(() => undefined);
    await sleep(decision.delayMs, opts.signal);
    waitedMs += decision.delayMs;
  }
}
