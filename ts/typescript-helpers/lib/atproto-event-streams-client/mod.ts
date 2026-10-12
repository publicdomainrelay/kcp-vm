import {
  eventKey,
  DEFAULT_RELAY_URLS,
  DEFAULT_JETSTREAM_URLS,
} from "@publicdomainrelay/atproto-event-stream-common";
import type {
  ATProtoRelay,
  ATProtoJetstream,
  ATProtoEventStreamsClient,
  ATProtoEventStreamWatcher,
} from "@publicdomainrelay/atproto-event-stream-abc";
import type { FirehoseRecordEvent } from "@publicdomainrelay/atproto-event-stream-common";
import { createATProtoRelay } from "@publicdomainrelay/atproto-event-stream-relay";
import { createATProtoJetstream } from "@publicdomainrelay/atproto-event-stream-jetstream";
import type { LoggerInterface } from "@publicdomainrelay/logger";

export type {
  ATProtoRelay,
  ATProtoJetstream,
  ATProtoEventStreamsClient,
  ATProtoEventStreamWatcher,
} from "@publicdomainrelay/atproto-event-stream-abc";
export type { FirehoseRecordEvent, FirehoseOperation } from "@publicdomainrelay/atproto-event-stream-common";

export function createATProtoEventStreamsClient(opts: {
  relays: ATProtoRelay[];
  jetstreams: ATProtoJetstream[];
  dedupWindowMs?: number;
}): ATProtoEventStreamsClient {
  const { relays, jetstreams, dedupWindowMs = 15_000 } = opts;
  const watchers: ATProtoEventStreamWatcher[] = [];

  // Batching: all watch() calls in the same synchronous block share one set
  // of underlying connections. Each registration specifies its own wantedCollections;
  // the union is passed to each stream (Jetstream filters server-side via repeated
  // wantedCollections params, relay filters client-side).
  let started = false;
  let _sharedTeardown: (() => void) | null = null;
  const registrations: Array<{
    wantedCollections: string[];
    onEvent(event: FirehoseRecordEvent): void | Promise<void>;
  }> = [];
  const sharedWatchers: ATProtoEventStreamWatcher[] = [];

  function createDedicatedWatcher(
    wo: { wantedCollections: string[]; onEvent(event: FirehoseRecordEvent): void | Promise<void>; log?: LoggerInterface },
  ): ATProtoEventStreamWatcher {
    const seen = new Map<string, number>();
    const dedicated: ATProtoEventStreamWatcher[] = [];

    const purgeTimer = setInterval(() => {
      const cutoff = Date.now() - dedupWindowMs;
      for (const [key, ts] of seen) {
        if (ts < cutoff) seen.delete(key);
      }
    }, Math.max(dedupWindowMs, 5_000));

    const onEvent = (event: FirehoseRecordEvent): void | Promise<void> => {
      const key = eventKey(event);
      const now = Date.now();
      const last = seen.get(key);
      if (last !== undefined && (now - last) < dedupWindowMs) return;
      seen.set(key, now);
      const r = wo.onEvent(event);
      if (r instanceof Promise) r.catch(() => {});
    };

    const streams = [
      ...relays.map((r) => r),
      ...jetstreams.map((j) => j),
    ];

    for (const stream of streams) {
      const w = stream.watch({ wantedCollections: wo.wantedCollections, onEvent, log: wo.log });
      dedicated.push(w);
      watchers.push(w);
    }

    return {
      close() {
        clearInterval(purgeTimer);
        for (const w of dedicated) {
          try { w.close(); } catch { /* ignore */ }
        }
      },
    };
  }

  function startStreams(): void {
    if (started || registrations.length === 0) return;
    started = true;

    const allCollections = [...new Set(registrations.flatMap((r) => r.wantedCollections))];
    const seen = new Map<string, number>();
    const purgeTimer = setInterval(() => {
      const cutoff = Date.now() - dedupWindowMs;
      for (const [key, ts] of seen) {
        if (ts < cutoff) seen.delete(key);
      }
    }, Math.max(dedupWindowMs, 5_000));

    const onEvent = (event: FirehoseRecordEvent): void | Promise<void> => {
      const key = eventKey(event);
      const now = Date.now();
      const last = seen.get(key);
      if (last !== undefined && (now - last) < dedupWindowMs) return;
      seen.set(key, now);

      for (const reg of registrations) {
        if (reg.wantedCollections.includes(event.collection)) {
          const r = reg.onEvent(event);
          if (r instanceof Promise) r.catch(() => {});
        }
      }
    };

    const streams = [
      ...relays.map((r) => ({ name: `relay:${r.url}`, stream: r })),
      ...jetstreams.map((j) => ({ name: `jetstream:${j.url}`, stream: j })),
    ];

    for (const { stream } of streams) {
      const w = stream.watch({ wantedCollections: allCollections, onEvent });
      sharedWatchers.push(w);
      watchers.push(w);
    }

    // Called when last registration closes.
    function teardown(): void {
      clearInterval(purgeTimer);
      for (const w of sharedWatchers) {
        try { w.close(); } catch { /* ignore */ }
      }
      sharedWatchers.length = 0;
      started = false;
    }

    _sharedTeardown = teardown;
  }

  return {
    relays,
    jetstreams,

    watch(wo): ATProtoEventStreamWatcher {
      const reg = { wantedCollections: [...wo.wantedCollections], onEvent: wo.onEvent };

      if (started) {
        // Late registration after initial batch — open dedicated connections.
        return createDedicatedWatcher(wo);
      }

      registrations.push(reg);
      queueMicrotask(startStreams);

      let closed = false;

      return {
        close() {
          if (closed) return;
          closed = true;
          const idx = registrations.indexOf(reg);
          if (idx >= 0) registrations.splice(idx, 1);

          if (registrations.length === 0 && _sharedTeardown) {
            _sharedTeardown();
            _sharedTeardown = null;
          }
        },
      };
    },

    close() {
      registrations.length = 0;
      for (const w of watchers) {
        try { w.close(); } catch { /* ignore */ }
      }
      watchers.length = 0;
    },
  };
}

export interface DefaultClientOpts {
  /** Additional relay URLs beyond defaults. */
  additionalRelays?: string[];
  /** Additional Jetstream URLs beyond defaults. */
  additionalJetstreams?: string[];
  /** Relay URLs to remove from the default set. */
  removeRelays?: string[];
  /** Jetstream URLs to remove from the default set. */
  removeJetstreams?: string[];
  /**
   * Time window in ms for deduplication. Events with the same AT-URI + CID
   * within this window are suppressed. Default 15 seconds.
   */
  dedupWindowMs?: number;
  log?: LoggerInterface;
}

export function createDefaultATProtoEventStreamsClient(
  opts: DefaultClientOpts = {},
): ATProtoEventStreamsClient {
  const removeRelay = new Set(opts.removeRelays ?? []);
  const removeJetstream = new Set(opts.removeJetstreams ?? []);

  const relayUrls = [
    ...DEFAULT_RELAY_URLS.filter((u) => !removeRelay.has(u)),
    ...(opts.additionalRelays ?? []),
  ];

  const jetstreamUrls = [
    ...DEFAULT_JETSTREAM_URLS.filter((u) => !removeJetstream.has(u)),
    ...(opts.additionalJetstreams ?? []),
  ];

  const relays = relayUrls.map((url) => createATProtoRelay(url, { log: opts.log }));
  const jetstreams = jetstreamUrls.map((url) => createATProtoJetstream(url, { log: opts.log }));

  opts.log?.info?.("event_streams_client_defaults", {
    relayCount: relays.length,
    relayUrls: relays.map((r) => r.url),
    jetstreamCount: jetstreams.length,
    jetstreamUrls: jetstreams.map((j) => j.url),
  });

  return createATProtoEventStreamsClient({ relays, jetstreams, dedupWindowMs: opts.dedupWindowMs });
}
