export interface TtlMemoOptions {
  ttlMs: number;
  maxEntries?: number;
  now?: () => number;
}

interface Entry<V> {
  value: V;
  expiresAt: number;
}

export interface TtlMemoStats {
  hits: number;
  misses: number;
  joined: number;
  entries: number;
}

export class TtlMemo<V> {
  private readonly entries = new Map<string, Entry<V>>();
  private readonly pending = new Map<string, Promise<V>>();
  private readonly ttlMs: number;
  private readonly maxEntries: number;
  private readonly now: () => number;
  private hits = 0;
  private misses = 0;
  private joined = 0;

  constructor(opts: TtlMemoOptions) {
    this.ttlMs = opts.ttlMs;
    this.maxEntries = opts.maxEntries ?? 4096;
    this.now = opts.now ?? Date.now;
  }

  peek(key: string): V | undefined {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    if (entry.expiresAt <= this.now()) {
      this.entries.delete(key);
      return undefined;
    }
    return entry.value;
  }

  async get(
    key: string,
    load: () => Promise<V>,
    keep: (value: V) => number | false = () => this.ttlMs,
  ): Promise<V> {
    const cached = this.peek(key);
    if (cached !== undefined) {
      this.hits++;
      return cached;
    }
    const inflight = this.pending.get(key);
    if (inflight) {
      this.joined++;
      return await inflight;
    }
    this.misses++;
    const work = (async () => {
      try {
        const value = await load();
        const ttl = keep(value);
        if (ttl !== false && ttl > 0) this.store(key, value, ttl);
        return value;
      } finally {
        this.pending.delete(key);
      }
    })();
    this.pending.set(key, work);
    return await work;
  }

  invalidate(key: string): void {
    this.entries.delete(key);
  }

  clear(): void {
    this.entries.clear();
  }

  stats(): TtlMemoStats {
    return { hits: this.hits, misses: this.misses, joined: this.joined, entries: this.entries.size };
  }

  private store(key: string, value: V, ttlMs: number): void {
    if (this.entries.size >= this.maxEntries) this.evict();
    this.entries.set(key, { value, expiresAt: this.now() + ttlMs });
  }

  private evict(): void {
    const at = this.now();
    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= at) this.entries.delete(key);
    }
    while (this.entries.size >= this.maxEntries) {
      const oldest = this.entries.keys().next();
      if (oldest.done) break;
      this.entries.delete(oldest.value);
    }
  }
}
