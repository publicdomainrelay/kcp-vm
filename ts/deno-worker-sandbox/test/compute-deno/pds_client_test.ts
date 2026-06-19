import { assertEquals } from "@std/assert";
import { createRemotePdsClient } from "@publicdomainrelay/compute-deno-atproto";

Deno.test("createRemotePdsClient logs in and resolves DID", async () => {
  const controller = new AbortController();
  const server = Deno.serve({ port: 0, hostname: "127.0.0.1", signal: controller.signal }, (req) => {
    const url = new URL(req.url);

    if (url.pathname === "/xrpc/com.atproto.server.createSession" && req.method === "POST") {
      return new Response(JSON.stringify({
        accessJwt: "fake.jwt.token",
        refreshJwt: "fake.refresh.token",
        did: "did:plc:test123",
        handle: "test.bsky.social",
      }), { headers: { "content-type": "application/json" } });
    }

    if (url.pathname === "/xrpc/com.atproto.repo.createRecord" && req.method === "POST") {
      return new Response(JSON.stringify({
        uri: "at://did:plc:test123/com.example.test/r1",
        cid: "bafyreiabcdef",
      }), { headers: { "content-type": "application/json" } });
    }

    if (url.pathname === "/xrpc/com.atproto.repo.getRecord" && req.method === "GET") {
      return new Response(JSON.stringify({
        uri: "at://did:plc:test123/com.example.test/r1",
        cid: "bafyreiabcdef",
        value: { $type: "com.example.test", message: "hello" },
      }), { headers: { "content-type": "application/json" } });
    }

    return new Response(JSON.stringify({ error: "MethodNotFound" }), {
      status: 404,
      headers: { "content-type": "application/json" },
    });
  });

  const addr = server.addr as { port: number };
  const pdsUrl = `http://127.0.0.1:${addr.port}`;

  try {
    const { client, did } = await createRemotePdsClient(pdsUrl, "test.bsky.social", "password");
    assertEquals(did, "did:plc:test123");

    const created = await client.createRecord(
      "did:plc:test123",
      "com.example.test",
      { $type: "com.example.test", message: "hello" },
    );
    assertEquals(created.uri, "at://did:plc:test123/com.example.test/r1");
    assertEquals(created.cid, "bafyreiabcdef");

    const fetched = await client.getRecord("did:plc:test123", "com.example.test", "r1");
    assertEquals(fetched, {
      uri: "at://did:plc:test123/com.example.test/r1",
      cid: "bafyreiabcdef",
      value: { $type: "com.example.test", message: "hello" },
    });
  } finally {
    controller.abort();
    await server.shutdown();
  }
});

Deno.test("createRemotePdsClient getRecord returns null for 404", async () => {
  const controller = new AbortController();
  const server = Deno.serve({ port: 0, hostname: "127.0.0.1", signal: controller.signal }, (req) => {
    const url = new URL(req.url);

    if (url.pathname === "/xrpc/com.atproto.server.createSession" && req.method === "POST") {
      return new Response(JSON.stringify({
        accessJwt: "fake.jwt.token",
        refreshJwt: "fake.refresh.token",
        did: "did:plc:test123",
        handle: "test.bsky.social",
      }), { headers: { "content-type": "application/json" } });
    }

    return new Response(JSON.stringify({ error: "RecordNotFound" }), {
      status: 404,
      headers: { "content-type": "application/json" },
    });
  });

  const addr = server.addr as { port: number };
  const pdsUrl = `http://127.0.0.1:${addr.port}`;

  try {
    const { client } = await createRemotePdsClient(pdsUrl, "test.bsky.social", "password");
    const fetched = await client.getRecord("did:plc:test123", "com.example.test", "rMISSING");
    assertEquals(fetched, null);
  } finally {
    controller.abort();
    await server.shutdown();
  }
});
