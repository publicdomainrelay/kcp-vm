import {
  createNonceBook,
  DPOP_NONCE_HEADER,
  isUseDpopNonce,
  originOf,
  SessionCustodyError,
} from "@publicdomainrelay/atproto-session-custody-abc";
import type {
  CustodyAgent,
  CustodyCompleteInput,
  CustodyIdentity,
  CustodyLogin,
  CustodyLoginStart,
  CustodyRecord,
  CustodySession,
  CustodyStartInput,
  NonceBook,
  ProofRequest,
  SessionCustody,
  SessionCustodyConnector,
  SessionProof,
  SignOutResult,
  TokenCustody,
} from "@publicdomainrelay/atproto-session-custody-abc";

export interface OpenBaoEndpoint {
  address: string;
  caCert?: string;
  clientCert?: string;
  clientKey?: string;
  certLogin?: { mount: string; role: string };
}

export interface OpenBaoSessionCustodyOptions {
  endpoint?: () => Promise<OpenBaoEndpoint>;
  address?: string;
  caCert?: string;
  mount?: string;
  token?: string | (() => string | Promise<string>);
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_MOUNT = "atproto";
const DEFAULT_TIMEOUT_MS = 15_000;
const TOKEN_RENEW_BEFORE_MS = 60_000;

interface Connected {
  endpoint: OpenBaoEndpoint;
  client?: Deno.HttpClient;
}

interface BaoReply {
  status: number;
  json: Record<string, unknown> | null;
}

function errorsOf(reply: BaoReply): string {
  const errors = reply.json?.errors;
  return Array.isArray(errors) ? errors.join("; ") : JSON.stringify(reply.json);
}

function dataOf(reply: BaoReply, operation: string): Record<string, unknown> {
  const data = reply.json?.data;
  if (!data || typeof data !== "object") {
    throw new SessionCustodyError(operation, reply.status, "the answer has no data");
  }
  return data as Record<string, unknown>;
}

function text(data: Record<string, unknown>, key: string): string {
  const v = data[key];
  return typeof v === "string" ? v : "";
}

type CallAuth = { kind: "none" } | { kind: "own" } | { kind: "token"; token: string };

export function createOpenBaoCustodyConnector(
  opts: OpenBaoSessionCustodyOptions,
): SessionCustodyConnector {
  if (!opts.endpoint && !opts.address) {
    throw new Error("session custody: an address or an endpoint resolver is required");
  }
  const mount = opts.mount ?? DEFAULT_MOUNT;
  const baseFetch = opts.fetch ?? fetch;
  let connected: Promise<Connected> | null = null;
  let login: { token: string; renewAt: number } | null = null;

  async function connect(): Promise<Connected> {
    const endpoint = opts.endpoint
      ? await opts.endpoint()
      : { address: opts.address!, caCert: opts.caCert };
    const wantsClient = !opts.fetch &&
      (endpoint.caCert || (endpoint.clientCert && endpoint.clientKey));
    const client = wantsClient
      ? Deno.createHttpClient({
        ...(endpoint.caCert ? { caCerts: [endpoint.caCert] } : {}),
        ...(endpoint.clientCert && endpoint.clientKey
          ? { cert: endpoint.clientCert, key: endpoint.clientKey }
          : {}),
      })
      : undefined;
    return { endpoint: { ...endpoint, address: endpoint.address.replace(/\/+$/, "") }, client };
  }

  function connection(): Promise<Connected> {
    connected ??= connect().catch((err) => {
      connected = null;
      throw err;
    });
    return connected;
  }

  function forget(c: Connected): void {
    c.client?.close();
    connected = null;
    login = null;
  }

  async function request(
    c: Connected,
    method: string,
    path: string,
    headers: Headers,
    body?: unknown,
  ): Promise<BaoReply> {
    if (body !== undefined) headers.set("content-type", "application/json");
    headers.set("accept", "application/json");
    const res = await baseFetch(`${c.endpoint.address}/v1/${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(c.client ? { client: c.client } : {}),
    } as RequestInit);
    const raw = await res.text();
    let json: Record<string, unknown> | null = null;
    try {
      json = raw ? JSON.parse(raw) as Record<string, unknown> : null;
    } catch {
      json = { errors: [raw.slice(0, 200)] };
    }
    return { status: res.status, json };
  }

  async function token(c: Connected): Promise<string> {
    const cert = c.endpoint.certLogin;
    if (!cert) {
      const t = typeof opts.token === "function" ? await opts.token() : opts.token;
      if (!t) throw new Error("session custody: no token (token or a cert login)");
      return t;
    }
    if (login && Date.now() < login.renewAt) return login.token;
    const reply = await request(c, "POST", `auth/${cert.mount}/login`, new Headers(), {
      name: cert.role,
    });
    const auth = reply.json?.auth as { client_token?: string; lease_duration?: number } | undefined;
    if (reply.status !== 200 || !auth?.client_token) {
      throw new SessionCustodyError(`cert login as ${cert.role}`, reply.status, errorsOf(reply));
    }
    const leaseMs = (auth.lease_duration ?? 0) * 1000;
    login = {
      token: auth.client_token,
      renewAt: Date.now() + Math.max(leaseMs - TOKEN_RENEW_BEFORE_MS, leaseMs / 2),
    };
    return auth.client_token;
  }

  async function call(
    method: string,
    path: string,
    body?: unknown,
    auth: CallAuth = { kind: "own" },
  ): Promise<BaoReply> {
    for (let attempt = 0;; attempt++) {
      const c = await connection();
      let reply: BaoReply;
      try {
        const headers = new Headers();
        if (auth.kind === "own") headers.set("x-vault-token", await token(c));
        else if (auth.kind === "token") headers.set("x-vault-token", auth.token);
        reply = await request(c, method, path, headers, body);
      } catch (err) {
        if (attempt > 0 || !(err instanceof TypeError) || !opts.endpoint) throw err;
        forget(c);
        continue;
      }
      if (reply.status === 403 && auth.kind === "own" && c.endpoint.certLogin && attempt === 0) {
        login = null;
        continue;
      }
      return reply;
    }
  }

  const didPath = (did: string) => `auth/${mount}/session/${encodeURIComponent(did)}`;

  function build(auth: CallAuth): SessionCustody {
    return {
      async start(input: CustodyStartInput): Promise<CustodyLoginStart> {
        const reply = await call("POST", `auth/${mount}/login/start`, {
          role: input.role,
          ...(input.handle ? { handle: input.handle } : {}),
          ...(input.did ? { did: input.did } : {}),
          ...(input.redirectUri ? { redirect_uri: input.redirectUri } : {}),
        }, { kind: "none" });
        if (reply.status !== 200) {
          throw new SessionCustodyError("start", reply.status, errorsOf(reply));
        }
        const d = dataOf(reply, "start");
        return {
          authorizationUrl: text(d, "authorization_url"),
          state: text(d, "state"),
          expiresAt: text(d, "expires_at"),
          did: text(d, "did"),
          pds: text(d, "pds"),
        };
      },

      async complete(input: CustodyCompleteInput): Promise<CustodyLogin> {
        const reply = await call("POST", `auth/${mount}/login`, {
          state: input.state,
          code: input.code,
          ...(input.iss ? { iss: input.iss } : {}),
        }, { kind: "none" });
        const granted = reply.json?.auth as
          | {
            client_token?: string;
            lease_duration?: number;
            metadata?: Record<string, string>;
            policies?: string[];
          }
          | undefined;
        if (reply.status !== 200 || !granted?.client_token) {
          throw new SessionCustodyError("complete", reply.status, errorsOf(reply));
        }
        return {
          did: granted.metadata?.did ?? "",
          handle: granted.metadata?.handle ?? "",
          pds: granted.metadata?.pds ?? "",
          token: granted.client_token,
          leaseSeconds: granted.lease_duration ?? 0,
          policies: granted.policies ?? [],
        };
      },

      async session(did: string): Promise<CustodySession | null> {
        const reply = await call("GET", didPath(did), undefined, auth);
        if (reply.status === 404) return null;
        if (reply.status !== 200) {
          throw new SessionCustodyError("session", reply.status, errorsOf(reply));
        }
        const d = dataOf(reply, "session");
        return {
          did: text(d, "did"),
          handle: text(d, "handle"),
          pds: text(d, "pds"),
          issuer: text(d, "issuer"),
          scope: text(d, "scope"),
          expiresAt: text(d, "expires_at"),
          status: text(d, "status") === "active" ? "active" : "expired",
          statusReason: text(d, "status_reason"),
        };
      },

      async proof(request: ProofRequest): Promise<SessionProof> {
        const reply = await call("POST", `${didPath(request.did)}/proof`, {
          htm: request.method,
          htu: request.url,
          ...(request.nonce ? { nonce: request.nonce } : {}),
        }, auth);
        if (reply.status !== 200) {
          throw new SessionCustodyError("proof", reply.status, errorsOf(reply));
        }
        const d = dataOf(reply, "proof");
        return {
          accessToken: text(d, "access_token"),
          dpopProof: text(d, "dpop_proof"),
          expiresAt: text(d, "expires_at"),
        };
      },

      async signOut(did: string): Promise<SignOutResult> {
        const reply = await call("DELETE", didPath(did), undefined, auth);
        if (reply.status !== 200 && reply.status !== 204) {
          throw new SessionCustodyError("signOut", reply.status, errorsOf(reply));
        }
        const d = reply.status === 200 ? dataOf(reply, "signOut") : {};
        const revokeError = text(d, "revoke_error");
        return { revoked: d.revoked === true, ...(revokeError ? { revokeError } : {}) };
      },
    };
  }

  return {
    own: build({ kind: "own" }),
    forToken(token: string): TokenCustody {
      const auth: CallAuth = { kind: "token", token };
      return {
        ...build(auth),
        async identity(): Promise<CustodyIdentity | null> {
          const reply = await call("GET", "auth/token/lookup-self", undefined, auth);
          if (reply.status === 403) return null;
          if (reply.status !== 200) {
            throw new SessionCustodyError("identity", reply.status, errorsOf(reply));
          }
          const d = dataOf(reply, "identity");
          const meta = (d.meta ?? {}) as Record<string, string>;
          return {
            did: meta.did ?? "",
            handle: meta.handle ?? "",
            pds: meta.pds ?? "",
            policies: Array.isArray(d.policies)
              ? d.policies.filter((p): p is string => typeof p === "string")
              : [],
            ttlSeconds: typeof d.ttl === "number" ? d.ttl : 0,
            renewable: d.renewable === true,
          };
        },
        async renew(incrementSeconds?: number): Promise<number> {
          const reply = await call(
            "POST",
            "auth/token/renew-self",
            incrementSeconds ? { increment: `${incrementSeconds}s` } : {},
            auth,
          );
          if (reply.status !== 200) {
            throw new SessionCustodyError("renew", reply.status, errorsOf(reply));
          }
          const granted = reply.json?.auth as { lease_duration?: number } | undefined;
          return granted?.lease_duration ?? 0;
        },
        async revokeToken(): Promise<void> {
          const reply = await call("POST", "auth/token/revoke-self", undefined, auth);
          if (reply.status !== 200 && reply.status !== 204 && reply.status !== 403) {
            throw new SessionCustodyError("revokeToken", reply.status, errorsOf(reply));
          }
        },
      };
    },
    async clientMetadata(): Promise<Record<string, unknown>> {
      const reply = await call("GET", `auth/${mount}/client-metadata`, undefined, { kind: "none" });
      if (reply.status !== 200) {
        throw new SessionCustodyError("clientMetadata", reply.status, errorsOf(reply));
      }
      return dataOf(reply, "clientMetadata");
    },
  };
}

export function createOpenBaoSessionCustody(opts: OpenBaoSessionCustodyOptions): SessionCustody {
  return createOpenBaoCustodyConnector(opts).own;
}

export interface CustodyFetchOptions {
  custody: SessionCustody;
  did: string;
  pds: string;
  fetch?: typeof fetch;
  nonces?: NonceBook;
}

export type CustodyFetchHandler = (url: string, init?: RequestInit) => Promise<Response>;

function resolveAgainst(pds: string, target: string): string {
  return new URL(target, pds.endsWith("/") ? pds : `${pds}/`).toString();
}

export function createCustodyFetchHandler(opts: CustodyFetchOptions): CustodyFetchHandler {
  const baseFetch = opts.fetch ?? fetch;
  const nonces = opts.nonces ?? createNonceBook();
  return async (target, init) => {
    const url = resolveAgainst(opts.pds, target);
    const origin = originOf(url);
    const method = (init?.method ?? "GET").toUpperCase();
    for (let attempt = 0;; attempt++) {
      const proof = await opts.custody.proof({
        did: opts.did,
        method,
        url,
        nonce: nonces.get(origin),
      });
      const headers = new Headers(init?.headers);
      headers.set("authorization", `DPoP ${proof.accessToken}`);
      headers.set("dpop", proof.dpopProof);
      const res = await baseFetch(url, { ...init, method, headers });
      const nonce = res.headers.get(DPOP_NONCE_HEADER);
      if (nonce) nonces.set(origin, nonce);
      if (attempt > 0 || !nonce || init?.body instanceof ReadableStream) return res;
      let errorCode: string | undefined;
      if (res.status === 400 || res.status === 401) {
        errorCode = await res.clone().json().then((b: { error?: string }) => b?.error).catch(() =>
          undefined
        );
      }
      if (
        !isUseDpopNonce({
          status: res.status,
          wwwAuthenticate: res.headers.get("www-authenticate"),
          errorCode,
        })
      ) return res;
      await res.body?.cancel();
    }
  };
}

export interface CustodySessionManager {
  did: string;
  fetchHandler: CustodyFetchHandler;
}

export function createCustodySessionManager(opts: CustodyFetchOptions): CustodySessionManager {
  return { did: opts.did, fetchHandler: createCustodyFetchHandler(opts) };
}

async function expectOk(res: Response, what: string): Promise<Response> {
  if (!res.ok) {
    throw new Error(
      `${what} failed: ${res.status} ${(await res.text().catch(() => "")).slice(0, 200)}`,
    );
  }
  return res;
}

function jsonInit(body: unknown): RequestInit {
  return {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  };
}

export function createCustodyAgent(opts: CustodyFetchOptions): CustodyAgent {
  const handler = createCustodyFetchHandler(opts);
  const xrpc = (nsid: string) => `/xrpc/${nsid}`;
  const writeOne = async (
    nsid: string,
    repo: string,
    collection: string,
    rkey: string,
    record: Record<string, unknown>,
  ) => {
    const res = await expectOk(
      await handler(xrpc(nsid), jsonInit({ repo, collection, rkey, record })),
      nsid,
    );
    const data = await res.json() as { uri: string; cid?: string };
    return { uri: data.uri, cid: data.cid ?? "" };
  };
  return {
    did: opts.did,
    sessionData: { userDid: opts.did },
    signer: {
      did: () => opts.did,
      sign: () =>
        Promise.reject(
          new Error("a custody agent signs nothing locally; the key stays in OpenBao"),
        ),
    },
    async applyWrites(repo, writes) {
      const res = await expectOk(
        await handler(
          xrpc("com.atproto.repo.applyWrites"),
          jsonInit({
            repo,
            writes: writes.map((w) => ({
              $type: `com.atproto.repo.applyWrites#${w.action}`,
              collection: w.collection,
              rkey: w.rkey,
              ...(w.action === "delete" ? {} : { value: w.record }),
            })),
          }),
        ),
        "applyWrites",
      );
      const data = await res.json() as { commit?: { rev?: string } };
      const rev = data.commit?.rev ?? "";
      return { repo, commit: rev, rev, since: null, blocks: new Uint8Array(), ops: [] };
    },
    async getRecord(repo, collection, rkey) {
      const q = new URLSearchParams({ repo, collection, rkey });
      const res = await handler(`${xrpc("com.atproto.repo.getRecord")}?${q}`);
      if (!res.ok) {
        await res.body?.cancel();
        return null;
      }
      const data = await res.json() as {
        uri: string;
        cid?: string;
        value: Record<string, unknown>;
      };
      return { uri: data.uri, cid: data.cid ?? "", value: data.value };
    },
    async listRecords(repo, collection, listOpts) {
      const target = listOpts?.limit ?? 100;
      const records: CustodyRecord[] = [];
      let cursor: string | undefined;
      do {
        const q = new URLSearchParams({ repo, collection, limit: String(Math.min(target, 100)) });
        if (cursor) q.set("cursor", cursor);
        const res = await handler(`${xrpc("com.atproto.repo.listRecords")}?${q}`);
        if (!res.ok) {
          await res.body?.cancel();
          break;
        }
        const data = await res.json() as {
          records: Array<{ uri: string; cid?: string; value: Record<string, unknown> }>;
          cursor?: string;
        };
        for (const r of data.records) {
          records.push({ uri: r.uri, cid: r.cid ?? "", value: r.value });
        }
        cursor = data.cursor;
      } while (cursor && records.length < target);
      return { records: records.slice(0, target) };
    },
    async getServiceAuth(aud, lxm) {
      const q = new URLSearchParams({ aud, ...(lxm ? { lxm } : {}) });
      const res = await expectOk(
        await handler(`${xrpc("com.atproto.server.getServiceAuth")}?${q}`),
        "getServiceAuth",
      );
      return (await res.json() as { token: string }).token;
    },
    createRecord: (repo, collection, rkey, record) =>
      writeOne("com.atproto.repo.createRecord", repo, collection, rkey, record),
    putRecord: (repo, collection, rkey, record) =>
      writeOne("com.atproto.repo.putRecord", repo, collection, rkey, record),
  };
}
