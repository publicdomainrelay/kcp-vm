import { Hono } from "@hono/hono";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export interface IngressRef {
  ingressRef: string;
  readonly ingressUrl: string;
  readonly ingressHost: string;
  onServe(fetch: (req: Request) => Promise<Response>): Promise<void>;
  close(): void;
}

export interface ServeTcpOpts {
  addr?: string;
  port?: number;
  /** PEM certificate, inline. Takes precedence over certFile. */
  cert?: string;
  /** PEM private key, inline. Takes precedence over keyFile. */
  key?: string;
  /** PEM certificate file, read at beginServe. Requires keyFile. */
  certFile?: string;
  /** PEM private key file, read at beginServe. Requires certFile. */
  keyFile?: string;
}

export interface CreateServeOpts {
  logger?: StructuredLoggerInterface;
  tcp?: ServeTcpOpts;
  unix?: { socketPath: string };
  relays?: IngressRef[];
  /**
   * When set, the bound TCP port is written to this file once listening, so a
   * caller that asked for port 0 can discover the port. Ignored without tcp.
   */
  portFile?: string;
}

/**
 * Read a certificate/key pair from disk. Both must be given: with either
 * missing the server stays plain HTTP, so a deployment that does not opt in
 * is unaffected.
 */
export async function readTlsFiles(
  certFile: unknown,
  keyFile: unknown,
): Promise<{ cert?: string; key?: string }> {
  if (typeof certFile !== "string" || typeof keyFile !== "string" || !certFile || !keyFile) return {};
  return {
    cert: await Deno.readTextFile(certFile),
    key: await Deno.readTextFile(keyFile),
  };
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
      const { addr, port, cert, key, certFile, keyFile } = opts.tcp!;
      // Inline PEM wins over the file for the half it supplies, so passing one
      // inline and one as a path does what it reads like.
      const fromFiles = await readTlsFiles(certFile, keyFile);
      const certPem = cert ?? fromFiles.cert;
      const keyPem = key ?? fromFiles.key;
      const tlsEnabled = !!(certPem && keyPem);
      const tlsOpts = tlsEnabled ? { cert: certPem, key: keyPem } : {};
      _httpServer = Deno.serve(
        {
          hostname: addr ?? "0.0.0.0",
          port: port ?? 0,
          signal: controller.signal,
          onListen: ({ hostname, port }) => {
            _tcpPort = port;
            logger?.info("serve listening", { hostname, port, tls: tlsEnabled });
          },
          ...tlsOpts,
        },
        app.fetch,
      );
      // Deno.serve invokes onListen before it returns, so _tcpPort is the port
      // that was actually bound -- which is the point when port 0 was asked for.
      // Best effort: the file is a convenience for whoever needs to discover the
      // port, and a read-only working directory must not take the server down.
      if (opts.portFile) {
        try {
          await Deno.writeTextFile(opts.portFile, String(_tcpPort));
          logger?.info("port file written", { path: opts.portFile, port: _tcpPort });
        } catch (err) {
          logger?.error?.("port file write failed", { path: opts.portFile, error: String(err) });
        }
      }
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
