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

/** AT-URI + CID uniquely identifies a record version for dedup. */
export function eventKey(event: FirehoseRecordEvent): string {
  return `${event.uri}|${event.cid}`;
}

/** Public Bluesky relay URLs — standard Lexicons only. Custom Lexicons filtered out. */
export const DEFAULT_RELAY_URLS: string[] = [
  "https://bsky.network",
];

/** Public Jetstream URLs — all Lexicons pass through. Use for custom collections. */
export const DEFAULT_JETSTREAM_URLS: string[] = [
  "wss://jetstream1.us-east.bsky.network/subscribe",
  "wss://jetstream2.us-east.bsky.network/subscribe",
  "wss://jetstream1.us-west.bsky.network/subscribe",
  "wss://jetstream2.us-west.bsky.network/subscribe",
];
