import { fetchWithRetry, retryNoteOf } from "@publicdomainrelay/http-retry-fetch";

export interface PasswordSessionConfig {
  service: string;
  identifier: string;
  password: string;
}

export interface PasswordSessionAgent {
  did: string;
  sessionData: { userDid: string; handle: string; pds: string };
  createRecord(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  putRecord(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  getRecord(did: string, collection: string, rkey: string): Promise<{ uri: string; cid: string; value: Record<string, unknown> } | null>;
  listRecords(
    did: string,
    collection: string,
    opts?: { limit?: number },
  ): Promise<{ records: Array<{ uri: string; cid: string; value: Record<string, unknown> }> }>;
  getServiceAuth(aud: string, lxm?: string): Promise<string>;
  dispose(): void;
}

export function parsePasswordSessions(raw: unknown): Map<string, PasswordSessionConfig> {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error("password sessions must be a JSON object keyed by DID");
  }
  const out = new Map<string, PasswordSessionConfig>();
  for (const [did, value] of Object.entries(raw as Record<string, unknown>)) {
    const v = value as Record<string, unknown> | null;
    if (!did.startsWith("did:") || !v || typeof v.service !== "string" || typeof v.identifier !== "string" || typeof v.password !== "string") {
      throw new Error(`password session for ${did} needs a did key and string service, identifier and password`);
    }
    out.set(did, { service: v.service.replace(/\/+$/, ""), identifier: v.identifier, password: v.password });
  }
  return out;
}

interface Session {
  accessJwt: string;
  did: string;
  handle: string;
}

export async function createPasswordSessionAgent(
  cfg: PasswordSessionConfig,
  deps: {
    fetch?: typeof fetch;
    onRetry?: (event: import("@publicdomainrelay/http-retry-fetch").RetryEvent) => void;
    onGiveUp?: (event: import("@publicdomainrelay/http-retry-fetch").GiveUpEvent) => void;
  } = {},
): Promise<PasswordSessionAgent> {
  const doFetch = deps.fetch ?? ((input: string | URL | Request, init?: RequestInit) => fetch(input, init));
  const xrpc = (nsid: string) => `${cfg.service}/xrpc/${nsid}`;

  async function login(): Promise<Session> {
    const res = await doFetch(xrpc("com.atproto.server.createSession"), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ identifier: cfg.identifier, password: cfg.password }),
    });
    const body = await res.json().catch(() => ({})) as Record<string, unknown>;
    if (!res.ok || typeof body.accessJwt !== "string" || typeof body.did !== "string") {
      throw new Error(`createSession at ${cfg.service} for ${cfg.identifier} failed: HTTP ${res.status} ${JSON.stringify(body)}`);
    }
    return { accessJwt: body.accessJwt, did: body.did, handle: typeof body.handle === "string" ? body.handle : "" };
  }

  let session = await login();

  async function call(nsid: string, init: { method: "GET" | "POST"; query?: Record<string, string>; body?: unknown }): Promise<Response> {
    const url = new URL(xrpc(nsid));
    for (const [k, v] of Object.entries(init.query ?? {})) url.searchParams.set(k, v);
    const send = () =>
      fetchWithRetry(() =>
        doFetch(url, {
          method: init.method,
          headers: {
            authorization: `Bearer ${session.accessJwt}`,
            ...(init.body === undefined ? {} : { "content-type": "application/json" }),
          },
          ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
        }), {
        label: `${init.method} ${nsid}`,
        ...(deps.onRetry ? { onRetry: deps.onRetry } : {}),
        ...(deps.onGiveUp ? { onGiveUp: deps.onGiveUp } : {}),
      });
    let res = await send();
    if (res.status === 401) {
      await res.body?.cancel();
      session = await login();
      res = await send();
    }
    return res;
  }

  async function json(nsid: string, init: { method: "GET" | "POST"; query?: Record<string, string>; body?: unknown }) {
    const res = await call(nsid, init);
    const body = await res.json().catch(() => ({})) as Record<string, unknown>;
    if (!res.ok) {
      const note = retryNoteOf(res);
      throw new Error(`${nsid} at ${cfg.service} failed: HTTP ${res.status} ${JSON.stringify(body)}${note === "" ? "" : ` (${note})`}`);
    }
    return body;
  }

  const ref = (b: Record<string, unknown>) => ({ uri: String(b.uri), cid: String(b.cid) });

  return {
    did: session.did,
    sessionData: { userDid: session.did, handle: session.handle, pds: cfg.service },
    async createRecord(repo, collection, rkey, record) {
      return ref(await json("com.atproto.repo.createRecord", { method: "POST", body: { repo, collection, rkey, record } }));
    },
    async putRecord(repo, collection, rkey, record) {
      return ref(await json("com.atproto.repo.putRecord", { method: "POST", body: { repo, collection, rkey, record } }));
    },
    async getRecord(repo, collection, rkey) {
      const res = await call("com.atproto.repo.getRecord", { method: "GET", query: { repo, collection, rkey } });
      if (res.status === 400 || res.status === 404) {
        await res.body?.cancel();
        return null;
      }
      const body = await res.json() as Record<string, unknown>;
      return { uri: String(body.uri), cid: String(body.cid), value: body.value as Record<string, unknown> };
    },
    async listRecords(repo, collection, opts) {
      const body = await json("com.atproto.repo.listRecords", {
        method: "GET",
        query: { repo, collection, ...(opts?.limit ? { limit: String(opts.limit) } : {}) },
      });
      return { records: (body.records as Array<{ uri: string; cid: string; value: Record<string, unknown> }>) ?? [] };
    },
    async getServiceAuth(aud, lxm) {
      const body = await json("com.atproto.server.getServiceAuth", {
        method: "GET",
        query: { aud, ...(lxm ? { lxm } : {}) },
      });
      if (typeof body.token !== "string") throw new Error(`getServiceAuth at ${cfg.service} returned no token`);
      return body.token;
    },
    dispose() {},
  };
}
