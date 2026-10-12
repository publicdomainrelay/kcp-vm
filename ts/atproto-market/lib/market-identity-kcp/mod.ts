import type { MarketIdentity, MarketIdentityObjects, MarketIdentityStatus } from "@publicdomainrelay/market-identity-abc";
import { findSecretMaterial, MARKET_IDENTITY_RESOURCE } from "@publicdomainrelay/market-identity-abc";
import { assertMarketIdentityStatus, parseMarketIdentity } from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface KcpMarketIdentityOptions {
  server: string;
  workspace: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

export class KcpMarketIdentityError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "KcpMarketIdentityError";
  }
}

const DEFAULT_TIMEOUT_MS = 15_000;

export function createKcpMarketIdentityObjects(opts: KcpMarketIdentityOptions): MarketIdentityObjects {
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && opts.caCert ? Deno.createHttpClient({ caCerts: [opts.caCert] }) : undefined;
  const { group, version, plural } = MARKET_IDENTITY_RESOURCE;
  const base = `${opts.server.replace(/\/+$/, "")}/clusters/${opts.workspace}/apis/${group}/${version}/${plural}`;

  async function token(): Promise<string | undefined> {
    if (opts.tokenFile) return (await Deno.readTextFile(opts.tokenFile)).trim();
    return opts.token;
  }

  async function request(url: string, init: RequestInit = {}): Promise<unknown> {
    const headers = new Headers(init.headers);
    headers.set("accept", "application/json");
    const bearer = await token();
    if (bearer) headers.set("authorization", `Bearer ${bearer}`);
    const res = await baseFetch(url, {
      ...init,
      headers,
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(client ? { client } : {}),
    } as RequestInit);
    const text = await res.text();
    if (!res.ok) {
      throw new KcpMarketIdentityError(res.status, `${init.method ?? "GET"} ${new URL(url).pathname}: ${res.status} ${text.slice(0, 300)}`);
    }
    return text ? JSON.parse(text) : null;
  }

  return {
    async get(name: string): Promise<MarketIdentity | null> {
      try {
        return parseMarketIdentity(await request(`${base}/${encodeURIComponent(name)}`));
      } catch (err) {
        if (err instanceof KcpMarketIdentityError && err.status === 404) return null;
        throw err;
      }
    },
    async writeStatus(name: string, status: MarketIdentityStatus): Promise<void> {
      const secrets = findSecretMaterial(status);
      if (secrets.length) {
        throw new Error(`marketidentity ${name}: refusing a status that carries secret material at ${secrets.join(", ")}; keys, tokens and codes belong in the vault`);
      }
      assertMarketIdentityStatus(status);
      await request(`${base}/${encodeURIComponent(name)}/status`, {
        method: "PATCH",
        headers: { "content-type": "application/json-patch+json" },
        body: JSON.stringify([{ op: "add", path: "/status", value: status }]),
      });
    },
  };
}
