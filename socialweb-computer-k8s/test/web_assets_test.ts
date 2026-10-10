import { assertEquals } from "@std/assert";

async function read(relative: string): Promise<string> {
  return await Deno.readTextFile(new URL(relative, new URL("../web/", import.meta.url)));
}

Deno.test("the web app ships the files the server serves", async () => {
  const html = await read("index.html");
  assertEquals(html.includes("/components/swc-app.js"), true);
  assertEquals(html.includes("/styles.css"), true);
  assertEquals(html.includes("<swc-app>"), true);

  for (
    const asset of [
      "styles.css",
      "components/swc-app.js",
      "components/swc-cluster-list.js",
      "lib/atproto-oauth.js",
      "lib/k8s-api.js",
      "lib/pds.js",
      "generated/oauth-scope.js",
    ]
  ) {
    const stat = await Deno.stat(new URL(asset, new URL("../web/", import.meta.url)));
    assertEquals(stat.isFile, true, `${asset} must exist`);
  }
});

Deno.test("the web app takes its scope from the generated module and it carries the cluster RPCs", async () => {
  const app = await read("components/swc-app.js");
  assertEquals(app.includes("../generated/oauth-scope.js"), true);
  assertEquals(/atproto repo:/.test(app), false, "the scope must not be written into the component");

  const generated = await read("generated/oauth-scope.js");
  assertEquals(generated.includes("DO NOT EDIT"), true);
  for (const nsid of ["createCluster", "listClusters", "getKubeconfig", "controlPlaneUp", "controlPlaneDown", "deleteCluster"]) {
    assertEquals(generated.includes(`rpc:com.publicdomainrelay.temp.cluster.${nsid}?aud=*`), true, nsid);
  }
  assertEquals(generated.includes("repo:com.publicdomainrelay.temp.market.rfp?action=create"), true, "nodes are requested as the user");
});

Deno.test("the only server call that carries the session is the deposit", async () => {
  const pds = await read("lib/pds.js");
  assertEquals([...pds.matchAll(/fetch\(\s*['"]\//g)].length, 1);
  assertEquals(pds.includes("depositSession"), true);

  const api = await read("lib/k8s-api.js");
  assertEquals(api.includes("refreshJwt"), false, "API calls must not ship the refresh token");
  assertEquals(api.includes("accessJwt"), false, "API calls must not ship the access token");
  assertEquals(api.includes("SWC-Proof"), true);
  assertEquals(api.includes("swc-proof+jwt"), true);
});

Deno.test("the deposited session names the client_id it was issued to", async () => {
  const oauth = await read("lib/atproto-oauth.js");
  const payload = oauth.slice(oauth.indexOf("session: {"), oauth.indexOf("returnTo: pending.returnTo"));
  assertEquals(payload.includes("clientId: pending.clientId"), true);

  const inproc = await Deno.readTextFile(
    new URL("../lib/socialweb-computer-requester-inproc/mod.ts", import.meta.url),
  );
  assertEquals(inproc.includes("stored.clientId ?? opts.oauthClientId"), true);
});
