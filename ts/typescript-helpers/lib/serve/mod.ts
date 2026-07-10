import { Hono } from "@hono/hono";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export interface IngressRef {
  ingressRef: string;
  readonly ingressUrl: string;
  readonly ingressHost: string;
  onServe(fetch: (req: Request) => Promise<Response>): Promise<void>;
  close(): void;
}

export interface CreateServeOpts {
  logger?: StructuredLoggerInterface;
  tcp?: { addr?: string; port?: number };
  unix?: { socketPath: string };
  relays?: IngressRef[];
}

export interface ServeHandle {
  app: Hono;
  addRelay(relay: IngressRef): void;
  onConnected(cb: (ingressRef: string) => void | Promise<void>): void;
  beginServe(): Promise<void>;
  shutdown(): void;
  /** TCP port resolved during beginServe (0 if port 0 was passed but not yet started, or if no TCP). */
  readonly tcpPort: number;
}

export function createServe(opts: CreateServeOpts): ServeHandle {
  const app = new Hono();
  const logger = opts.logger;
  const relays: IngressRef[] = [...(opts.relays ?? [])];
  const onConnectedCallbacks: Array<(ingressRef: string) => void | Promise<void>> = [];
  let controller: AbortController | null = null;
  let _tcpPort = 0;
  let _httpServer: Deno.HttpServer | null = null;

  function addRelay(relay: IngressRef): void {
    relays.push(relay);
    // If serve has already begun, connect the relay immediately.
    // Otherwise it will be connected during beginServe().
    if (controller) {
      relay.onServe(fetchAdapter).catch((err) => {
        logger?.error?.("relay onServe failed", { error: String(err) });
      });
    }
  }

  function onConnected(cb: (ingressRef: string) => void | Promise<void>): void {
    onConnectedCallbacks.push(cb);
  }

  const fetchAdapter = (req: Request): Promise<Response> => {
    return Promise.resolve(app.fetch(req));
  };

  async function beginServe(): Promise<void> {
    // Idempotent: if already begun (controller set), skip.
    if (controller) return;

    const hasTcp = opts.tcp !== undefined;
    const hasUnix = opts.unix !== undefined;
    const hasRelays = relays.length > 0;

    if (!hasTcp && !hasUnix && !hasRelays) {
      throw new Error("createServe: at least one mode required (tcp, unix, or relays)");
    }

    controller = new AbortController();

    if (hasTcp) {
      const { addr, port } = opts.tcp!;
      _httpServer = Deno.serve(
        {
          hostname: addr ?? "0.0.0.0",
          port: port ?? 0,
          signal: controller.signal,
          onListen: ({ hostname, port }) => {
            _tcpPort = port;
            logger?.info("serve listening", { hostname, port });
          },
        },
        app.fetch,
      );
      _httpServer.finished.catch((err) => {
        logger?.error?.("serve finished with error", { error: String(err) });
      });
    } else if (hasUnix) {
      const { socketPath } = opts.unix!;
      try {
        await Deno.remove(socketPath);
      } catch { /* stale socket may not exist */ }
      _httpServer = Deno.serve(
        {
          path: socketPath,
          signal: controller.signal,
          onListen: ({ path }) => {
            logger?.info("serve listening", { path });
          },
        },
        app.fetch,
      );
      _httpServer.finished.catch((err) => {
        logger?.error?.("serve finished with error", { error: String(err) });
      });
    }

    for (const relay of relays) {
      await relay.onServe(fetchAdapter);
    }

    const primaryProxyRef = relays[0]?.ingressRef ?? "";
    for (const cb of onConnectedCallbacks) {
      await cb(primaryProxyRef);
    }
  }

  function shutdown(): void {
    controller?.abort();
    for (const relay of relays) {
      try { relay.close(); } catch { /* best effort */ }
    }
  }

  return { app, addRelay, onConnected, beginServe, shutdown, get tcpPort() { return _tcpPort; } };
}
