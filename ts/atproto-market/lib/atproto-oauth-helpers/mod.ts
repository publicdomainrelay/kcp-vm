// Shared OAuth helpers for @atproto/oauth-client — runtime, state store, session store.
// Used by both bidder (createOAuthAgent) and requester (createOAuthRequester).

import { createWebCryptoKey } from "./key.ts";
import type { RuntimeImplementation, StateStore, SessionStore } from "@atproto/oauth-client";

/** Web Crypto runtime for @atproto/oauth-client. Works in Deno and browsers. */
export function webCryptoRuntime(): RuntimeImplementation {
  return {
    createKey: createWebCryptoKey,
    getRandomValues(length: number): Uint8Array {
      return crypto.getRandomValues(new Uint8Array(length));
    },
    async digest(data: Uint8Array, alg: { name: string }): Promise<Uint8Array> {
      const name = alg.name === "sha256" ? "SHA-256" : alg.name === "sha384" ? "SHA-384" : alg.name === "sha512" ? "SHA-512" : alg.name;
      return new Uint8Array(await crypto.subtle.digest(name, data as BufferSource));
    },
  };
}

/** In-memory StateStore for short-lived OAuth state (only needed during auth flow). */
export function memoryStateStore(): StateStore {
  const map = new Map<string, unknown>();
  return {
    async get(key: string) { return map.get(key) as never; },
    async set(key: string, value: unknown) { map.set(key, value); },
    async del(key: string) { map.delete(key); },
  };
}

/** JSON-file-backed SessionStore for OAuth session persistence across runs. */
export function jsonSessionStore(filePath: string): SessionStore {
  let cache: Record<string, unknown> | null = null;
  async function load(): Promise<Record<string, unknown>> {
    if (cache) return cache;
    try {
      const raw = await Deno.readTextFile(filePath);
      cache = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      cache = {};
    }
    return cache!;
  }
  async function save(): Promise<void> {
    await Deno.writeTextFile(filePath, JSON.stringify(cache, null, 2));
  }
  return {
    async get(key: string) { return (await load())[key] as never; },
    async set(key: string, value: unknown) { (await load())[key] = value; await save(); },
    async del(key: string) { delete (await load())[key]; await save(); },
  };
}

/** Build OAuth client metadata for the well-known endpoint. */
export function oauthClientMetadata(opts: {
  clientId?: string;
  redirectUri?: string;
  scope: string;
  clientName?: string;
}): Record<string, unknown> {
  return {
    client_id: opts.clientId ?? "http://localhost",
    application_type: "web",
    dpop_bound_access_tokens: true,
    redirect_uris: [opts.redirectUri ?? "http://127.0.0.1:0/callback"],
    grant_types: ["authorization_code", "refresh_token"],
    response_types: ["code"],
    scope: opts.scope,
    token_endpoint_auth_method: "none",
    client_name: opts.clientName ?? "ATProto OAuth Client",
  };
}

/** Start a loopback OAuth callback server, returns params from the redirect. */
export function startLoopbackCallbackServer(port: number): {
  promise: Promise<Record<string, string>>;
  shutdown(): void;
} {
  const { promise, resolve } = Promise.withResolvers<Record<string, string>>();
  const server = Deno.serve({ hostname: "127.0.0.1", port }, (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/callback") {
      const params: Record<string, string> = {};
      url.searchParams.forEach((v, k) => { params[k] = v; });
      resolve(params);
      return new Response("<h1>Authorized! You can close this tab.</h1>", {
        headers: { "content-type": "text/html" },
      });
    }
    return new Response("Not found", { status: 404 });
  });
  return {
    promise,
    shutdown: () => server.shutdown(),
  };
}
