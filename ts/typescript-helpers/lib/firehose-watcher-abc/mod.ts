import type { LoggerInterface } from "@publicdomainrelay/logger";

// Canonical types now live in atproto-event-stream-common.
// Re-export for backward compat — existing consumers don't break.
export type {
  FirehoseOperation,
  FirehoseProgress,
  FirehoseRecordEvent,
} from "@publicdomainrelay/atproto-event-stream-common";

// Internal re-import so FirehoseWatcherOptions can reference the type.
import type { FirehoseProgress as _FirehoseProgress, FirehoseRecordEvent as _FirehoseRecordEvent } from "@publicdomainrelay/atproto-event-stream-common";

export interface FirehoseWatcher {
  close(): void;
}

export interface FirehoseWatcherOptions {
  url: string;
  wantedCollections: string[];
  cursor?: number;
  onRecord(event: _FirehoseRecordEvent): void | Promise<void>;
  onProgress?(progress: _FirehoseProgress): void;
  log?: LoggerInterface;
}
