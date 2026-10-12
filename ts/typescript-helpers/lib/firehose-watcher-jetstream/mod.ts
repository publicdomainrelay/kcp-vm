import type {
  FirehoseOperation,
  FirehoseRecordEvent,
  FirehoseWatcher,
  FirehoseWatcherOptions,
} from "@publicdomainrelay/firehose-watcher-abc";

interface JetstreamCommit {
  collection?: string;
  operation?: string;
  rkey?: string;
  cid?: string;
}

interface JetstreamFrame {
  did?: string;
  time_us?: number;
  kind?: string;
  commit?: JetstreamCommit;
}

const OPERATIONS: Record<string, FirehoseOperation> = {
  create: "create",
  update: "update",
  delete: "delete",
};

export function createFirehoseWatcher(
  opts: FirehoseWatcherOptions,
): FirehoseWatcher {
  const { url, onRecord, log } = opts;
  const wanted = new Set(opts.wantedCollections);
  let cursor = opts.cursor;
  let retryCount = 0;
  let closed = false;
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  function endpoint(): string {
    const u = new URL(url);
    for (const collection of opts.wantedCollections) {
      u.searchParams.append("wantedCollections", collection);
    }
    if (cursor !== undefined && cursor !== null) {
      u.searchParams.set("cursor", String(cursor));
    }
    return u.toString();
  }

  function emit(frame: JetstreamFrame): void {
    if (frame.kind !== "commit") return;
    const did = frame.did;
    const commit = frame.commit;
    if (!did || !commit?.collection || !commit.rkey) return;
    if (wanted.size > 0 && !wanted.has(commit.collection)) return;
    const operation = OPERATIONS[commit.operation ?? ""];
    if (!operation) return;
    Promise.resolve(
      onRecord({
        did,
        collection: commit.collection,
        rkey: commit.rkey,
        cid: commit.cid ?? "",
        operation,
        uri: `at://${did}/${commit.collection}/${commit.rkey}`,
      } satisfies FirehoseRecordEvent),
    ).catch((err) => log?.error("firehose_onrecord_failed", { err: String(err) }));
  }

  function connect(): void {
    if (closed) return;
    log?.info("firehose_connecting", { url, cursor });
    try {
      ws = new WebSocket(endpoint());
    } catch (err) {
      log?.error("firehose_ws_constructor_failed", { url, err: String(err) });
      scheduleReconnect();
      return;
    }

    ws.onopen = () => {
      log?.info("firehose_connected", { url });
    };

    ws.onmessage = (event: MessageEvent) => {
      retryCount = 0;
      try {
        if (typeof event.data !== "string") return;
        const frame = JSON.parse(event.data) as JetstreamFrame;
        if (typeof frame.time_us === "number") cursor = frame.time_us;
        emit(frame);
      } catch {
        // skip malformed frames
      }
    };

    ws.onerror = () => {
      log?.warn("firehose_ws_error", { url });
    };

    ws.onclose = () => {
      log?.info("firehose_disconnected", { url });
      ws = null;
      scheduleReconnect();
    };
  }

  function scheduleReconnect(): void {
    if (closed) return;
    const delay = Math.min(1000 * Math.pow(2, retryCount), 30000);
    retryCount++;
    log?.info("firehose_reconnect_scheduled", { url, delayMs: delay, retryCount });
    reconnectTimer = setTimeout(connect, delay);
  }

  function close(): void {
    closed = true;
    if (reconnectTimer !== null) clearTimeout(reconnectTimer);
    try {
      ws?.close();
    } catch {
      // ignore
    }
    ws = null;
  }

  connect();
  return { close };
}
