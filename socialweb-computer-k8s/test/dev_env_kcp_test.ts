import { assert, assertEquals } from "@std/assert";
import { parseKubeconfig } from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";

const kubeconfig = Deno.env.get("KUBECONFIG");
const instance = Deno.env.get("SOCIALWEB_INSTANCE");
const kcpHost = Deno.env.get("SOCIALWEB_KCP_HOST");
const peers = (Deno.env.get("SWC_DEV_PEERS") ?? "").split(",").filter((p) => p && p !== instance);

const WORKSPACES = "/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces";

function markerOf(id: string): string {
  return `swc-${id}`.slice(0, 63).replace(/-+$/, "");
}

Deno.test({
  name: "a TS client reaches its own dev instance's kcp from dev env alone",
  ignore: !kubeconfig || !instance || !kcpHost,
  async fn() {
    const creds = parseKubeconfig(await Deno.readTextFile(kubeconfig!));
    assertEquals(creds.server, new URL(kcpHost!).origin);
    const client = Deno.createHttpClient({
      caCerts: creds.ca ? [creds.ca] : undefined,
      ...(creds.cert && creds.key ? { cert: creds.cert, key: creds.key } : {}),
    });
    const headers: Record<string, string> = { accept: "application/json" };
    if (creds.token) headers.authorization = `Bearer ${creds.token}`;
    const call = async (path: string, init: RequestInit = {}) => {
      const res = await fetch(`${creds.server}${path}`, {
        ...init,
        headers: { ...headers, ...(init.headers ?? {}) },
        client,
      } as RequestInit);
      return { status: res.status, text: await res.text() };
    };
    try {
      const ready = await call("/readyz");
      assertEquals(ready.status, 200, ready.text);
      const own = markerOf(instance!);
      const created = await call(WORKSPACES, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          apiVersion: "tenancy.kcp.io/v1alpha1",
          kind: "Workspace",
          metadata: { name: own },
        }),
      });
      assert(created.status === 201 || created.status === 409, `${created.status} ${created.text}`);
      const list = await call(WORKSPACES);
      assertEquals(list.status, 200, list.text);
      const names = (JSON.parse(list.text).items as { metadata: { name: string } }[]).map((w) => w.metadata.name);
      assert(names.includes(own), `own marker ${own} missing from ${names.join(",")}`);
      for (const peer of peers) {
        assert(!names.includes(markerOf(peer)), `${instance} sees ${peer}'s marker: ${names.join(",")}`);
      }
      console.log(JSON.stringify({ instance, server: creds.server, workspaces: names, peersAbsent: peers }));
    } finally {
      client.close();
    }
  },
});
