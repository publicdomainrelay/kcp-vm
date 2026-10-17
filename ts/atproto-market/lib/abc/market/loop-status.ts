export interface MarketDeclineTally {
  reason: string;
  count: number;
}

export interface MarketStreamTally {
  source: string;
  cursor?: number;
  lagMs?: number;
  lastEventAt?: string;
}

export interface MarketRecentDecline {
  rfpUri: string;
  reason: string;
  at: string;
}

export const RECENT_DECLINES = 50;

export const LATENCY_RING = 256;

export interface MarketLatencyView {
  count: number;
  p50Us: number;
  p95Us: number;
  maxUs: number;
}

export function latencyViewOf(ring: readonly number[]): MarketLatencyView | undefined {
  if (ring.length === 0) return undefined;
  const sorted = [...ring].sort((a, b) => a - b);
  const at = (q: number) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];
  return { count: sorted.length, p50Us: at(0.5), p95Us: at(0.95), maxUs: sorted[sorted.length - 1] };
}

export interface MarketLoopView {
  rfpsSeen: number;
  rfpsScoped: number;
  declinedByReason: MarketDeclineTally[];
  bids: number;
  wins: number;
  contractsActive: number;
  streams: MarketStreamTally[];
  recentDeclines?: MarketRecentDecline[];
  rfpToBid?: MarketLatencyView;
  kcpWait?: MarketLatencyView;
  updatedAt: string;
}

export interface MarketLoopRecorder {
  rfpSeen(): void;
  rfpScoped(): void;
  declined(reason: string, rfpUri?: string, at?: Date): void;
  recentlyDeclined?(rfpUri: string): MarketRecentDecline | undefined;
  bid(): void;
  bidTimed?(rfpToBidMs: number, kcpWaitMs: number): void;
  win(): void;
  contractsActive(count: number): void;
  streamProgress(source: string, cursor: number, eventTime: string | undefined, receivedAt: Date): void;
}

export function declineReasonOf(text: string): string {
  const words = text.trim().toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
  if (words.length === 0) return "unknown";
  return words[0] + words.slice(1).map((w) => w[0].toUpperCase() + w.slice(1)).join("");
}

export class MarketLoopTally implements MarketLoopRecorder {
  #rfpsSeen = 0;
  #rfpsScoped = 0;
  #declined = new Map<string, number>();
  #bids = 0;
  #wins = 0;
  #contractsActive = 0;
  #streams = new Map<string, MarketStreamTally>();
  #recent: MarketRecentDecline[] = [];
  #rfpToBid: number[] = [];
  #kcpWait: number[] = [];
  #onChange: () => void;

  constructor(onChange: () => void = () => {}, initial?: Partial<MarketLoopView>) {
    this.#onChange = onChange;
    this.#rfpsSeen = initial?.rfpsSeen ?? 0;
    this.#rfpsScoped = initial?.rfpsScoped ?? 0;
    for (const d of initial?.declinedByReason ?? []) this.#declined.set(d.reason, d.count);
    this.#bids = initial?.bids ?? 0;
    this.#wins = initial?.wins ?? 0;
    this.#contractsActive = initial?.contractsActive ?? 0;
    for (const st of initial?.streams ?? []) this.#streams.set(st.source, { ...st });
    this.#recent = (initial?.recentDeclines ?? []).slice(-RECENT_DECLINES).map((d) => ({ ...d }));
  }

  recentlyDeclined(rfpUri: string): MarketRecentDecline | undefined {
    return this.#recent.find((d) => d.rfpUri === rfpUri);
  }

  cursorOf(source: string): number | undefined {
    const cursor = this.#streams.get(source)?.cursor;
    return typeof cursor === "number" && cursor > 0 ? cursor : undefined;
  }

  #changed(): void {
    this.#onChange();
  }

  rfpSeen(): void {
    this.#rfpsSeen++;
    this.#changed();
  }

  rfpScoped(): void {
    this.#rfpsScoped++;
    this.#changed();
  }

  declined(reason: string, rfpUri?: string, at?: Date): void {
    this.#declined.set(reason, (this.#declined.get(reason) ?? 0) + 1);
    if (rfpUri && at) {
      this.#recent = [...this.#recent.filter((d) => d.rfpUri !== rfpUri), { rfpUri, reason, at: at.toISOString() }]
        .slice(-RECENT_DECLINES);
    }
    this.#changed();
  }

  bid(): void {
    this.#bids++;
    this.#changed();
  }

  bidTimed(rfpToBidMs: number, kcpWaitMs: number): void {
    for (const [ring, ms] of [[this.#rfpToBid, rfpToBidMs], [this.#kcpWait, kcpWaitMs]] as const) {
      ring.push(Math.round(ms * 1000));
      if (ring.length > LATENCY_RING) ring.shift();
    }
    this.#changed();
  }

  win(): void {
    this.#wins++;
    this.#changed();
  }

  contractsActive(count: number): void {
    if (count === this.#contractsActive) return;
    this.#contractsActive = count;
    this.#changed();
  }

  streamProgress(source: string, cursor: number, eventTime: string | undefined, receivedAt: Date): void {
    const at = eventTime ? Date.parse(eventTime) : Number.NaN;
    this.#streams.set(source, {
      source,
      cursor,
      ...(Number.isFinite(at) ? { lagMs: Math.max(0, receivedAt.getTime() - at), lastEventAt: new Date(at).toISOString() } : {}),
    });
    this.#onChange();
  }

  view(at: Date): MarketLoopView {
    return {
      rfpsSeen: this.#rfpsSeen,
      rfpsScoped: this.#rfpsScoped,
      declinedByReason: [...this.#declined].sort(([a], [b]) => a.localeCompare(b)).map(([reason, count]) => ({ reason, count })),
      bids: this.#bids,
      wins: this.#wins,
      contractsActive: this.#contractsActive,
      streams: [...this.#streams.values()].sort((a, b) => a.source.localeCompare(b.source)),
      ...(this.#recent.length ? { recentDeclines: this.#recent.map((d) => ({ ...d })) } : {}),
      ...(this.#rfpToBid.length ? { rfpToBid: latencyViewOf(this.#rfpToBid), kcpWait: latencyViewOf(this.#kcpWait) } : {}),
      updatedAt: at.toISOString(),
    };
  }
}
