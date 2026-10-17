import { retryAfterMsOf, type HeaderReader } from "@publicdomainrelay/http-retry-common";

export type WriteAction = "create" | "update" | "delete";

export type WriteClass = "contract" | "bid" | "sweep";

export const WRITE_POINTS: Readonly<Record<WriteAction, number>> = { create: 3, update: 2, delete: 1 };

export const BID_WRITE_POINTS = WRITE_POINTS.create;

export const CONTRACT_FINISH_POINTS = 21;

export const DEFAULT_BID_WRITE_RESERVE = 500;

export const DEFAULT_PAUSE_MS = 60_000;

export const HOURS_KEPT = 24;

const HOUR_MS = 3_600_000;

const EPOCH_SECONDS_FLOOR = 1_000_000_000;

export interface PdsWrite {
  collection: string;
  action: WriteAction;
}

export interface PdsWriteObservation {
  pds: string;
  status: number;
  headers: HeaderReader;
  writes: readonly PdsWrite[];
}

export interface WriteBlock {
  reason: string;
  retryAtMs?: number;
}

export class WriteBudgetError extends Error {
  constructor(readonly writeClass: WriteClass, readonly block: WriteBlock) {
    super(block.reason);
    this.name = "WriteBudgetError";
  }
}

export function isRateLimitError(err: unknown): boolean {
  return err instanceof WriteBudgetError || /\b429\b|RateLimitExceeded/.test(String(err));
}

export interface PdsWriteCollectionView {
  collection: string;
  writes: number;
  points: number;
}

export interface PdsWriteHourView {
  hour: string;
  points: number;
}

export interface PdsLimitView {
  pds: string;
  limit?: number;
  remaining?: number;
  resetAt?: string;
  pausedUntil?: string;
  observedAt: string;
}

export interface PdsWritesView {
  since: string;
  writes: number;
  points: number;
  rateLimited: number;
  reserve: number;
  byCollection: PdsWriteCollectionView[];
  perHour: PdsWriteHourView[];
  limits: PdsLimitView[];
}

export interface PdsWriteBudgetOptions {
  now?: () => number;
  reserve?: number;
  liveContracts?: () => number;
  bidCollections?: Iterable<string>;
  sweepCollections?: Iterable<string>;
}

interface PdsLimitState {
  limit?: number;
  remaining?: number;
  resetAtMs?: number;
  pausedUntilMs?: number;
  observedAtMs: number;
}

function numberOf(raw: string | null): number | undefined {
  if (raw === null || raw.trim() === "") return undefined;
  const value = Number(raw.trim());
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}

export class PdsWriteBudget {
  readonly #now: () => number;
  readonly #margin: number;
  #live: () => number;
  readonly #bid: ReadonlySet<string>;
  readonly #sweep: ReadonlySet<string>;
  readonly #pds = new Map<string, PdsLimitState>();
  readonly #byCollection = new Map<string, { writes: number; points: number }>();
  readonly #hours = new Map<number, number>();
  readonly #listeners = new Set<() => void>();
  readonly #since: number;
  #writes = 0;
  #points = 0;
  #limited = 0;

  constructor(opts: PdsWriteBudgetOptions = {}) {
    this.#now = opts.now ?? Date.now;
    this.#margin = opts.reserve ?? DEFAULT_BID_WRITE_RESERVE;
    this.#live = opts.liveContracts ?? (() => 0);
    this.#bid = new Set(opts.bidCollections ?? []);
    this.#sweep = new Set(opts.sweepCollections ?? []);
    this.#since = this.#now();
  }

  classOf(collection: string, action: WriteAction): WriteClass {
    if (action === "delete" || this.#sweep.has(collection)) return "sweep";
    return this.#bid.has(collection) ? "bid" : "contract";
  }

  trackContracts(liveContracts: () => number): void {
    this.#live = liveContracts;
  }

  reserve(): number {
    return this.#margin + this.#live() * CONTRACT_FINISH_POINTS;
  }

  onChange(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  observe(obs: PdsWriteObservation): void {
    const now = this.#now();
    const state = this.#pds.get(obs.pds) ?? { observedAtMs: now };
    this.#pds.set(obs.pds, state);
    state.observedAtMs = now;
    const limit = numberOf(obs.headers.get("ratelimit-limit"));
    const remaining = numberOf(obs.headers.get("ratelimit-remaining"));
    const reset = numberOf(obs.headers.get("ratelimit-reset"));
    if (limit !== undefined) state.limit = limit;
    if (reset !== undefined) state.resetAtMs = reset >= EPOCH_SECONDS_FLOOR ? reset * 1000 : now + reset * 1000;
    if (obs.status === 429) {
      this.#limited++;
      state.remaining = 0;
      state.pausedUntilMs = now + (retryAfterMsOf(obs.headers, now) ?? DEFAULT_PAUSE_MS);
    } else if (obs.status >= 200 && obs.status < 300) {
      let spent = 0;
      for (const w of obs.writes) {
        const points = WRITE_POINTS[w.action];
        spent += points;
        const row = this.#byCollection.get(w.collection) ?? { writes: 0, points: 0 };
        row.writes++;
        row.points += points;
        this.#byCollection.set(w.collection, row);
        this.#writes++;
      }
      this.#points += spent;
      const hour = Math.floor(now / HOUR_MS) * HOUR_MS;
      this.#hours.set(hour, (this.#hours.get(hour) ?? 0) + spent);
      for (const key of this.#hours.keys()) if (key <= hour - HOURS_KEPT * HOUR_MS) this.#hours.delete(key);
      if (remaining === undefined && state.remaining !== undefined) state.remaining = Math.max(0, state.remaining - spent);
      if (state.pausedUntilMs !== undefined && state.pausedUntilMs <= now) delete state.pausedUntilMs;
    }
    if (obs.status !== 429 && remaining !== undefined) state.remaining = remaining;
    for (const listener of this.#listeners) listener();
  }

  #remainingOf(state: PdsLimitState, now: number): number | undefined {
    return state.resetAtMs !== undefined && now >= state.resetAtMs ? undefined : state.remaining;
  }

  gate(writeClass: WriteClass, points: number): WriteBlock | undefined {
    if (writeClass === "contract") return undefined;
    const now = this.#now();
    for (const [pds, state] of this.#pds) {
      if (state.pausedUntilMs !== undefined && state.pausedUntilMs > now) {
        return {
          reason: `write budget: ${pds} rate limits writes until ${new Date(state.pausedUntilMs).toISOString()}`,
          retryAtMs: state.pausedUntilMs,
        };
      }
      const remaining = this.#remainingOf(state, now);
      const reserve = this.reserve();
      if (remaining !== undefined && remaining < reserve + points) {
        return {
          reason: `write budget: ${pds} has ${remaining} points left and ${reserve} are kept for ${this.#live()} live contracts and the margin`,
          retryAtMs: state.resetAtMs,
        };
      }
    }
    return undefined;
  }

  check(writeClass: WriteClass, points: number): void {
    const block = this.gate(writeClass, points);
    if (block) throw new WriteBudgetError(writeClass, block);
  }

  declineReason(): string | undefined {
    return this.gate("bid", BID_WRITE_POINTS)?.reason;
  }

  view(): PdsWritesView {
    const limits: PdsLimitView[] = [...this.#pds].map(([pds, s]) => ({
      pds,
      ...(s.limit !== undefined ? { limit: s.limit } : {}),
      ...(s.remaining !== undefined ? { remaining: s.remaining } : {}),
      ...(s.resetAtMs !== undefined ? { resetAt: new Date(s.resetAtMs).toISOString() } : {}),
      ...(s.pausedUntilMs !== undefined && s.pausedUntilMs > this.#now() ? { pausedUntil: new Date(s.pausedUntilMs).toISOString() } : {}),
      observedAt: new Date(s.observedAtMs).toISOString(),
    }));
    return {
      since: new Date(this.#since).toISOString(),
      writes: this.#writes,
      points: this.#points,
      rateLimited: this.#limited,
      reserve: this.reserve(),
      byCollection: [...this.#byCollection]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([collection, row]) => ({ collection, ...row })),
      perHour: [...this.#hours].sort(([a], [b]) => a - b).map(([hour, points]) => ({ hour: new Date(hour).toISOString(), points })),
      limits,
    };
  }
}
