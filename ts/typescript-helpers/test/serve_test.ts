// createServe is the one place every service in this org binds a socket, so
// these two properties have to hold there rather than in each entry point:
// serving TLS from PEM files, and writing the port that was actually bound to
// a file. Both are what let a caller ask for port 0 (an ephemeral port) and
// still find the service, over https.
import { assertEquals, assertRejects } from "@std/assert";
import { createServe, type IngressRef } from "@publicdomainrelay/serve";
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

type ReportedListener = { name: string; kind: string; address: string };

async function readListeners(path: string): Promise<ReportedListener[]> {
  const body = JSON.parse(await Deno.readTextFile(path));
  return body.listeners;
}

function fakeRelay(ingressUrl: string, name?: string): IngressRef & { served: number } {
  const relay = {
    name,
    ingressRef: "",
    ingressUrl,
    ingressHost: new URL(ingressUrl).host,
    served: 0,
    onServe(_fetch: (req: Request) => Promise<Response>): Promise<void> {
      relay.served++;
      return Promise.resolve();
    },
    close(): void {},
  };
  return relay;
}

Deno.test("createServe reports two tcp listeners and one unix listener with their bound addresses", async () => {
  const dir = await Deno.makeTempDir();
  const listenersFile = `${dir}/listeners.json`;
  const socketPath = `${dir}/control.sock`;

  const serve = createServe({
    tcp: [{ name: "http", addr: "127.0.0.1", port: 0 }, { name: "admin", addr: "127.0.0.1", port: 0 }],
    unix: { name: "control", socketPath },
    listenersFile,
  });
  serve.app.get("/health", (c) => c.text("ok"));
  await serve.beginServe();

  const client = Deno.createHttpClient({ proxy: { transport: "unix", path: socketPath } } as never);
  try {
    const reported = await readListeners(listenersFile);
    assertEquals(reported.map((l) => [l.name, l.kind]), [["http", "tcp"], ["admin", "tcp"], ["control", "unix"]]);
    assertEquals(reported[2].address, socketPath);
    assertEquals(serve.listeners, reported);
    const [httpPort, adminPort] = reported.slice(0, 2).map((l) => Number(l.address.split(":")[1]));
    assertEquals(httpPort > 0 && adminPort > 0 && httpPort !== adminPort, true);
    assertEquals(serve.tcpPort, httpPort);
    for (const l of reported.slice(0, 2)) {
      assertEquals(await (await fetch(`http://${l.address}/health`)).text(), "ok");
    }
    assertEquals(await (await fetch("http://localhost/health", { client } as RequestInit)).text(), "ok");
  } finally {
    client.close();
    serve.shutdown();
  }
});

Deno.test("createServe reports a port-0 tcp listener and a relay ingress URL", async () => {
  const dir = await Deno.makeTempDir();
  const listenersFile = `${dir}/listeners.json`;
  const relay = fakeRelay("https://abc123.relay.example");

  const serve = createServe({ tcp: { addr: "127.0.0.1", port: 0 }, relays: [relay], listenersFile });
  await serve.beginServe();

  try {
    assertEquals(relay.served, 1);
    assertEquals(await readListeners(listenersFile), [
      { name: "tcp", kind: "tcp", address: `127.0.0.1:${serve.tcpPort}` },
      { name: "relay", kind: "relay", address: "https://abc123.relay.example" },
    ]);
  } finally {
    serve.shutdown();
  }
});

Deno.test("createServe reports a relay added after serve began", async () => {
  const dir = await Deno.makeTempDir();
  const listenersFile = `${dir}/listeners.json`;

  const serve = createServe({ tcp: { addr: "127.0.0.1", port: 0 }, listenersFile });
  await serve.beginServe();
  try {
    await serve.addRelay(fakeRelay("https://late.relay.example", "late"));
    assertEquals((await readListeners(listenersFile)).map((l) => l.name), ["tcp", "late"]);
  } finally {
    serve.shutdown();
  }
});

Deno.test("two createServe handles sharing a listeners file keep both and never repeat a name", async () => {
  const dir = await Deno.makeTempDir();
  const listenersFile = `${dir}/listeners.json`;

  const first = createServe({ tcp: { addr: "127.0.0.1", port: 0 }, listenersFile });
  const second = createServe({ tcp: { addr: "127.0.0.1", port: 0 }, listenersFile });
  await first.beginServe();
  await second.beginServe();
  try {
    assertEquals(await readListeners(listenersFile), [
      { name: "tcp", kind: "tcp", address: `127.0.0.1:${first.tcpPort}` },
      { name: "tcp-1", kind: "tcp", address: `127.0.0.1:${second.tcpPort}` },
    ]);
    second.shutdown();
    await first.listenersWritten();
    assertEquals((await readListeners(listenersFile)).map((l) => l.name), ["tcp"]);
  } finally {
    first.shutdown();
  }
});

Deno.test("createServe refuses a listener name that is not a dns label", async () => {
  const serve = createServe({ tcp: { name: "Not_A_Label", addr: "127.0.0.1", port: 0 } });
  await assertRejects(() => serve.beginServe(), Error, "Not_A_Label");
});
