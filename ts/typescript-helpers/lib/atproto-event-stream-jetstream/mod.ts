import type { ATProtoJetstream, ATProtoEventStreamWatcher } from "@publicdomainrelay/atproto-event-stream-abc";
import { createFirehoseWatcher } from "@publicdomainrelay/firehose-watcher-jetstream";
import type { LoggerInterface } from "@publicdomainrelay/logger";

export function createATProtoJetstream(
  jetstreamUrl: string,
  opts?: { log?: LoggerInterface },
): ATProtoJetstream {
  const url = jetstreamUrl.replace(/\/+$/, "");
  const log = opts?.log;

  return {
    url,

    watch(wo): ATProtoEventStreamWatcher {
      return createFirehoseWatcher({
        url,
        wantedCollections: wo.wantedCollections,
        cursor: wo.cursor,
        onRecord: wo.onEvent,
        onProgress: wo.onProgress,
        log: wo.log ?? log,
      });
    },
  };
}
