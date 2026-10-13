/** Record operation kind — create, update, or delete. */
export type FirehoseOperation = "create" | "update" | "delete";

/** Normalized event shape emitted by all ATProto event stream transports. */
export interface FirehoseRecordEvent {
  did: string;
  collection: string;
  rkey: string;
  cid: string;
  operation: FirehoseOperation;
  /** at://{did}/{collection}/{rkey} */
  uri: string;
}

export interface FirehoseProgress {
  cursor: number;
  eventTime?: string;
}

export type StreamKind = "relay" | "jetstream";

export interface StreamCursorStore {
  load(source: string): number | undefined;
  save(source: string, progress: FirehoseProgress): void;
}

export interface StreamResumeOverlap {
  subscribeReposEvents: number;
  jetstreamMicros: number;
}

export const DEFAULT_RESUME_OVERLAP: StreamResumeOverlap = {
  subscribeReposEvents: 1_000,
  jetstreamMicros: 30_000_000,
};

export function streamSource(kind: StreamKind, url: string): string {
  return `${kind}:${url}`;
}

export function resumeCursor(
  kind: StreamKind,
  stored: number | undefined,
  overlap: StreamResumeOverlap = DEFAULT_RESUME_OVERLAP,
): number | undefined {
  if (stored === undefined || !Number.isFinite(stored) || stored <= 0) return undefined;
  const back = kind === "relay" ? overlap.subscribeReposEvents : overlap.jetstreamMicros;
  return Math.max(0, stored - back);
}

/** AT-URI + CID uniquely identifies a record version for dedup. */
export function eventKey(event: FirehoseRecordEvent): string {
  return `${event.uri}|${event.cid}`;
}

/** Public Bluesky relay URLs — standard Lexicons only. Custom Lexicons filtered out. */
export const DEFAULT_RELAY_URLS: string[] = [
  "https://bsky.network",
  "https://relay.mini-cloud-0002.chadig.com",
];

/** Public Jetstream URLs — all Lexicons pass through. Use for custom collections. */
export const DEFAULT_JETSTREAM_URLS: string[] = [
  "wss://jetstream1.us-east.bsky.network/subscribe",
  "wss://jetstream2.us-east.bsky.network/subscribe",
  "wss://jetstream1.us-west.bsky.network/subscribe",
  "wss://jetstream2.us-west.bsky.network/subscribe",
];
