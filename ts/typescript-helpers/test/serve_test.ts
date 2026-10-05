// createServe is the one place every service in this org binds a socket, so
// these two properties have to hold there rather than in each entry point:
// serving TLS from PEM files, and writing the port that was actually bound to
// a file. Both are what let a caller ask for port 0 (an ephemeral port) and
// still find the service, over https.
import { assertEquals } from "@std/assert";
import { createServe } from "@publicdomainrelay/serve";
import { generateLocalhostTlsCert } from "@publicdomainrelay/tls-localhost";

Deno.test("createServe writes the bound port to portFile when asked for port 0", async () => {
  const dir = await Deno.makeTempDir();
  const portFile = `${dir}/port-bound-to`;

  const serve = createServe({
    tcp: { addr: "127.0.0.1", port: 0 },
    portFile,
  });
  serve.app.get("/health", (c) => c.json({ status: "ok" }));
  await serve.beginServe();

  try {
    const written = (await Deno.readTextFile(portFile)).trim();
    assertEquals(Number(written), serve.tcpPort);
    assertEquals(serve.tcpPort > 0, true);

    const res = await fetch(`http://127.0.0.1:${written}/health`);
    assertEquals(res.status, 200);
  } finally {
    serve.shutdown();
  }
});

Deno.test("createServe serves TLS when given certificate and key files", async () => {
  const dir = await Deno.makeTempDir();
  const certFile = `${dir}/server.crt`;
  const keyFile = `${dir}/server.key`;
  const portFile = `${dir}/port-bound-to`;

  const { caCertPem, serverCertPem, serverKeyPem } = await generateLocalhostTlsCert();
  await Deno.writeTextFile(certFile, serverCertPem);
  await Deno.writeTextFile(keyFile, serverKeyPem);

  const serve = createServe({
    tcp: { addr: "127.0.0.1", port: 0, certFile, keyFile },
    portFile,
  });
  serve.app.get("/health", (c) => c.json({ status: "ok" }));
  await serve.beginServe();

  const client = Deno.createHttpClient({ caCerts: [caCertPem] });
  try {
    const port = (await Deno.readTextFile(portFile)).trim();
    // The cert names *.localhost, so the handshake only validates by name there.
    const res = await fetch(`https://localhost:${port}/health`, {
      client,
    } as RequestInit);
    assertEquals(res.status, 200);
    assertEquals(await res.json(), { status: "ok" });
  } finally {
    client.close();
    serve.shutdown();
  }
});

Deno.test("createServe stays plain HTTP when only one of certFile/keyFile is given", async () => {
  const dir = await Deno.makeTempDir();
  const certFile = `${dir}/server.crt`;
  const portFile = `${dir}/port-bound-to`;

  const { serverCertPem } = await generateLocalhostTlsCert();
  await Deno.writeTextFile(certFile, serverCertPem);

  const serve = createServe({
    tcp: { addr: "127.0.0.1", port: 0, certFile },
    portFile,
  });
  serve.app.get("/health", (c) => c.json({ status: "ok" }));
  await serve.beginServe();

  try {
    const port = (await Deno.readTextFile(portFile)).trim();
    const res = await fetch(`http://127.0.0.1:${port}/health`);
    assertEquals(res.status, 200);
  } finally {
    serve.shutdown();
  }
});
