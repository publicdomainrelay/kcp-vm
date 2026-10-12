import { assert, assertEquals, assertRejects } from "@std/assert";
import { createOpenBaoClaimKeyStore } from "@publicdomainrelay/socialweb-computer-k8s-claim-keys-openbao";

const KEY_A = "a".repeat(64);
const KEY_B = "b".repeat(64);

function fakeKvV2(token = "door-token") {
  const kv = new Map<string, { version: number; data: Record<string, unknown> }>();
  const seen: string[] = [];
  const fetch = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const method = init?.method ?? "GET";
    seen.push(`${method} ${url.pathname}`);
    if (new Headers(init?.headers).get("X-Vault-Token") !== token) {
      return new Response(JSON.stringify({ errors: ["permission denied"] }), { status: 403 });
    }
    const m = url.pathname.match(/^\/v1\/secret\/(data|metadata)\/(.+)$/);
    if (!m) return new Response("{}", { status: 404 });
    const [, kind, path] = m;
    const entry = kv.get(path);
    if (kind === "data" && method === "GET") {
      if (!entry) return new Response(JSON.stringify({ errors: [] }), { status: 404 });
      return Response.json({ data: { data: entry.data, metadata: { version: entry.version } } });
    }
    if (kind === "data" && method === "POST") {
      const body = JSON.parse(String(init?.body ?? "{}"));
      const cas = body.options?.cas;
      if (cas !== undefined && cas !== (entry?.version ?? 0)) {
        return Response.json({ errors: ["check-and-set parameter did not match the current version"] }, { status: 400 });
      }
      kv.set(path, { version: (entry?.version ?? 0) + 1, data: body.data });
      return Response.json({ data: { version: (entry?.version ?? 0) + 1 } });
    }
    if (kind === "metadata" && method === "DELETE") {
      kv.delete(path);
      return new Response(null, { status: 204 });
    }
    return new Response("{}", { status: 405 });
  };
  return { kv, seen, fetch: fetch as typeof globalThis.fetch };
}

Deno.test("the OpenBao claim key store writes a claim's key once at the door's KV path, check-and-set 0", async () => {
  const bao = fakeKvV2();
  const store = createOpenBaoClaimKeyStore({ address: "https://bao.test:8200/", token: () => Promise.resolve("door-token"), fetch: bao.fetch });
  assertEquals(store.durable, true);
  assertEquals(await store.read("c-node1"), null);
  const first = await store.ensure("c-node1", KEY_A);
  assertEquals(first, { privateKeyHex: KEY_A, keyRef: "socialweb-door/claims/c-node1/secrets-ingress" });
  assert(bao.kv.has("socialweb-door/claims/c-node1/secrets-ingress"));
  assert(bao.seen.includes("POST /v1/secret/data/socialweb-door/claims/c-node1/secrets-ingress"));
});

Deno.test("a losing writer takes the winner's key, so two doors serve one URL", async () => {
  const bao = fakeKvV2();
  const one = createOpenBaoClaimKeyStore({ address: "https://bao.test:8200", token: () => Promise.resolve("door-token"), fetch: bao.fetch });
  const two = createOpenBaoClaimKeyStore({ address: "https://bao.test:8200", token: () => Promise.resolve("door-token"), fetch: bao.fetch });
  await one.ensure("c-node1", KEY_A);
  assertEquals((await two.ensure("c-node1", KEY_B)).privateKeyHex, KEY_A);
  assertEquals((await two.read("c-node1"))?.privateKeyHex, KEY_A);
  assertEquals(bao.kv.get("socialweb-door/claims/c-node1/secrets-ingress")?.version, 1);
});

Deno.test("a removed claim's key is gone from OpenBao and the next ensure stores a new one", async () => {
  const bao = fakeKvV2();
  const store = createOpenBaoClaimKeyStore({ address: "https://bao.test:8200", token: () => Promise.resolve("door-token"), fetch: bao.fetch });
  await store.ensure("c-node1", KEY_A);
  await store.remove("c-node1");
  assert(bao.seen.includes("DELETE /v1/secret/metadata/socialweb-door/claims/c-node1/secrets-ingress"));
  assertEquals(await store.read("c-node1"), null);
  assertEquals((await store.ensure("c-node1", KEY_B)).privateKeyHex, KEY_B);
  await store.remove("never-stored");
});

Deno.test("an OpenBao that refuses the door is an error, never an absent key", async () => {
  const bao = fakeKvV2("other-token");
  const store = createOpenBaoClaimKeyStore({ address: "https://bao.test:8200", token: () => Promise.resolve("door-token"), fetch: bao.fetch });
  await assertRejects(() => store.read("c-node1"), Error, "403");
  await assertRejects(() => store.ensure("c-node1", KEY_A), Error, "403");
});
