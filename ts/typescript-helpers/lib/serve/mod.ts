import { Hono } from "@hono/hono";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export interface RelayRef {
  proxyRef: string;
  onServe(fetch: (req: Request) => Promise<Response>): Promise<void>;
  close(): void;
}

export interface CreateServeOpts {
  logger?: StructuredLoggerInterface;
  tcp?: { addr?: string; port?: number };
  unix?: { socketPath: string };
  relays?: RelayRef[];
}

export interface ServeHandle {
  app: Hono;
  addRelay(relay: RelayRef): void;
  onConnected(cb: (proxyRef: string) => void | Promise<void>): void;
  beginServe(): Promise<void>;
  shutdown(): void;
}

export function createServe(opts: CreateServeOpts): ServeHandle {
  const app = new Hono();
  const logger = opts.logger;
  const relays: RelayRef[] = [...(opts.relays ?? [])];
  const onConnectedCallbacks: Array<(proxyRef: string) => void | Promise<void>> = [];
  let controller: AbortController | null = null;

  function addRelay(relay: RelayRef): void {
    relays.push(relay);
  }

  function onConnected(cb: (proxyRef: string) => void | Promise<void>): void {
    onConnectedCallbacks.push(cb);
  }

  const fetchAdapter = (req: Request): Promise<Response> => {
    return Promise.resolve(app.fetch(req));
  };

  async function beginServe(): Promise<void> {
    const hasTcp = opts.tcp !== undefined;
    const hasUnix = opts.unix !== undefined;
    const hasRelays = relays.length > 0;

    if (!hasTcp && !hasUnix && !hasRelays) {
      throw new Error("createServe: at least one mode required (tcp, unix, or relays)");
    }

    controller = new AbortController();

    if (hasTcp) {
      const { addr, port } = opts.tcp!;
      Deno.serve(
        {
          hostname: addr ?? "0.0.0.0",
          port: port ?? 0,
          signal: controller.signal,
          onListen: ({ hostname, port }) => {
            logger?.info("serve listening", { hostname, port });
          },
        },
        app.fetch,
      );
    } else if (hasUnix) {
      const { socketPath } = opts.unix!;
      try {
        await Deno.remove(socketPath);
      } catch { /* stale socket may not exist */ }
      Deno.serve(
        {
          path: socketPath,
          signal: controller.signal,
          onListen: ({ path }) => {
            logger?.info("serve listening", { path });
          },
        },
        app.fetch,
      );
    }

    for (const relay of relays) {
      await relay.onServe(fetchAdapter);
    }

    const primaryProxyRef = relays[0]?.proxyRef ?? "";
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

  return { app, addRelay, onConnected, beginServe, shutdown };
}
