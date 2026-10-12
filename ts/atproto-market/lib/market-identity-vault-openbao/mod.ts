import type { IdentityVault, VaultRef } from "@publicdomainrelay/market-identity-abc";

export interface OpenBaoIdentityVaultOptions {
  address: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  namespace?: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 15_000;
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

export function createOpenBaoIdentityVault(opts: OpenBaoIdentityVaultOptions): IdentityVault {
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && opts.caCert ? Deno.createHttpClient({ caCerts: [opts.caCert] }) : undefined;
  const address = opts.address.replace(/\/+$/, "");

  async function token(): Promise<string> {
    const t = opts.tokenFile ? (await Deno.readTextFile(opts.tokenFile)).trim() : opts.token;
    if (!t) throw new Error("openbao vault: no token (token or tokenFile)");
    return t;
  }

  async function call(method: string, path: string, body?: unknown): Promise<Response> {
    const headers = new Headers({ "x-vault-token": await token(), accept: "application/json" });
    if (opts.namespace) headers.set("x-vault-namespace", opts.namespace);
    if (body !== undefined) headers.set("content-type", "application/json");
    return await baseFetch(`${address}/v1/${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(client ? { client } : {}),
    } as RequestInit);
  }

  async function fail(res: Response, what: string): Promise<never> {
    const text = await res.text().catch(() => "");
    throw new Error(`openbao vault: ${what}: ${res.status} ${text.slice(0, 200)}`);
  }

  return {
    async read(ref) {
      const { mount, path } = checkedRef(ref);
      const res = await call("GET", `${mount}/data/${path}`);
      if (res.status === 404) {
        await res.body?.cancel();
        return null;
      }
      if (!res.ok) return await fail(res, `read ${mount}/${path}`);
      const out = await res.json() as { data?: { data?: Record<string, unknown> | null } };
      return out.data?.data ?? null;
    },
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
  };
}
