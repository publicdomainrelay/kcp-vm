import { assertEquals } from "@std/assert";
import { createClusterApiFactory } from "@publicdomainrelay/hono-factory-socialweb-computer-k8s";
import { createXrpcClusterGateway } from "@publicdomainrelay/socialweb-computer-k8s-gateway-xrpc";
import { ClusterGatewayError } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import type { ClusterGateway, RequestProofVerifier } from "@publicdomainrelay/socialweb-computer-k8s-abc";

const DID = "did:plc:apiowner";
const DAY = 86_400;

interface Call {
  op: string;
  did: string;
  name?: string;
  ttl?: number;
}

function stubGateway(calls: Call[], fail?: ClusterGatewayError): ClusterGateway {
  const record = (op: string, did: string, name?: string, ttl?: number) => {
    calls.push({ op, did, name, ttl });
    if (fail) throw fail;
  };
  return {
    list(did) {
      record("list", did);
      return Promise.resolve([{ name: "ci", phase: "Ready", running: true }]);
    },
    create(did, name) {
      record("create", did, name);
      return Promise.resolve({ phase: "Initializing" });
    },
    kubeconfig(did, name, ttl) {
      record("kubeconfig", did, name, ttl);
      return Promise.resolve({ kubeconfig: "apiVersion: v1\n" });
    },
    up(did, name) {
      record("up", did, name);
      return Promise.resolve({ phase: "Initializing" });
    },
    down(did, name) {
      record("down", did, name);
      return Promise.resolve({ phase: "Unavailable" });
    },
    remove(did, name) {
      record("delete", did, name);
      return Promise.resolve({ phase: "Draining" });
    },
  };
}

const allow: RequestProofVerifier = {
  verify: (r) => Promise.resolve(r.authorization === "SWC-Proof ok" ? { did: DID } : null),
};

function app(calls: Call[], fail?: ClusterGatewayError) {
  return createClusterApiFactory({
    gateway: stubGateway(calls, fail),
    proof: allow,
    maxKubeconfigTtlSec: 7 * DAY,
    kcpPublicHost: "https://kcp.example.com",
    credentialHelpers: [{ os: "linux", arch: "amd64", path: "/downloads/socialweb-credential-linux-amd64" }],
  }).createApp();
}

function post(a: ReturnType<typeof app>, path: string, body: unknown, authorization = "SWC-Proof ok") {
  return a.fetch(new Request(`http://localhost${path}`, {
    method: "POST",
    headers: { "content-type": "application/json", authorization },
    body: JSON.stringify(body),
  }));
}

Deno.test("every cluster route refuses a request without a valid proof and never reaches the gateway", async () => {
  const calls: Call[] = [];
  const a = app(calls);
  for (const route of ["list", "create", "kubeconfig", "up", "down", "delete"]) {
    const res = await post(a, `/api/clusters/${route}`, { name: "ci" }, "SWC-Proof forged");
    assertEquals(res.status, 401, route);
  }
  assertEquals(calls.length, 0);
});

Deno.test("the account is the proved one, never one named in the body", async () => {
  const calls: Call[] = [];
  const res = await post(app(calls), "/api/clusters/create", { name: "ci", did: "did:plc:someoneelse" });
  assertEquals(res.status, 200);
  assertEquals(await res.json(), { name: "ci", phase: "Initializing" });
  assertEquals(calls, [{ op: "create", did: DID, name: "ci", ttl: undefined }]);
});

Deno.test("list, up, down and delete pass through for the proved account", async () => {
  const calls: Call[] = [];
  const a = app(calls);
  assertEquals(await (await post(a, "/api/clusters/list", {})).json(), { clusters: [{ name: "ci", phase: "Ready", running: true }] });
  assertEquals((await (await post(a, "/api/clusters/up", { name: "ci" })).json()).phase, "Initializing");
  assertEquals((await (await post(a, "/api/clusters/down", { name: "ci" })).json()).phase, "Unavailable");
  assertEquals((await (await post(a, "/api/clusters/delete", { name: "ci" })).json()).phase, "Draining");
  assertEquals(calls.map((c) => c.op), ["list", "up", "down", "delete"]);
});

Deno.test("a cluster name that is not a DNS label is refused before the gateway", async () => {
  const calls: Call[] = [];
  const a = app(calls);
  for (const name of ["", "CI", "-ci", "ci-", "a".repeat(64), "c_i", 7]) {
    assertEquals((await post(a, "/api/clusters/create", { name })).status, 400, String(name));
  }
  assertEquals(calls.length, 0);
});

Deno.test("a gateway refusal keeps its status and code", async () => {
  const res = await post(app([], new ClusterGatewayError(429, "ClusterInitializing", "wait")), "/api/clusters/up", { name: "ci" });
  assertEquals(res.status, 429);
  assertEquals(await res.json(), { error: "ClusterInitializing", message: "wait" });
});

Deno.test("an oversized or non-object body is refused", async () => {
  const a = app([]);
  assertEquals((await post(a, "/api/clusters/create", { name: "ci", pad: "x".repeat(5000) })).status, 413);
  assertEquals((await post(a, "/api/clusters/create", ["ci"])).status, 400);
});

Deno.test("the XRPC gateway asks for service auth per method and sends it as a bearer", async () => {
  const seen: { url: string; method: string; auth: string | null; body: string | null }[] = [];
  const minted: string[] = [];
  const gateway = createXrpcClusterGateway({
    gatewayUrl: "http://gateway.test/",
    audience: "did:web:gateway.test",
    serviceAuth: {
      serviceAuth(did, aud, lxm) {
        minted.push(`${did}|${aud}|${lxm}`);
        return Promise.resolve(`tok-${lxm}`);
      },
    },
    fetch: async (input, init) => {
      seen.push({
        url: String(input),
        method: init?.method ?? "GET",
        auth: new Headers(init?.headers).get("authorization"),
        body: (init?.body as string | undefined) ?? null,
      });
      const nsid = String(input).split("/xrpc/")[1];
      const out = nsid.endsWith("listClusters") ? { clusters: [{ name: "ci", phase: "Ready", running: true }] } : nsid.endsWith("getKubeconfig") ? { kubeconfig: "k", instance: "1" } : { phase: "Ready" };
      return await Promise.resolve(new Response(JSON.stringify(out), { status: 200 }));
    },
  });

  assertEquals((await gateway.list(DID))[0].name, "ci");
  assertEquals((await gateway.kubeconfig(DID, "ci", DAY)).kubeconfig, "k");
  await gateway.create(DID, "ci");

  assertEquals(seen[0], { url: "http://gateway.test/xrpc/com.publicdomainrelay.temp.cluster.listClusters", method: "GET", auth: "Bearer tok-com.publicdomainrelay.temp.cluster.listClusters", body: null });
  assertEquals(seen[1].method, "POST");
  assertEquals(JSON.parse(seen[1].body!), { name: "ci", ttlSeconds: DAY });
  assertEquals(seen[2].url.endsWith("/xrpc/com.publicdomainrelay.temp.cluster.createCluster"), true);
  assertEquals(minted, [
    `${DID}|did:web:gateway.test|com.publicdomainrelay.temp.cluster.listClusters`,
    `${DID}|did:web:gateway.test|com.publicdomainrelay.temp.cluster.getKubeconfig`,
    `${DID}|did:web:gateway.test|com.publicdomainrelay.temp.cluster.createCluster`,
  ]);
});

Deno.test("the XRPC gateway carries the gateway's error code and status", async () => {
  const gateway = createXrpcClusterGateway({
    gatewayUrl: "http://gateway.test",
    audience: "did:web:gateway.test",
    serviceAuth: { serviceAuth: () => Promise.resolve("t") },
    fetch: () => Promise.resolve(new Response(JSON.stringify({ error: "ClusterNotFound", message: "no such cluster" }), { status: 404 })),
  });
  try {
    await gateway.up(DID, "nope");
    throw new Error("expected a refusal");
  } catch (err) {
    assertEquals(err instanceof ClusterGatewayError, true);
    assertEquals((err as ClusterGatewayError).status, 404);
    assertEquals((err as ClusterGatewayError).code, "ClusterNotFound");
  }
});

Deno.test("a PDS that will not mint service auth is reported as a sign-in problem", async () => {
  const gateway = createXrpcClusterGateway({
    gatewayUrl: "http://gateway.test",
    audience: "did:web:gateway.test",
    serviceAuth: { serviceAuth: () => Promise.reject(new Error("invalid_token")) },
    fetch: () => Promise.reject(new Error("must not be called")),
  });
  try {
    await gateway.list(DID);
    throw new Error("expected a refusal");
  } catch (err) {
    assertEquals((err as ClusterGatewayError).status, 401);
  }
});
