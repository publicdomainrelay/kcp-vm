import { assert, assertEquals } from "@std/assert";
import { tryRestoreOAuthQRSession } from "@publicdomainrelay/atproto-helpers";
import type { OAuthSessionData } from "@publicdomainrelay/atproto-helpers";

function b64url(s: string): string {
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function unsignedJwt(exp: number): string {
  return `${b64url(JSON.stringify({ alg: "ES256", typ: "JWT" }))}.${
    b64url(JSON.stringify({ sub: "did:plc:defect4", iat: Math.floor(Date.now() / 1000), exp }))
  }.signature`;
}

Deno.test("defect4: restore must not burn the refresh token when the access token is live", async () => {
  const tokenCalls: string[] = [];
  let origin = "";
  const { promise: listening, resolve: onListen } = Promise.withResolvers<number>();
  const server = Deno.serve({ port: 0, hostname: "127.0.0.1", onListen: (addr) => onListen(addr.port) }, async (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/.well-known/oauth-protected-resource") {
      return Response.json({ authorization_servers: [origin] });
    }
    if (url.pathname === "/.well-known/oauth-authorization-server") {
      return Response.json({ token_endpoint: `${origin}/token` });
    }
    if (url.pathname === "/token") {
      tokenCalls.push(await req.text().catch(() => ""));
      return Response.json({ error: "invalid_grant" }, { status: 400 });
    }
    if (url.pathname.endsWith(".listRecords")) {
      return Response.json({ records: [] });
    }
    return new Response("not found", { status: 404 });
  });
  origin = `http://127.0.0.1:${await listening}`;

  const dpop = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  const dpopPrivateJwk = await crypto.subtle.exportKey("jwk", dpop.privateKey) as unknown as Record<string, string>;
  const dpopPublicJwk = await crypto.subtle.exportKey("jwk", dpop.publicKey) as unknown as Record<string, string>;

  const tmpDir = await Deno.makeTempDir();
  const sessionPath = `${tmpDir}/oauth-qr-session-requester-alice.json`;
  const session: OAuthSessionData = {
    accessJwt: unsignedJwt(Math.floor(Date.now() / 1000) + 3600),
    refreshJwt: "the-one-usable-refresh-token",
    userDid: "did:plc:defect4",
    handle: "alice.test",
    pds: origin,
    dpopPublicJwk,
    dpopPrivateJwk,
  };
  await Deno.writeTextFile(sessionPath, JSON.stringify(session, null, 2));

  const agent = await tryRestoreOAuthQRSession({ sessionPath, handle: "alice.test" });
  console.log("[defect4] restored =", agent !== null, "tokenEndpointCalls =", tokenCalls.length);
  const fileStillThere = await Deno.stat(sessionPath).then(() => true, () => false);
  console.log("[defect4] session file preserved =", fileStillThere);
  agent?.dispose();
  await server.shutdown();

  assertEquals(tokenCalls.length, 0);
  assert(agent !== null);
  assert(fileStillThere);
});
