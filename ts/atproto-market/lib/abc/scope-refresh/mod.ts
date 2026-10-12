export interface ScopeRefreshWindow {
  refreshIntervalMs: number;
  idleTtlMs: number;
}

export interface ScopeRefreshEntry<T> {
  readonly key: string;
  readonly counterpartyDid: string;
  readonly generation: number;
  lastUseMs: number;
  payload: T;
}

export class ScopeRefreshState<T> {
  private readonly entries = new Map<string, ScopeRefreshEntry<T>>();
  private readonly byDid = new Map<string, Set<string>>();
  private generation = 0;

  constructor(readonly window: ScopeRefreshWindow) {}

  use(key: string, counterpartyDid: string, payload: T, nowMs: number): ScopeRefreshEntry<T> {
    const existing = this.entries.get(key);
    if (existing) {
      existing.lastUseMs = nowMs;
      existing.payload = payload;
      return existing;
    }
    const entry: ScopeRefreshEntry<T> = {
      key,
      counterpartyDid,
      generation: ++this.generation,
      lastUseMs: nowMs,
      payload,
    };
    this.entries.set(key, entry);
    const keys = this.byDid.get(counterpartyDid) ?? new Set<string>();
    keys.add(key);
    this.byDid.set(counterpartyDid, keys);
    return entry;
  }

  current(key: string, generation: number): ScopeRefreshEntry<T> | undefined {
    const entry = this.entries.get(key);
    return entry && entry.generation === generation ? entry : undefined;
  }

  drop(key: string): void {
    const entry = this.entries.get(key);
    if (!entry) return;
    this.entries.delete(key);
    const keys = this.byDid.get(entry.counterpartyDid);
    if (!keys) return;
    keys.delete(key);
    if (keys.size === 0) this.byDid.delete(entry.counterpartyDid);
  }

  keysForDid(did: string): string[] {
    return [...(this.byDid.get(did) ?? [])];
  }

  idle(entry: ScopeRefreshEntry<T>, nowMs: number): boolean {
    return nowMs - entry.lastUseMs > this.window.idleTtlMs;
  }

  size(): number {
    return this.entries.size;
  }
}
