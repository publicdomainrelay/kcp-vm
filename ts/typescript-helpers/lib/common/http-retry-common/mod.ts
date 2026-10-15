export interface RetryPolicy {
  baseDelayMs: number;
  maxDelayMs: number;
  maxTotalWaitMs: number;
  maxAttempts: number;
  jitter: number;
  hintMarginMs: number;
}

export const DEFAULT_RETRY_POLICY: RetryPolicy = {
  baseDelayMs: 1_000,
  maxDelayMs: 30_000,
  maxTotalWaitMs: 90_000,
  maxAttempts: 8,
  jitter: 0.25,
  hintMarginMs: 250,
};

export const RETRYABLE_STATUSES: ReadonlySet<number> = new Set([408, 425, 429, 500, 502, 503, 504]);

export interface HeaderReader {
  get(name: string): string | null;
}

export interface RetrySummary {
  attempts: number;
  waitedMs: number;
  lastStatus: number | null;
  gaveUp: string | null;
}

export type RetryDecision =
  | { retry: true; delayMs: number; reason: string }
  | { retry: false; reason: string };

export interface RetryInput {
  attempt: number;
  status: number | null;
  headers?: HeaderReader;
  waitedMs: number;
  nowMs: number;
  policy: RetryPolicy;
  random: number;
}

const EPOCH_SECONDS_FLOOR = 1_000_000_000;

function secondsToMs(raw: string): number | null {
  const value = Number(raw.trim());
  if (!Number.isFinite(value) || value < 0) return null;
  return Math.ceil(value * 1000);
}

export function retryAfterMsOf(headers: HeaderReader, nowMs: number): number | null {
  const retryAfter = headers.get("retry-after");
  if (retryAfter !== null && retryAfter.trim() !== "") {
    const seconds = secondsToMs(retryAfter);
    if (seconds !== null) return seconds;
    const at = Date.parse(retryAfter);
    if (Number.isFinite(at)) return Math.max(0, at - nowMs);
  }
  for (const name of ["ratelimit-reset", "x-ratelimit-reset"]) {
    const reset = headers.get(name);
    if (reset === null || reset.trim() === "") continue;
    const value = Number(reset.trim());
    if (!Number.isFinite(value) || value < 0) continue;
    if (value >= EPOCH_SECONDS_FLOOR) return Math.max(0, Math.ceil(value * 1000 - nowMs));
    return Math.ceil(value * 1000);
  }
  return null;
}

export function backoffDelayMs(attempt: number, policy: RetryPolicy, random: number): number {
  const exponent = Math.max(0, attempt - 1);
  const raw = Math.min(policy.maxDelayMs, policy.baseDelayMs * 2 ** Math.min(exponent, 30));
  return Math.round(raw * (1 + policy.jitter * random));
}

export function isRetryableStatus(status: number): boolean {
  return RETRYABLE_STATUSES.has(status);
}

export function decideRetry(input: RetryInput): RetryDecision {
  const { attempt, status, headers, waitedMs, nowMs, policy, random } = input;
  if (status !== null && !isRetryableStatus(status)) {
    return { retry: false, reason: `status ${status} is not retryable` };
  }
  if (attempt >= policy.maxAttempts) {
    return { retry: false, reason: `${attempt} attempts used, the limit is ${policy.maxAttempts}` };
  }
  const remainingMs = policy.maxTotalWaitMs - waitedMs;
  if (remainingMs <= 0) {
    return { retry: false, reason: `waited ${waitedMs} ms of ${policy.maxTotalWaitMs} ms allowed` };
  }
  const hintedMs = headers ? retryAfterMsOf(headers, nowMs) : null;
  if (hintedMs !== null) {
    const delayMs = hintedMs + policy.hintMarginMs;
    if (delayMs > remainingMs) {
      return {
        retry: false,
        reason: `the server asked for ${Math.ceil(hintedMs / 1000)} s and only ${Math.floor(remainingMs / 1000)} s of the wait budget is left`,
      };
    }
    return { retry: true, delayMs, reason: `the server asked for ${Math.ceil(hintedMs / 1000)} s` };
  }
  const delayMs = Math.min(backoffDelayMs(attempt, policy, random), remainingMs);
  const cause = status === null ? "a network error" : `status ${status}`;
  return { retry: true, delayMs, reason: `${cause}, exponential backoff` };
}

export function describeSummary(summary: RetrySummary): string {
  const retries = summary.attempts - 1;
  if (retries <= 0 && summary.gaveUp === null) return "";
  const gave = summary.gaveUp === null ? "" : `; gave up: ${summary.gaveUp}`;
  return `after ${summary.attempts} attempts over ${Math.round(summary.waitedMs / 100) / 10} s of waiting${gave}`;
}
