// Shared OAuth helpers for @atproto/oauth-client — runtime, state store, session store.
// Used by both bidder (createOAuthAgent) and requester (createOAuthRequester).

import type { Key, RuntimeImplementation, StateStore, SessionStore } from "@atproto/oauth-client";

/** Web Crypto runtime for @atproto/oauth-client. Works in Deno and browsers. */
export function webCryptoRuntime(): RuntimeImplementation {
  return {
    async createKey(algs: string[]): Promise<Key> {
      const alg = algs.find((a) => a === "ES256") ?? algs[0];
      const key = await crypto.subtle.generateKey(
        { name: "ECDSA", namedCurve: "P-256" },
        true, ["sign"],
      );
      const jwk = await crypto.subtle.exportKey("jwk", key.privateKey);
      return { alg, kid: await crypto.randomUUID(), privateJwk: jwk } as unknown as Key;
    },
    getRandomValues(length: number): Uint8Array {
      return crypto.getRandomValues(new Uint8Array(length));
    },
    async digest(data: Uint8Array, alg: { name: string }): Promise<Uint8Array> {
      return new Uint8Array(await crypto.subtle.digest(alg.name, data as BufferSource));
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
