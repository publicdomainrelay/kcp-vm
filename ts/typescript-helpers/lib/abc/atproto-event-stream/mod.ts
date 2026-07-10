import type { FirehoseRecordEvent } from "@publicdomainrelay/atproto-event-stream-common";
import type { LoggerInterface } from "@publicdomainrelay/logger";

/** Handle returned by watch() — call close() to stop receiving events. */
export interface ATProtoEventStreamWatcher {
  close(): void;
}

/** A single ATProto event source (one relay or one Jetstream instance). */
export interface ATProtoEventStream {
  /**
   * Start watching for record events matching wantedCollections.
   * Each stream implementation handles collection filtering internally
   * (Jetstream via server-side wantedCollections param, relay via client-side filter).
   */
  watch(opts: {
    wantedCollections: string[];
    onEvent(event: FirehoseRecordEvent): void | Promise<void>;
    log?: LoggerInterface;
  }): ATProtoEventStreamWatcher;
}

/** AT Protocol relay — provides firehose subscribeRepos + collection index queries. */
export interface ATProtoRelay extends ATProtoEventStream {
  readonly url: string;

  /**
   * Query relay for DIDs that have records in a collection.
   * Uses GET /xrpc/com.atproto.sync.listReposByCollection.
   */
  listReposByCollection(
    collection: string,
    opts?: { limit?: number; timeoutMs?: number; log?: LoggerInterface },
  ): Promise<string[]>;
}

/** Jetstream — fan-out service with JSON encoding and wantedCollections filtering. */
export interface ATProtoJetstream extends ATProtoEventStream {
  readonly url: string;
}

/** Coordinates multiple ATProtoEventStream instances. Deduplicates by AT-URI + CID. */
export interface ATProtoEventStreamsClient {
  /**
   * Watch specific collections across all underlying streams.
   * Events deduplicated by AT-URI + CID — each record version emitted at most once.
   */
  watch(opts: {
    wantedCollections: string[];
    onEvent(event: FirehoseRecordEvent): void | Promise<void>;
    log?: LoggerInterface;
  }): ATProtoEventStreamWatcher;

  /** Close all underlying watchers. */
  close(): void;

  /** Underlying relay instances (for direct access — discovery, requestCrawl, etc.). */
  readonly relays: readonly ATProtoRelay[];

  /** Underlying Jetstream instances. */
  readonly jetstreams: readonly ATProtoJetstream[];
}
