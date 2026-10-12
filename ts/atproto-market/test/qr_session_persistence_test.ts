import { assert, assertEquals } from "@std/assert";
import { saveOAuthQRSession, tryRestoreOAuthQRSession } from "@publicdomainrelay/atproto-helpers";
import type { OAuthSessionData } from "@publicdomainrelay/atproto-helpers";

function b64url(s: string): string {
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function unsignedJwt(exp: number): string {
  return `${b64url(JSON.stringify({ alg: "ES256", typ: "JWT" }))}.${
    b64url(JSON.stringify({ sub: "did:plc:qrsession", iat: Math.floor(Date.now() / 1000), exp }))
  }.signature`;
}

async function withMockPds(
  run: (origin: string) => Promise<void>,
): Promise<void> {
  let origin = "";
  const { promise: listening, resolve: onListen } = Promise.withResolvers<number>();
  const server = Deno.serve({ port: 0, hostname: "127.0.0.1", onListen: (addr) => onListen(addr.port) }, (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/.well-known/oauth-protected-resource") {
      return Response.json({ authorization_servers: [origin] });
    }
    if (url.pathname === "/.well-known/oauth-authorization-server") {
      return Response.json({ token_endpoint: `${origin}/token` });
    }
    if (url.pathname.endsWith(".listRecords")) {
      return Response.json({ records: [] });
    }
    return new Response("not found", { status: 404 });
  });
  origin = `http://127.0.0.1:${await listening}`;
  try {
    await run(origin);
  } finally {
    await server.shutdown();
  }
}

async function makeSession(origin: string, handle: string): Promise<OAuthSessionData> {
  const dpop = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  const dpopPrivateJwk = await crypto.subtle.exportKey("jwk", dpop.privateKey) as unknown as Record<string, string>;
  const dpopPublicJwk = await crypto.subtle.exportKey("jwk", dpop.publicKey) as unknown as Record<string, string>;
  return {
    accessJwt: unsignedJwt(Math.floor(Date.now() / 1000) + 3600),
    refreshJwt: "the-refresh-token",
    userDid: "did:plc:qrsession",
    handle,
    pds: origin,
    dpopPublicJwk,
    dpopPrivateJwk,
  };
}

Deno.test("a session saved to --oauth-session-file is the one a restart restores from", async () => {
  await withMockPds(async (origin) => {
    const tmpDir = await Deno.makeTempDir();
    const sessionPath = `${tmpDir}/pinned-session.json`;
    const handle = "bobvmbuilder.bsky.social";

    await saveOAuthQRSession(await makeSession(origin, handle), { sessionPath, label: "bidder", handle });

    const written = await Deno.stat(sessionPath).then(() => true, () => false);
    console.log("[qrsession] pinned path written =", written);

    const agent = await tryRestoreOAuthQRSession({ sessionPath, label: "bidder", handle });
    console.log("[qrsession] restored from pinned path =", agent !== null);
    agent?.dispose();

    assert(written);
    assert(agent !== null);
  });
});

Deno.test("with no explicit path, a session round-trips under the same label and handle", async () => {
  await withMockPds(async (origin) => {
    const tmpHome = await Deno.makeTempDir();
    const realHome = Deno.env.get("HOME");
    Deno.env.set("HOME", tmpHome);
    try {
      const handle = "johnandersen777.bsky.social";
      await saveOAuthQRSession(await makeSession(origin, "bobvmbuilder.bsky.social"), { label: "bidder", handle });

      const agent = await tryRestoreOAuthQRSession({ label: "bidder", handle });
      console.log("[qrsession] restored under default path =", agent !== null);
      agent?.dispose();

      assert(agent !== null);

      const other = await tryRestoreOAuthQRSession({ label: "bidder", handle: "someone-else.bsky.social" });
      console.log("[qrsession] a different handle finds nothing =", other === null);
      other?.dispose();
      assertEquals(other, null);
    } finally {
      if (realHome === undefined) Deno.env.delete("HOME");
      else Deno.env.set("HOME", realHome);
    }
  });
});

Deno.test("with no handle configured, a pinned session is restored without knowing the account", async () => {
  await withMockPds(async (origin) => {
    const tmpDir = await Deno.makeTempDir();
    const sessionPath = `${tmpDir}/pinned-no-handle.json`;
    const handle = "bobvmbuilder.bsky.social";

    await saveOAuthQRSession(await makeSession(origin, handle), { sessionPath, label: "bidder", handle });

    const agent = await tryRestoreOAuthQRSession({ sessionPath, label: "bidder" });
    console.log("[qrsession] restored with no handle =", agent !== null, "carried handle =", agent?.sessionData.handle);
    agent?.dispose();

    assert(agent !== null);
    assertEquals(agent?.sessionData.handle, handle);
  });
});
