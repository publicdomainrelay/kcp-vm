import { type CoalescedStatusWriter, type KcpClient, nextWriteDelayMs } from "@publicdomainrelay/kcp-client-abc";
import {
  isConflict,
  isNotFound,
  KcpError,
  type KcpCredentials,
  type KcpListOptions,
  type KcpTarget,
  type KcpWatchEvent,
  patchBody,
  patchContentType,
  resourcePath,
  resourceVersionOf,
  serverRoot,
  splitWatchLines,
  workspaceOfServer,
} from "@publicdomainrelay/kcp-client-common";

export interface KcpRestClientOptions {
  fetch?: typeof fetch;
  timeoutMs?: number;
  tokenMaxAgeMs?: number;
  watchTimeoutSeconds?: number;
  conflictRetries?: number;
}

const DEFAULT_TIMEOUT_MS = 15_000;
const DEFAULT_TOKEN_MAX_AGE_MS = 60_000;
const DEFAULT_WATCH_TIMEOUT_SECONDS = 300;
const DEFAULT_CONFLICT_RETRIES = 5;
const DEFAULT_RETRY_AFTER_MS = 5_000;
const MAX_RETRY_AFTER_MS = 60_000;
const MIN_QUIET_STREAM_MS = 1_000;

interface SendInit {
  body?: string;
  contentType?: string;
  signal: AbortSignal;
}

function retryAfterMs(res: Response): number {
  const raw = res.headers.get("retry-after");
  const seconds = raw === null ? Number.NaN : Number(raw);
  const ms = Number.isFinite(seconds) ? seconds * 1_000 : Date.parse(raw ?? "") - Date.now();
  const wait = Number.isFinite(ms) && ms > 0 ? ms : DEFAULT_RETRY_AFTER_MS;
  return Math.min(wait, MAX_RETRY_AFTER_MS);
}

function selectorQuery(opts: KcpListOptions | undefined, q: URLSearchParams): URLSearchParams {
  if (opts?.labelSelector) q.set("labelSelector", opts.labelSelector);
  if (opts?.fieldSelector) q.set("fieldSelector", opts.fieldSelector);
  return q;
}

export function createKcpRestClient(credentials: KcpCredentials, opts: KcpRestClientOptions = {}): KcpClient {
  const root = serverRoot(credentials.server);
  const defaultWorkspace = workspaceOfServer(credentials.server);
  const baseFetch = opts.fetch ?? fetch;
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const tokenMaxAgeMs = opts.tokenMaxAgeMs ?? DEFAULT_TOKEN_MAX_AGE_MS;
  const watchTimeoutSeconds = opts.watchTimeoutSeconds ?? DEFAULT_WATCH_TIMEOUT_SECONDS;
  const conflictRetries = opts.conflictRetries ?? DEFAULT_CONFLICT_RETRIES;
  let token: { value: string | undefined; readAt: number } | undefined;
  let httpClient: Promise<Deno.HttpClient | undefined> | undefined;
  let readsPausedUntil = 0;

  async function bearer(reread: boolean): Promise<string | undefined> {
    if (!credentials.tokenFile) return credentials.token;
    if (!reread && token && Date.now() - token.readAt < tokenMaxAgeMs) return token.value;
    try {
      const value = (await Deno.readTextFile(credentials.tokenFile)).trim() || credentials.token;
      token = { value, readAt: Date.now() };
    } catch (err) {
      if (!token) throw err;
    }
    return token.value;
  }

  function client(): Promise<Deno.HttpClient | undefined> {
    if (opts.fetch) return Promise.resolve(undefined);
    httpClient ??= (async () => {
      const ca = credentials.caCert ?? (credentials.caFile ? await Deno.readTextFile(credentials.caFile) : undefined);
      const mtls = credentials.clientCert && credentials.clientKey
        ? { cert: credentials.clientCert, key: credentials.clientKey }
        : {};
      if (!ca && !("cert" in mtls)) return undefined;
      return Deno.createHttpClient({ ...(ca ? { caCerts: [ca] } : {}), ...mtls });
    })();
    return httpClient;
  }

  function pathOf(target: KcpTarget, name?: string, subresource?: string): string {
    return resourcePath({ ...target, workspace: target.workspace ?? defaultWorkspace }, name, subresource);
  }

  async function send(method: string, path: string, init: SendInit): Promise<Response> {
    const http = await client();
    for (let attempt = 0;; attempt++) {
      const headers = new Headers({ accept: "application/json" });
      if (init.contentType) headers.set("content-type", init.contentType);
      const bearerToken = await bearer(attempt > 0);
      if (bearerToken) headers.set("authorization", `Bearer ${bearerToken}`);
      const res = await baseFetch(`${root}${path}`, {
        method,
        headers,
        body: init.body,
        signal: init.signal,
        ...(http ? { client: http } : {}),
      } as RequestInit);
      if (res.status === 401 && attempt === 0 && credentials.tokenFile) {
        await res.body?.cancel();
        continue;
      }
      return res;
    }
  }

  async function request(method: string, path: string, body?: string, contentType?: string): Promise<unknown> {
    if (method === "GET" && Date.now() < readsPausedUntil) {
      throw new KcpError(429, method, path.split("?")[0], `held back until ${new Date(readsPausedUntil).toISOString()}: kcp rate limited this client`);
    }
    const res = await send(method, path, { body, contentType, signal: AbortSignal.timeout(timeoutMs) });
    const text = await res.text();
    if (!res.ok) {
      if (res.status === 429) readsPausedUntil = Date.now() + retryAfterMs(res);
      throw new KcpError(res.status, method, path.split("?")[0], text);
    }
    return text ? JSON.parse(text) : null;
  }

  async function get(target: KcpTarget, name: string): Promise<unknown | null> {
    try {
      return await request("GET", pathOf(target, name));
    } catch (err) {
      if (isNotFound(err)) return null;
      throw err;
    }
  }

  async function* watch(target: KcpTarget, wopts: Parameters<KcpClient["watch"]>[1]): AsyncGenerator<KcpWatchEvent> {
    const path = pathOf(target);
    const reconnect = wopts.reconnect ?? true;
    let resourceVersion = wopts.resourceVersion;
    while (!wopts.signal.aborted) {
      const q = selectorQuery(wopts, new URLSearchParams({
        watch: "true",
        allowWatchBookmarks: "true",
        timeoutSeconds: String(watchTimeoutSeconds),
      }));
      if (resourceVersion) q.set("resourceVersion", resourceVersion);
      const openedAt = Date.now();
      let res: Response;
      try {
        res = await send("GET", `${path}?${q}`, { signal: wopts.signal });
      } catch (err) {
        if (wopts.signal.aborted) return;
        throw err;
      }
      if (!res.ok || !res.body) {
        const text = await res.text().catch(() => "");
        throw new KcpError(res.status, "WATCH", path, text);
      }
      const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
      let events = 0;
      let buffered = "";
      try {
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          const split = splitWatchLines(buffered + value);
          buffered = split.rest;
          for (const line of split.lines) {
            const event = JSON.parse(line) as KcpWatchEvent;
            events++;
            if (event.type === "ERROR") {
              yield event;
              return;
            }
            resourceVersion = resourceVersionOf(event.object) ?? resourceVersion;
            yield event;
          }
        }
      } catch (err) {
        if (wopts.signal.aborted) return;
        if (events === 0) throw err;
      } finally {
        await reader.cancel().catch(() => {});
      }
      if (!reconnect) return;
      if (events === 0 && Date.now() - openedAt < MIN_QUIET_STREAM_MS) {
        throw new KcpError(503, "WATCH", path, "the watch stream closed at once with no event");
      }
    }
  }

  async function patch(
    target: KcpTarget,
    name: string,
    p: Parameters<KcpClient["patch"]>[2],
    popts?: Parameters<KcpClient["patch"]>[3],
  ): Promise<unknown> {
    const query = p.type === "apply"
      ? `?fieldManager=${encodeURIComponent(p.fieldManager)}${p.force ? "&force=true" : ""}`
      : "";
    return await request("PATCH", `${pathOf(target, name, popts?.subresource)}${query}`, patchBody(p), patchContentType(p));
  }

  return {
    server: root,
    get,
    async list(target, lopts) {
      const q = selectorQuery(lopts, new URLSearchParams()).toString();
      const out = await request("GET", `${pathOf(target)}${q ? `?${q}` : ""}`) as {
        metadata?: { resourceVersion?: string };
        items?: unknown[];
      };
      return { items: out?.items ?? [], resourceVersion: out?.metadata?.resourceVersion ?? "" };
    },
    watch,
    async create(target, object) {
      return await request("POST", pathOf(target), JSON.stringify(object), "application/json");
    },
    async createIfAbsent(target, object) {
      try {
        return { object: await request("POST", pathOf(target), JSON.stringify(object), "application/json"), created: true };
      } catch (err) {
        if (!(err instanceof KcpError && err.status === 409)) throw err;
        const name = object.metadata?.name;
        const existing = name ? await get(target, name) : null;
        if (existing === null) throw err;
        return { object: existing, created: false };
      }
    },
    patch,
    async patchStatus(target, name, next, sopts) {
      const retries = sopts?.retries ?? conflictRetries;
      for (let attempt = 0;; attempt++) {
        const current = await get(target, name);
        if (current === null) return null;
        const status = next(current);
        if (status === null) return current;
        const rv = resourceVersionOf(current);
        const ops = [
          ...(rv ? [{ op: "test", path: "/metadata/resourceVersion", value: rv }] : []),
          { op: "add", path: "/status", value: status },
        ];
        try {
          return await patch(target, name, { type: "json", ops }, { subresource: "status" });
        } catch (err) {
          if (isNotFound(err)) return null;
          if (isConflict(err) && attempt < retries) continue;
          throw err;
        }
      }
    },
    async delete(target, name, preconditions) {
      const body = preconditions
        ? JSON.stringify({ apiVersion: "v1", kind: "DeleteOptions", preconditions })
        : undefined;
      try {
        await request("DELETE", pathOf(target, name), body, body ? "application/json" : undefined);
        return true;
      } catch (err) {
        if (isNotFound(err)) return false;
        throw err;
      }
    },
  };
}

export interface CoalescedStatusWriterOptions<T> {
  write(value: T): Promise<unknown>;
  minIntervalMs?: number;
  onError?(err: unknown): void;
}

const DEFAULT_STATUS_MIN_INTERVAL_MS = 1_000;

export function createCoalescedStatusWriter<T>(opts: CoalescedStatusWriterOptions<T>): CoalescedStatusWriter<T> {
  const minIntervalMs = opts.minIntervalMs ?? DEFAULT_STATUS_MIN_INTERVAL_MS;
  let pending: { value: T } | undefined;
  let lastWriteAt: number | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let chain: Promise<void> = Promise.resolve();
  let closed = false;

  function drain(): Promise<void> {
    chain = chain.then(async () => {
      const next = pending;
      pending = undefined;
      if (!next) return;
      lastWriteAt = Date.now();
      try {
        await opts.write(next.value);
      } catch (err) {
        opts.onError?.(err);
      }
    });
    return chain;
  }

  function schedule(urgent: boolean): void {
    if (timer !== undefined) {
      if (!urgent) return;
      clearTimeout(timer);
    }
    const delay = urgent ? 0 : nextWriteDelayMs(lastWriteAt, Date.now(), minIntervalMs);
    timer = setTimeout(() => {
      timer = undefined;
      void drain();
    }, delay);
  }

  function flush(): Promise<void> {
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
    return drain();
  }

  return {
    set(value, urgent = false) {
      if (closed) return;
      pending = { value };
      schedule(urgent);
    },
    flush,
    async close() {
      closed = true;
      await flush();
    },
  };
}

export function replaceStatusWrite(kcp: KcpClient, target: KcpTarget, name: string): (status: unknown) => Promise<unknown> {
  return (status) =>
    kcp.patch(target, name, { type: "json", ops: [{ op: "add", path: "/status", value: status }] }, { subresource: "status" });
}

export function statusFieldWrite(
  kcp: KcpClient,
  target: KcpTarget,
  name: string,
  field: string,
): (value: unknown) => Promise<unknown> {
  return (value) => kcp.patch(target, name, { type: "merge", body: { status: { [field]: value } } }, { subresource: "status" });
}
