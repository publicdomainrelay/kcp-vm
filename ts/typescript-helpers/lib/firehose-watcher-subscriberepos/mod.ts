import type {
  FirehoseOperation,
  FirehoseRecordEvent,
  FirehoseWatcher,
  FirehoseWatcherOptions,
} from "@publicdomainrelay/firehose-watcher-abc";

interface SubscribeReposOp {
  action?: string;
  path?: string;
  cid?: { $link?: string } | null;
}

interface SubscribeReposFrame {
  seq?: number;
  repo?: string;
  ops?: SubscribeReposOp[];
}

interface RelayEnvelope {
  seq?: number;
  frame?: SubscribeReposFrame;
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
    if (cursor === undefined || cursor === null) return url;
    const sep = url.includes("?") ? "&" : "?";
    return `${url}${sep}cursor=${cursor}`;
  }

  function emit(frame: SubscribeReposFrame): void {
    const did = frame.repo;
    if (!did || !Array.isArray(frame.ops)) return;
    for (const op of frame.ops) {
      const path = op.path;
      if (!path) continue;
      const slashIdx = path.indexOf("/");
      if (slashIdx <= 0) continue;
      const collection = path.slice(0, slashIdx);
      if (wanted.size > 0 && !wanted.has(collection)) continue;
      const operation = OPERATIONS[op.action ?? ""];
      if (!operation) continue;
      const rkey = path.slice(slashIdx + 1);
      const cid = op.cid?.$link ?? "";
      Promise.resolve(
        onRecord({
          did,
          collection,
          rkey,
          cid,
          operation,
          uri: `at://${did}/${collection}/${rkey}`,
        } satisfies FirehoseRecordEvent),
      ).catch((err) => log?.error("firehose_onrecord_failed", { err: String(err) }));
    }
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
        const raw = JSON.parse(event.data) as RelayEnvelope & SubscribeReposFrame;
        const frame = raw.frame ?? raw;
        const seq = typeof raw.seq === "number" ? raw.seq : frame.seq;
        if (typeof seq === "number") cursor = seq;
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
