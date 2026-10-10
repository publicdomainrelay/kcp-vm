// @ts-types="npm:@types/ssh2@^1"
import { Server, utils, type Connection } from "ssh2";
import { Duplex } from "node:stream";
import { Buffer } from "node:buffer";
import { assertEquals } from "@std/assert";
import { runNodeSession } from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import {
  encodeNodeJoinDocument,
  K3S_JOIN_GO,
  K3S_JOIN_PATH,
  K3S_JOIN_READY_MARKER,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

const ADVERTISE = "100.127.255.254";

function serverSocketStream(socket: WebSocket): Duplex {
  const pending: Uint8Array[] = [];
  let open = socket.readyState === WebSocket.OPEN;
  socket.binaryType = "arraybuffer";
  const stream = new Duplex({
    read() {
      return;
    },
    write(chunk: Uint8Array, _enc, cb) {
      if (open) socket.send(chunk);
      else pending.push(chunk);
      cb();
    },
    final(cb) {
      socket.close();
      cb();
    },
  });
  socket.addEventListener("open", () => {
    open = true;
    for (const c of pending.splice(0)) socket.send(c);
  });
  socket.addEventListener("message", (e) => stream.push(new Uint8Array(e.data as ArrayBuffer)));
  socket.addEventListener("close", () => stream.push(null));
  return stream;
}

interface FakeGuest {
  fqdn: string;
  privateKeyPath: string;
  forward: Promise<{ client: Connection; bindAddr: string; bindPort: number }>;
  document: Promise<Record<string, string>>;
  go: Promise<void>;
  execCommand: Promise<string>;
  channelClosed: Promise<void>;
  close(): Promise<void>;
}

async function fakeGuest(opts: { sayReady?: boolean } = {}): Promise<FakeGuest> {
  const hostKey = utils.generateKeyPairSync("ed25519");
  const clientKey = utils.generateKeyPairSync("ed25519");
  const privateKeyPath = await Deno.makeTempFile();
  await Deno.writeTextFile(privateKeyPath, clientKey.private);

  const forward = Promise.withResolvers<{ client: Connection; bindAddr: string; bindPort: number }>();
  const document = Promise.withResolvers<Record<string, string>>();
  const go = Promise.withResolvers<void>();
  const execCommand = Promise.withResolvers<string>();
  const channelClosed = Promise.withResolvers<void>();

  const ssh = new Server({ hostKeys: [hostKey.private] }, (client) => {
    client.on("authentication", (ctx) => ctx.method === "publickey" ? ctx.accept() : ctx.reject(["publickey"]));
    client.on("ready", () => {
      client.on("request", (accept, reject, name, info) => {
        if (name !== "tcpip-forward") return reject?.();
        accept?.();
        const i = info as { bindAddr: string; bindPort: number };
        forward.resolve({ client, bindAddr: i.bindAddr, bindPort: i.bindPort });
      });
      client.on("session", (acceptSession) => {
        const session = acceptSession();
        session.on("exec", (acceptExec, _reject, info) => {
          execCommand.resolve(info.command);
          const ch = acceptExec();
          let buf = "";
          let phase: "doc" | "go" | "held" = "doc";
          ch.on("data", (chunk: Buffer) => {
            buf += chunk.toString();
            if (phase === "doc" && buf.includes("\n\n")) {
              const [docText, rest] = buf.split("\n\n");
              buf = rest ?? "";
              document.resolve(Object.fromEntries(docText.split("\n").map((l) => {
                const [k, v] = l.split(" ");
                return [k, atob(v)];
              })));
              phase = "go";
              if (opts.sayReady !== false) ch.write(`${K3S_JOIN_READY_MARKER}\n`);
            }
            if (phase === "go" && buf.includes("\n")) {
              if (buf.split("\n")[0] === K3S_JOIN_GO) {
                phase = "held";
                ch.write("level=info msg=\"Starting k3s agent\"\n");
                go.resolve();
              }
            }
          });
          ch.on("close", () => channelClosed.resolve());
        });
      });
    });
  });

  const server = Deno.serve({ port: 0, hostname: "127.0.0.1", onListen: () => {} }, (req) => {
    const { socket, response } = Deno.upgradeWebSocket(req);
    ssh.injectSocket(serverSocketStream(socket) as never);
    return response;
  });

  return {
    fqdn: `127.0.0.1:${server.addr.port}`,
    privateKeyPath,
    forward: forward.promise,
    document: document.promise,
    go: go.promise,
    execCommand: execCommand.promise,
    channelClosed: channelClosed.promise,
    async close() {
      ssh.close();
      await server.shutdown();
      await Deno.remove(privateKeyPath).catch(() => undefined);
    },
  };
}

function fakeApiserver() {
  const listener = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  const served = (async () => {
    for await (const conn of listener) {
      (async () => {
        const buf = new Uint8Array(1024);
        const n = await conn.read(buf);
        if (n) await conn.write(new TextEncoder().encode(`API:${new TextDecoder().decode(buf.subarray(0, n))}`));
        conn.close();
      })();
    }
  })().catch(() => undefined);
  return { port: (listener.addr as Deno.NetAddr).port, close: () => listener.close(), served };
}

Deno.test({
  name: "a node session hands over its join document, opens the reverse forward, and holds until released",
  sanitizeOps: false,
  sanitizeResources: false,
  fn: async () => {
    const guest = await fakeGuest();
    const api = fakeApiserver();
    const abort = new AbortController();
    const events: string[] = [];
    try {
      const outcome = runNodeSession({
        privateKeyPath: guest.privateKeyPath,
        fqdn: guest.fqdn,
        program: K3S_JOIN_PATH,
        joinDocument: encodeNodeJoinDocument({
          nodeName: "ci-node1",
          serverUrl: `https://${ADVERTISE}:${api.port}`,
          advertiseAddress: ADVERTISE,
          token: "K10ab::server:k10t",
          nodePassword: "k10p",
        }),
        forwardAddress: ADVERTISE,
        apiserverHost: "127.0.0.1",
        apiserverPort: api.port,
        signal: abort.signal,
        onJoining: () => events.push("joining"),
        onForwarded: () => events.push("forwarded"),
        log: () => {},
      });

      assertEquals(await guest.execCommand, K3S_JOIN_PATH);
      assertEquals((await guest.document).TOKEN, "K10ab::server:k10t");
      const fwd = await guest.forward;
      assertEquals([fwd.bindAddr, fwd.bindPort], [ADVERTISE, api.port]);
      await guest.go;

      const reply = await new Promise<string>((resolve, reject) => {
        fwd.client.forwardOut(fwd.bindAddr, fwd.bindPort, "10.42.0.7", 40000, (err, ch) => {
          if (err) return reject(err);
          let got = "";
          ch.on("data", (d: Buffer) => got += d.toString());
          ch.on("end", () => {
            ch.end();
            resolve(got);
          });
          ch.write("ping");
        });
      });
      assertEquals(reply, "API:ping");
      assertEquals(events, ["joining", "forwarded"]);

      abort.abort();
      const result = await outcome;
      assertEquals(result.code, 0);
      assertEquals(result.reason, "released");
      await guest.channelClosed;
    } finally {
      api.close();
      await guest.close();
    }
  },
});

Deno.test({
  name: "a guest that never reports ready gets no forward and no go-ahead",
  sanitizeOps: false,
  sanitizeResources: false,
  fn: async () => {
    const guest = await fakeGuest({ sayReady: false });
    let forwardRequested = false;
    guest.forward.then(() => forwardRequested = true);
    try {
      const result = await runNodeSession({
        privateKeyPath: guest.privateKeyPath,
        fqdn: guest.fqdn,
        program: K3S_JOIN_PATH,
        joinDocument: encodeNodeJoinDocument({
          nodeName: "n",
          serverUrl: "https://x:1",
          advertiseAddress: ADVERTISE,
          token: "t",
          nodePassword: "p",
        }),
        forwardAddress: ADVERTISE,
        apiserverHost: "127.0.0.1",
        apiserverPort: 1,
        signal: new AbortController().signal,
        readyTimeoutMs: 500,
        log: () => {},
      });
      assertEquals(result.code !== 0, true);
      assertEquals(result.reason, "the guest never reported it was ready to join");
      assertEquals(forwardRequested, false);
    } finally {
      await guest.close();
    }
  },
});
