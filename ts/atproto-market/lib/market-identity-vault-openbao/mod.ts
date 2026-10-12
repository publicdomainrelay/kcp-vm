import type { IdentityVault, VaultRef } from "@publicdomainrelay/market-identity-abc";

export interface OpenBaoEndpoint {
  address: string;
  caCert?: string;
  clientCert?: string;
  clientKey?: string;
  certLogin?: { mount: string; role: string };
}

export interface OpenBaoIdentityVaultOptions {
  address?: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  namespace?: string;
  endpoint?: () => Promise<OpenBaoEndpoint>;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 15_000;
const TOKEN_RENEW_BEFORE_MS = 60_000;
const SEGMENT = /^[A-Za-z0-9_.@-]+$/;

function checkedRef(ref: VaultRef): VaultRef {
  if (!ref.mount || !SEGMENT.test(ref.mount) || ref.mount === "." || ref.mount === "..") {
    throw new Error(`openbao vault: invalid mount ${JSON.stringify(ref.mount)}`);
  }
  const parts = ref.path.split("/");
  if (!ref.path || parts.some((p) => !SEGMENT.test(p) || p === "." || p === "..")) {
    throw new Error(`openbao vault: invalid path ${JSON.stringify(ref.path)}`);
  }
  return ref;
}

interface Connected {
  endpoint: OpenBaoEndpoint;
  client?: Deno.HttpClient;
}

export function createOpenBaoIdentityVault(opts: OpenBaoIdentityVaultOptions): IdentityVault {
  if (!opts.endpoint && !opts.address) throw new Error("openbao vault: an address or an endpoint resolver is required");
  const baseFetch = opts.fetch ?? fetch;
  let connected: Promise<Connected> | null = null;
  let login: { token: string; renewAt: number } | null = null;

  async function connect(): Promise<Connected> {
    const endpoint = opts.endpoint ? await opts.endpoint() : { address: opts.address!, caCert: opts.caCert };
    const wantsClient = !opts.fetch && (endpoint.caCert || (endpoint.clientCert && endpoint.clientKey));
    const client = wantsClient
      ? Deno.createHttpClient({
        ...(endpoint.caCert ? { caCerts: [endpoint.caCert] } : {}),
        ...(endpoint.clientCert && endpoint.clientKey ? { cert: endpoint.clientCert, key: endpoint.clientKey } : {}),
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

  function request(c: Connected, method: string, path: string, headers: Headers, body?: unknown): Promise<Response> {
    if (opts.namespace) headers.set("x-vault-namespace", opts.namespace);
    if (body !== undefined) headers.set("content-type", "application/json");
    headers.set("accept", "application/json");
    return baseFetch(`${c.endpoint.address}/v1/${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(c.client ? { client: c.client } : {}),
    } as RequestInit);
  }

  async function token(c: Connected): Promise<string> {
    const cert = c.endpoint.certLogin;
    if (!cert) {
      const t = opts.tokenFile ? (await Deno.readTextFile(opts.tokenFile)).trim() : opts.token;
      if (!t) throw new Error("openbao vault: no token (token, tokenFile or a cert login)");
      return t;
    }
    if (login && Date.now() < login.renewAt) return login.token;
    const res = await request(c, "POST", `auth/${cert.mount}/login`, new Headers(), { name: cert.role });
    if (!res.ok) return await fail(res, `cert login as ${cert.role}`);
    const out = await res.json() as { auth?: { client_token?: string; lease_duration?: number } };
    const t = out.auth?.client_token;
    if (!t) throw new Error(`openbao vault: cert login as ${cert.role} returned no token`);
    const leaseMs = (out.auth?.lease_duration ?? 0) * 1000;
    login = { token: t, renewAt: Date.now() + Math.max(leaseMs - TOKEN_RENEW_BEFORE_MS, leaseMs / 2) };
    return t;
  }

  async function call(method: string, path: string, body?: unknown): Promise<Response> {
    for (let attempt = 0;; attempt++) {
      const c = await connection();
      let res: Response;
      try {
        res = await request(c, method, path, new Headers({ "x-vault-token": await token(c) }), body);
      } catch (err) {
        if (attempt > 0 || !(err instanceof TypeError) || !opts.endpoint) throw err;
        forget(c);
        continue;
      }
      if (res.status === 403 && c.endpoint.certLogin && attempt === 0) {
        await res.body?.cancel();
        login = null;
        continue;
      }
      return res;
    }
  }

  async function fail(res: Response, what: string): Promise<never> {
    const text = await res.text().catch(() => "");
    throw new Error(`openbao vault: ${what}: ${res.status} ${text.slice(0, 200)}`);
  }

  async function read(ref: VaultRef): Promise<Record<string, unknown> | null> {
    const { mount, path } = checkedRef(ref);
    const res = await call("GET", `${mount}/data/${path}`);
    if (res.status === 404) {
      await res.body?.cancel();
      return null;
    }
    if (!res.ok) return await fail(res, `read ${mount}/${path}`);
    const out = await res.json() as { data?: { data?: Record<string, unknown> | null } };
    return out.data?.data ?? null;
  }

  return {
    read,
    async write(ref, data) {
      const { mount, path } = checkedRef(ref);
      const res = await call("POST", `${mount}/data/${path}`, { data });
      if (!res.ok) return await fail(res, `write ${mount}/${path}`);
      await res.body?.cancel();
    },
    async remove(ref) {
      const { mount, path } = checkedRef(ref);
      const res = await call("DELETE", `${mount}/metadata/${path}`);
      if (!res.ok && res.status !== 404) return await fail(res, `remove ${mount}/${path}`);
      await res.body?.cancel();
    },
    async list(ref) {
      const { mount, path } = checkedRef(ref);
      const res = await call("LIST", `${mount}/metadata/${path}`);
      if (res.status === 404) {
        await res.body?.cancel();
        return [];
      }
      if (!res.ok) return await fail(res, `list ${mount}/${path}`);
      const out = await res.json() as { data?: { keys?: unknown } };
      return Array.isArray(out.data?.keys) ? out.data.keys.filter((k): k is string => typeof k === "string") : [];
    },
    async create(ref, data) {
      const { mount, path } = checkedRef(ref);
      const res = await call("POST", `${mount}/data/${path}`, { options: { cas: 0 }, data });
      if (res.ok) {
        await res.body?.cancel();
        return structuredClone(data);
      }
      if (res.status !== 400) return await fail(res, `create ${mount}/${path}`);
      await res.body?.cancel();
      const held = await read(ref);
      if (!held) throw new Error(`openbao vault: create ${mount}/${path} lost a check-and-set race and reads back nothing`);
      return held;
    },
  };
}
