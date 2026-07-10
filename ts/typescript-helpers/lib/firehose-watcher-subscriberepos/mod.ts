import type {
  FirehoseOperation,
  FirehoseRecordEvent,
  FirehoseWatcher,
  FirehoseWatcherOptions,
} from "@publicdomainrelay/firehose-watcher-abc";
import type { SubscribeReposFrame } from "@publicdomainrelay/firehose-common";
import { splitRecordPath } from "@publicdomainrelay/firehose-common";

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

  function emit(frame: Partial<SubscribeReposFrame>): void {
    const did = frame.repo;
    if (!did || !Array.isArray(frame.ops)) return;
    for (const op of frame.ops) {
      const parsed = splitRecordPath(op.path ?? "");
      if (!parsed) continue;
      if (wanted.size > 0 && !wanted.has(parsed.collection)) continue;
      const operation = OPERATIONS[op.action ?? ""];
      if (!operation) continue;
      const cid = op.cid?.$link ?? "";
      Promise.resolve(
        onRecord({
          did,
          collection: parsed.collection,
          rkey: parsed.rkey,
          cid,
          operation,
          uri: `at://${did}/${parsed.collection}/${parsed.rkey}`,
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
        const frame = JSON.parse(event.data) as Partial<SubscribeReposFrame>;
        if (typeof frame.seq === "number") cursor = frame.seq;
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
