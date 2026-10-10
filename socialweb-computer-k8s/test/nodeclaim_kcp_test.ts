import { assertEquals, assertRejects } from "@std/assert";
import {
  createKcpClusterLinkReader,
  createKcpNodeClaimStore,
  parseKubeconfig,
} from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import type { NodeClaim } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { ApiValidationError, FINALIZER_NODE_CLAIM } from "@publicdomainrelay/socialweb-computer-k8s-api";

const KCP = "https://kcp.test";
const SPEC = {
  cluster: "ci",
  nodeName: "ci-node1",
  ownerDid: "did:plc:o",
  apiserverPort: 1,
  advertiseAddress: "100.127.255.254",
  tokenFile: "/t",
  nodePasswordFile: "/p",
  serverCAFile: "/c",
};

interface Seen {
  method: string;
  url: string;
  auth: string | null;
  type: string | null;
  body: unknown;
}

function fakeKcp(handler: (s: Seen) => Response): { fetch: typeof fetch; seen: Seen[] } {
  const seen: Seen[] = [];
  return {
    seen,
    fetch: async (input, init) => {
      const headers = new Headers(init?.headers);
      const s: Seen = {
        method: init?.method ?? "GET",
        url: String(input),
        auth: headers.get("authorization"),
        type: headers.get("content-type"),
        body: init?.body ? JSON.parse(init.body as string) : null,
      };
      seen.push(s);
      return await Promise.resolve(handler(s));
    },
  };
}

const raw = (rv: string, finalizers: string[] = []) => ({
  metadata: { name: "ci-node1", resourceVersion: rv, finalizers, annotations: { "kcp.io/cluster": "abc123" } },
  spec: SPEC,
});

function claim(): NodeClaim {
  return { workspace: "abc123", name: "ci-node1", finalizers: [], spec: SPEC, status: {} };
}

Deno.test("kubeconfig credentials come from the named context and the server drops its workspace path", () => {
  const text = `apiVersion: v1
clusters:
- cluster: {server: "https://127.0.0.1:6443/clusters/root"}
  name: root
contexts:
- context: {cluster: root, user: kcp-admin}
  name: root
current-context: root
kind: Config
users:
- name: kcp-admin
  user: {token: secret-token}
`;
  assertEquals(parseKubeconfig(text), {
    server: "https://127.0.0.1:6443",
    token: "secret-token",
    cert: undefined,
    key: undefined,
    ca: undefined,
  });
});

Deno.test("claims are read from every account's home workspace, one query per home", async () => {
  const kcp = fakeKcp((s) => {
    if (s.url.endsWith("/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces")) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "acct1" } }] }));
    }
    if (s.url.endsWith("/clusters/root:acct1/apis/tenancy.kcp.io/v1alpha1/workspaces")) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "ci" } }] }));
    }
    return new Response(JSON.stringify({ items: [raw("7", [FINALIZER_NODE_CLAIM])] }));
  });
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test", token: "t" }, fetch: kcp.fetch });
  const claims = await store.list();
  assertEquals(kcp.seen.map((s) => s.url), [
    `${KCP}/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces`,
    `${KCP}/clusters/root:acct1/apis/tenancy.kcp.io/v1alpha1/workspaces`,
    `${KCP}/clusters/root:acct1:ci/apis/socialweb.computer/v1alpha1/nodeclaims`,
  ]);
  assertEquals(kcp.seen[2].auth, "Bearer t");
  assertEquals(claims, [{
    workspace: "abc123",
    name: "ci-node1",
    resourceVersion: "7",
    deletionTimestamp: undefined,
    finalizers: [FINALIZER_NODE_CLAIM],
    spec: SPEC,
    status: {},
  }]);
});

Deno.test("a claim kcp serves outside the generated schema is refused, not half-read", async () => {
  const kcp = fakeKcp((s) => {
    if (s.url.endsWith("/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces")) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "acct1" } }] }));
    }
    if (s.url.endsWith("/clusters/root:acct1/apis/tenancy.kcp.io/v1alpha1/workspaces")) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "ci" } }] }));
    }
    return new Response(JSON.stringify({ items: [{ ...raw("7"), spec: { ...SPEC, apiserverPort: "1" } }] }));
  });
  const store = createKcpNodeClaimStore({ credentials: { server: KCP, token: "t" }, fetch: kcp.fetch });
  await assertRejects(() => store.list(), ApiValidationError, "spec.apiserverPort");
});

Deno.test("a status outside the generated schema never leaves the door", async () => {
  const kcp = fakeKcp(() => new Response("{}"));
  const store = createKcpNodeClaimStore({ credentials: { server: KCP }, fetch: kcp.fetch });
  const bogus = { phase: "Exploded", contract: {} } as unknown as NodeClaim["status"];
  await assertRejects(() => store.replaceStatus(claim(), bogus), ApiValidationError, "phase");
  assertEquals(kcp.seen.length, 0);
});

Deno.test("status is replaced whole on the status subresource", async () => {
  const kcp = fakeKcp(() => new Response("{}"));
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: kcp.fetch });
  await store.replaceStatus(claim(), { phase: "Running", contract: { vmUri: "at://v" } });
  assertEquals(kcp.seen[0].method, "PATCH");
  assertEquals(kcp.seen[0].url, `${KCP}/clusters/abc123/apis/socialweb.computer/v1alpha1/nodeclaims/ci-node1/status`);
  assertEquals(kcp.seen[0].type, "application/json-patch+json");
  assertEquals(kcp.seen[0].body, [{ op: "add", path: "/status", value: { phase: "Running", contract: { vmUri: "at://v" } } }]);
});

Deno.test("a finalizer edit is pinned to the version it read and retried on conflict", async () => {
  let gets = 0;
  const kcp = fakeKcp((s) => {
    if (s.method === "GET") return new Response(JSON.stringify(raw(String(++gets), ["other"])));
    return gets === 1 ? new Response("conflict", { status: 422 }) : new Response("{}");
  });
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: kcp.fetch });
  await store.addFinalizer(claim());
  const patches = kcp.seen.filter((s) => s.method === "PATCH").map((s) => s.body);
  assertEquals(patches, [
    [{ op: "test", path: "/metadata/resourceVersion", value: "1" }, { op: "add", path: "/metadata/finalizers", value: ["other", FINALIZER_NODE_CLAIM] }],
    [{ op: "test", path: "/metadata/resourceVersion", value: "2" }, { op: "add", path: "/metadata/finalizers", value: ["other", FINALIZER_NODE_CLAIM] }],
  ]);
});

Deno.test("removing a finalizer keeps everyone else's and is a no-op once gone", async () => {
  const kcp = fakeKcp((s) => s.method === "GET" ? new Response(JSON.stringify(raw("4", ["other", FINALIZER_NODE_CLAIM]))) : new Response("{}"));
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: kcp.fetch });
  await store.removeFinalizer(claim());
  assertEquals(kcp.seen[1].body, [
    { op: "test", path: "/metadata/resourceVersion", value: "4" },
    { op: "add", path: "/metadata/finalizers", value: ["other"] },
  ]);

  const gone = fakeKcp(() => new Response("not found", { status: 404 }));
  await createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: gone.fetch })
    .removeFinalizer(claim());
  assertEquals(gone.seen.length, 1);
});

Deno.test("an unreadable list is an error, never an empty list", async () => {
  const kcp = fakeKcp(() => new Response("forbidden", { status: 403 }));
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: kcp.fetch });
  await assertRejects(() => store.list());
});

const WORKSPACE_WALK = "/apis/tenancy.kcp.io/v1alpha1/workspaces";

function homesKcp(): { fetch: typeof fetch; seen: Seen[] } {
  return fakeKcp((s) => {
    if (s.url.endsWith(`/clusters/root${WORKSPACE_WALK}`)) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "acct1" } }] }));
    }
    if (s.url.endsWith(`/clusters/root:acct1${WORKSPACE_WALK}`)) {
      return new Response(JSON.stringify({ items: [{ metadata: { name: "ci" } }] }));
    }
    return new Response(JSON.stringify({ items: [] }));
  });
}

const walkCalls = (seen: Seen[]) => seen.filter((s) => s.url.includes(WORKSPACE_WALK)).length;

Deno.test("the account and home walk happens once per ttl, not once per poll", async () => {
  const kcp = homesKcp();
  const store = createKcpNodeClaimStore({
    credentials: { server: "https://kcp.test" },
    fetch: kcp.fetch,
    homesTtlMs: 60_000,
  });
  await store.list();
  await store.list();
  await store.list();
  assertEquals(
    walkCalls(kcp.seen),
    2,
    "three polls inside the ttl must walk root and each account once between them. The walk is " +
      "two requests per account on every tick, the door polls on a timer, and kcp rate limits the " +
      "credential the door shares with the clusterprovider: the walk spent that budget and the " +
      "429 it earned is what the door was logging",
  );
  assertEquals(kcp.seen.filter((s) => s.url.endsWith("/nodeclaims")).length, 3);
});

Deno.test("a walk past its ttl happens again, so a new account is not invisible", async () => {
  const kcp = homesKcp();
  const store = createKcpNodeClaimStore({
    credentials: { server: "https://kcp.test" },
    fetch: kcp.fetch,
    homesTtlMs: 30,
  });
  await store.list();
  await new Promise((r) => setTimeout(r, 50));
  await store.list();
  assertEquals(walkCalls(kcp.seen), 4);
});

Deno.test("one claim is read back by name, a gone claim is null, and a rate-limited read is an error", async () => {
  const contract = {
    version: 1,
    phase: "receipt" as const,
    vmName: "ci-node1",
    requesterDid: "did:plc:o",
    marketDid: "did:plc:o",
    receipt: { uri: "at://r", cid: "rc" },
    submitEventRef: "https://b",
    timestamps: {},
  };
  const kcp = fakeKcp(() => new Response(JSON.stringify({ ...raw("7"), status: { phase: "Running", contract } })));
  const store = createKcpNodeClaimStore({ credentials: { server: KCP }, fetch: kcp.fetch });
  const read = await store.get(claim());
  assertEquals(kcp.seen[0].method, "GET");
  assertEquals(kcp.seen[0].url, `${KCP}/clusters/abc123/apis/socialweb.computer/v1alpha1/nodeclaims/ci-node1`);
  assertEquals(read?.status.contract, contract);
  assertEquals(read?.resourceVersion, "7");

  const gone = fakeKcp(() => new Response("not found", { status: 404 }));
  assertEquals(await createKcpNodeClaimStore({ credentials: { server: KCP }, fetch: gone.fetch }).get(claim()), null);

  const limited = fakeKcp(() => new Response("slow down", { status: 429, headers: { "retry-after": "30" } }));
  const held = createKcpNodeClaimStore({ credentials: { server: KCP }, fetch: limited.fetch });
  await assertRejects(() => held.get(claim()), Error, "429");
  await assertRejects(() => held.get(claim()), Error, "held back");
  assertEquals(limited.seen.length, 1);
});

Deno.test("a 429 holds reads back for the retry-after it named and writes still go through", async () => {
  const kcp = fakeKcp((s) =>
    s.method === "GET"
      ? new Response("too many requests", { status: 429, headers: { "retry-after": "30" } })
      : new Response("{}")
  );
  const store = createKcpNodeClaimStore({ credentials: { server: "https://kcp.test" }, fetch: kcp.fetch });
  await assertRejects(() => store.list(), Error, "429");
  const afterFirst = kcp.seen.length;
  await assertRejects(() => store.list(), Error, "held back");
  assertEquals(
    kcp.seen.length,
    afterFirst,
    "a second read inside the window must not reach kcp at all: retrying a rate limit on the next " +
      "tick is what turns one 429 into every tick being a 429, and the budget is shared with the " +
      "clusterprovider",
  );
  await store.replaceStatus(claim(), { phase: "Running", contract: {} });
  assertEquals(
    kcp.seen[kcp.seen.length - 1].method,
    "PATCH",
    "a claim's own status write is not the traffic that earned the rate limit, and holding it " +
      "back delays the node the door is bringing up",
  );
});

Deno.test("a cluster link is read from the claim's workspace by name, and a missing one reads as null", async () => {
  const kcp = fakeKcp((s) => {
    if (s.url.endsWith("/clusterlinks/k-openbao")) {
      return new Response(JSON.stringify({
        metadata: { name: "k-openbao" },
        spec: { cluster: "ci", service: "openbao", targetPort: 8200 },
        status: { phase: "Listening", ticket: "endpointticket", did: "did:key:z6Mk" },
      }));
    }
    return new Response("not found", { status: 404 });
  });
  const links = createKcpClusterLinkReader({ credentials: { server: KCP, token: "t" }, fetch: kcp.fetch });
  const link = await links.get("abc123", "k-openbao");
  assertEquals(link?.status.ticket, "endpointticket");
  assertEquals(link?.spec?.service, "openbao");
  assertEquals(kcp.seen[0].url, `${KCP}/clusters/abc123/apis/socialweb.computer/v1alpha1/clusterlinks/k-openbao`);
  assertEquals(kcp.seen[0].auth, "Bearer t");
  assertEquals(await links.get("abc123", "k-apiserver"), null);
});

Deno.test("a cluster link that does not match the generated schema is refused", async () => {
  const kcp = fakeKcp(() => new Response(JSON.stringify({ metadata: { name: "k-openbao" }, spec: { cluster: "ci", service: "ssh", targetPort: 1 } })));
  const links = createKcpClusterLinkReader({ credentials: { server: KCP }, fetch: kcp.fetch });
  await assertRejects(() => links.get("abc123", "k-openbao"), ApiValidationError);
});
