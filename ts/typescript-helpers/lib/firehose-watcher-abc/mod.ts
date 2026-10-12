import type { LoggerInterface } from "@publicdomainrelay/logger";

// Canonical types now live in atproto-event-stream-common.
// Re-export for backward compat — existing consumers don't break.
export type {
  FirehoseOperation,
  FirehoseRecordEvent,
} from "@publicdomainrelay/atproto-event-stream-common";

// Internal re-import so FirehoseWatcherOptions can reference the type.
import type { FirehoseRecordEvent as _FirehoseRecordEvent } from "@publicdomainrelay/atproto-event-stream-common";

export interface FirehoseWatcher {
  close(): void;
}

export interface FirehoseWatcherOptions {
  url: string;
  wantedCollections: string[];
  cursor?: number;
  onRecord(event: _FirehoseRecordEvent): void | Promise<void>;
  log?: LoggerInterface;
}
