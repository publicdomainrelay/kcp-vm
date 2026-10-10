import { assertEquals, assertRejects } from "@std/assert";
import { createKcpNodeClaimStore, parseKubeconfig } from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import { NODECLAIM_FINALIZER, type NodeClaim } from "@publicdomainrelay/socialweb-computer-k8s-common";

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
    return new Response(JSON.stringify({ items: [raw("7", [NODECLAIM_FINALIZER])] }));
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
    finalizers: [NODECLAIM_FINALIZER],
    spec: SPEC,
    status: {},
  }]);
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
    [{ op: "test", path: "/metadata/resourceVersion", value: "1" }, { op: "add", path: "/metadata/finalizers", value: ["other", NODECLAIM_FINALIZER] }],
    [{ op: "test", path: "/metadata/resourceVersion", value: "2" }, { op: "add", path: "/metadata/finalizers", value: ["other", NODECLAIM_FINALIZER] }],
  ]);
});

Deno.test("removing a finalizer keeps everyone else's and is a no-op once gone", async () => {
  const kcp = fakeKcp((s) => s.method === "GET" ? new Response(JSON.stringify(raw("4", ["other", NODECLAIM_FINALIZER]))) : new Response("{}"));
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
