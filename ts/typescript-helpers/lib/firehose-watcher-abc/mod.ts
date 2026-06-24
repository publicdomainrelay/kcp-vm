import type { LoggerInterface } from "@publicdomainrelay/logger";

export type FirehoseOperation = "create" | "update" | "delete";

export interface FirehoseRecordEvent {
  did: string;
  collection: string;
  rkey: string;
  cid: string;
  operation: FirehoseOperation;
  uri: string;
}

export interface FirehoseWatcher {
  close(): void;
}

export interface FirehoseWatcherOptions {
  url: string;
  wantedCollections: string[];
  cursor?: number;
  onRecord(event: FirehoseRecordEvent): void | Promise<void>;
  log?: LoggerInterface;
}
