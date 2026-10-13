import type { GuestLogin, GuestLoginReply, GuestLoginStep } from "@publicdomainrelay/guest-login-abc";

export interface OpenBaoGuestLoginEndpoint {
  address: string;
  caCert?: string;
  clientCert?: string;
  clientKey?: string;
}

export interface OpenBaoGuestLoginOptions {
  endpoint: () => Promise<OpenBaoGuestLoginEndpoint>;
  mount: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 10_000;
const MOUNT = /^[a-z0-9][a-z0-9-]{0,62}$/;

export function createOpenBaoGuestLogin(opts: OpenBaoGuestLoginOptions): GuestLogin {
  if (!MOUNT.test(opts.mount)) throw new Error(`guest login: ${JSON.stringify(opts.mount)} is not an auth mount name`);
  const baseFetch = opts.fetch ?? fetch;
  let held: { key: string; client?: Deno.HttpClient } | null = null;

  function clientFor(e: OpenBaoGuestLoginEndpoint): Deno.HttpClient | undefined {
    if (opts.fetch) return undefined;
    const key = [e.caCert, e.clientCert, e.clientKey].join("\n");
    if (held?.key === key) return held.client;
    held?.client?.close();
    const client = e.caCert || (e.clientCert && e.clientKey)
      ? Deno.createHttpClient({
        ...(e.caCert ? { caCerts: [e.caCert] } : {}),
        ...(e.clientCert && e.clientKey ? { cert: e.clientCert, key: e.clientKey } : {}),
      })
      : undefined;
    held = { key, client };
    return client;
  }

  return {
    async step(step: GuestLoginStep, body: Record<string, string>): Promise<GuestLoginReply> {
      const e = await opts.endpoint();
      const client = clientFor(e);
      const res = await baseFetch(`${e.address.replace(/\/+$/, "")}/v1/auth/${opts.mount}/${step}`, {
        method: "POST",
        headers: { "content-type": "application/json", accept: "application/json" },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
        ...(client ? { client } : {}),
      } as RequestInit);
      const text = await res.text();
      let parsed: unknown = null;
      try {
        parsed = text ? JSON.parse(text) : null;
      } catch {
        parsed = { errors: [text.slice(0, 300)] };
      }
      return { status: res.status, body: parsed };
    },
  };
}
