import { Hono } from "@hono/hono";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";

export interface IngressRef {
  ingressRef: string;
  readonly ingressUrl: string;
  readonly ingressHost: string;
  readonly name?: string;
  onServe(fetch: (req: Request) => Promise<Response>): Promise<void>;
  close(): void;
}

export interface ServeTcpOpts {
  name?: string;
  addr?: string;
  port?: number;
  cert?: string;
  key?: string;
  certFile?: string;
  keyFile?: string;
}

export interface ServeUnixOpts {
  name?: string;
  socketPath: string;
}

export type ServeListenerKind = "tcp" | "unix" | "relay";

export interface ServeListener {
  name: string;
  kind: ServeListenerKind;
  address: string;
}

export interface CreateServeOpts {
  logger?: StructuredLoggerInterface;
  tcp?: ServeTcpOpts | ServeTcpOpts[];
  unix?: ServeUnixOpts | ServeUnixOpts[];
  relays?: IngressRef[];
  portFile?: string;
  listenersFile?: string;
}

export const LISTENER_NAME_PATTERN = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

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
  addRelay(relay: IngressRef): Promise<void>;
  onConnected(cb: (ingressRef: string) => void | Promise<void>): void;
  beginServe(): Promise<void>;
  shutdown(): void;
  listenersWritten(): Promise<void>;
  readonly tcpPort: number;
  readonly listeners: ServeListener[];
}

interface ListenerReport {
  path: string;
  order: string[];
  entries: Map<string, ServeListener>;
  chain: Promise<void>;
}

const reports = new Map<string, ListenerReport>();

function reportFor(path: string): ListenerReport {
  let report = reports.get(path);
  if (!report) {
    report = { path, order: [], entries: new Map(), chain: Promise.resolve() };
    reports.set(path, report);
  }
  return report;
}

function claimName(report: ListenerReport | undefined, local: Set<string>, wanted: string): string {
  const taken = (name: string) => local.has(name) || (report?.order.includes(name) ?? false);
  let name = wanted;
  for (let n = 1; taken(name); n++) name = `${wanted}-${n}`;
  local.add(name);
  report?.order.push(name);
  return name;
}

function asList<T>(value: T | T[] | undefined): T[] {
  if (value === undefined) return [];
  return Array.isArray(value) ? value : [value];
}

export function tcpListenerAddress(hostname: string, port: number): string {
  return hostname.includes(":") ? `[${hostname}]:${port}` : `${hostname}:${port}`;
}

function absolutePath(path: string): string {
  if (path.startsWith("/")) return path;
  try {
    return `${Deno.cwd()}/${path}`;
  } catch {
    return path;
  }
}

function closeQuietly(relay: IngressRef): void {
  try {
    relay.close();
  } catch (_err) {
    return;
  }
}

export function createServe(opts: CreateServeOpts): ServeHandle {
  const app = new Hono();
  const logger = opts.logger;
  const tcpOpts = asList(opts.tcp);
  const unixOpts = asList(opts.unix);
  const relays: IngressRef[] = [...(opts.relays ?? [])];
  const onConnectedCallbacks: Array<(ingressRef: string) => void | Promise<void>> = [];
  const report = opts.listenersFile ? reportFor(opts.listenersFile) : undefined;
  const localNames = new Set<string>();
  const own: ServeListener[] = [];
  const servers: Deno.HttpServer[] = [];
  let controller: AbortController | null = null;
  let _tcpPort = 0;
  let relayCount = 0;

  function checkName(name: string | undefined): void {
    if (name !== undefined && !LISTENER_NAME_PATTERN.test(name)) {
      throw new Error(`createServe: listener name ${JSON.stringify(name)} is not a dns label`);
    }
  }

  function writeReport(): Promise<void> {
    if (!report) return Promise.resolve();
    report.chain = report.chain.then(async () => {
      const listeners = report.order.flatMap((name) => {
        const entry = report.entries.get(name);
        return entry ? [entry] : [];
      });
      try {
        await Deno.writeTextFile(report.path, JSON.stringify({ listeners }) + "\n");
        logger?.info("listeners file written", { path: report.path, listeners });
      } catch (err) {
        logger?.error?.("listeners file write failed", { path: report.path, error: String(err) });
      }
    });
    return report.chain;
  }

  function record(listener: ServeListener): void {
    own.push(listener);
    report?.entries.set(listener.name, listener);
  }

  function relayName(relay: IngressRef): string {
    checkName(relay.name);
    const wanted = relay.name ?? (relayCount === 0 ? "relay" : `relay-${relayCount}`);
    relayCount++;
    return claimName(report, localNames, wanted);
  }

  async function connectRelay(relay: IngressRef, name: string): Promise<void> {
    await relay.onServe(fetchAdapter);
    record({ name, kind: "relay", address: relay.ingressUrl });
    await writeReport();
  }

  function addRelay(relay: IngressRef): Promise<void> {
    relays.push(relay);
    if (!controller) return Promise.resolve();
    const name = relayName(relay);
    return connectRelay(relay, name).catch((err) => {
      logger?.error?.("relay onServe failed", { error: String(err) });
    });
  }

  function onConnected(cb: (ingressRef: string) => void | Promise<void>): void {
    onConnectedCallbacks.push(cb);
  }

  const fetchAdapter = (req: Request): Promise<Response> => {
    return Promise.resolve(app.fetch(req));
  };

  async function serveTcp(tcp: ServeTcpOpts, name: string, signal: AbortSignal): Promise<void> {
    const fromFiles = await readTlsFiles(tcp.certFile, tcp.keyFile);
    const certPem = tcp.cert ?? fromFiles.cert;
    const keyPem = tcp.key ?? fromFiles.key;
    const tlsEnabled = !!(certPem && keyPem);
    const tlsOpts = tlsEnabled ? { cert: certPem, key: keyPem } : {};
    const bound = Promise.withResolvers<{ hostname: string; port: number }>();
    const server = Deno.serve(
      {
        hostname: tcp.addr ?? "0.0.0.0",
        port: tcp.port ?? 0,
        signal,
        onListen: ({ hostname, port }) => {
          logger?.info("serve listening", { name, hostname, port, tls: tlsEnabled });
          bound.resolve({ hostname, port });
        },
        ...tlsOpts,
      },
      app.fetch,
    );
    servers.push(server);
    server.finished.catch((err) => {
      logger?.error?.("serve finished with error", { name, error: String(err) });
    });
    const { hostname, port } = await bound.promise;
    if (_tcpPort === 0) _tcpPort = port;
    record({ name, kind: "tcp", address: tcpListenerAddress(hostname, port) });
  }

  async function serveUnix(unix: ServeUnixOpts, name: string, signal: AbortSignal): Promise<void> {
    await Deno.remove(unix.socketPath).catch(() => undefined);
    const bound = Promise.withResolvers<string>();
    const server = Deno.serve(
      {
        path: unix.socketPath,
        signal,
        onListen: ({ path }) => {
          logger?.info("serve listening", { name, path });
          bound.resolve(path);
        },
      },
      app.fetch,
    );
    servers.push(server);
    server.finished.catch((err) => {
      logger?.error?.("serve finished with error", { name, error: String(err) });
    });
    record({ name, kind: "unix", address: absolutePath(await bound.promise) });
  }

  async function writePortFile(): Promise<void> {
    if (!opts.portFile || tcpOpts.length === 0) return;
    try {
      await Deno.writeTextFile(opts.portFile, String(_tcpPort));
      logger?.info("port file written", { path: opts.portFile, port: _tcpPort });
    } catch (err) {
      logger?.error?.("port file write failed", { path: opts.portFile, error: String(err) });
    }
  }

  async function beginServe(): Promise<void> {
    if (controller) return;

    if (tcpOpts.length === 0 && unixOpts.length === 0 && relays.length === 0) {
      throw new Error("createServe: at least one mode required (tcp, unix, or relays)");
    }
    for (const l of [...tcpOpts, ...unixOpts]) checkName(l.name);
    for (const relay of relays) checkName(relay.name);

    const tcpNames = tcpOpts.map((tcp, i) => claimName(report, localNames, tcp.name ?? (i === 0 ? "tcp" : `tcp-${i}`)));
    const unixNames = unixOpts.map((unix, i) => claimName(report, localNames, unix.name ?? (i === 0 ? "unix" : `unix-${i}`)));
    const relayNames = relays.map((relay) => relayName(relay));

    controller = new AbortController();
    const signal = controller.signal;

    for (const [i, tcp] of tcpOpts.entries()) await serveTcp(tcp, tcpNames[i], signal);
    for (const [i, unix] of unixOpts.entries()) await serveUnix(unix, unixNames[i], signal);
    await writePortFile();
    await writeReport();

    for (const [i, relay] of relays.entries()) await connectRelay(relay, relayNames[i]);

    const primaryProxyRef = relays[0]?.ingressRef ?? "";
    for (const cb of onConnectedCallbacks) {
      await cb(primaryProxyRef);
    }
  }

  function shutdown(): void {
    controller?.abort();
    for (const relay of relays) closeQuietly(relay);
    if (report) {
      for (const name of localNames) {
        report.entries.delete(name);
        report.order = report.order.filter((n) => n !== name);
      }
      localNames.clear();
      if (report.order.length === 0) reports.delete(report.path);
      void writeReport();
    }
  }

  function listenersWritten(): Promise<void> {
    return report?.chain ?? Promise.resolve();
  }

  return {
    app,
    addRelay,
    onConnected,
    beginServe,
    shutdown,
    listenersWritten,
    get tcpPort() {
      return _tcpPort;
    },
    get listeners() {
      return [...own];
    },
  };
}
