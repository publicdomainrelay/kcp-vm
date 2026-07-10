import type { FirehoseRecordEvent } from "@publicdomainrelay/atproto-event-stream-common";
import type {
  ATProtoRelay,
  ATProtoEventStreamWatcher,
} from "@publicdomainrelay/atproto-event-stream-abc";
import { createFirehoseWatcher } from "@publicdomainrelay/firehose-watcher-subscriberepos";
import type { LoggerInterface } from "@publicdomainrelay/logger";

function relayUrlToSubscribeReposUrl(relayUrl: string): string {
  const base = relayUrl.replace(/^https?:\/\//, "wss://").replace(/\/+$/, "");
  return `${base}/xrpc/com.atproto.sync.subscribeRepos`;
}

export function createATProtoRelay(
  relayUrl: string,
  opts?: { log?: LoggerInterface },
): ATProtoRelay {
  const url = relayUrl.replace(/\/+$/, "");
  const subscribeReposUrl = relayUrlToSubscribeReposUrl(url);
  const log = opts?.log;

  return {
    url,

    watch(wo): ATProtoEventStreamWatcher {
      return createFirehoseWatcher({
        url: subscribeReposUrl,
        wantedCollections: wo.wantedCollections,
        onRecord: wo.onEvent,
        log: wo.log ?? log,
      });
    },

    async listReposByCollection(
      collection: string,
      lo?: { limit?: number; timeoutMs?: number; log?: LoggerInterface },
    ): Promise<string[]> {
      const l = lo?.log ?? log;
      try {
        const queryUrl =
          `${url}/xrpc/com.atproto.sync.listReposByCollection?collection=${
            encodeURIComponent(collection)
          }&limit=${lo?.limit ?? 1000}`;
        l?.info?.("relay_list_repos_query", { url, collection });
        const res = await fetch(queryUrl, {
          signal: lo?.timeoutMs ? AbortSignal.timeout(lo.timeoutMs) : undefined,
        });
        if (!res.ok) {
          l?.warn?.("relay_list_repos_http_error", { url, status: res.status, collection });
          return [];
        }
        const data = await res.json() as { repos?: Array<{ did: string }> };
        const dids = [...new Set((data.repos ?? []).map((r) => r.did).filter(Boolean))];
        l?.info?.("relay_list_repos_result", { url, collection, count: dids.length });
        return dids;
      } catch (err) {
        l?.warn?.("relay_list_repos_error", { url, collection, error: String(err) });
        return [];
      }
    },
  };
}
