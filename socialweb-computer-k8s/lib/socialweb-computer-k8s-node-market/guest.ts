// @ts-types="npm:@types/ssh2@^1"
import { Buffer } from "node:buffer";
import { Duplex } from "node:stream";
import net from "node:net";
import { Client, type ClientChannel } from "ssh2";
import { tunnelWsUrl } from "@publicdomainrelay/requester-xrpc";
import { K3S_JOIN_GO, K3S_JOIN_READY_MARKER } from "@publicdomainrelay/socialweb-computer-k8s-common";

function ignore(): void {
  return;
}

export type Log = (event: string, data?: Record<string, unknown>) => void;

async function tunnelStream(url: string): Promise<Duplex> {
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";
  await new Promise<void>((resolve, reject) => {
    ws.addEventListener("open", () => resolve(), { once: true });
    ws.addEventListener("error", () => reject(new Error(`tunnel websocket to ${url} failed`)), { once: true });
  });
  const stream = new Duplex({
    read() { ignore(); },
    write(chunk: Uint8Array, _encoding, cb) {
      try {
        ws.send(chunk);
        cb();
      } catch (err) {
        cb(err as Error);
      }
    },
    final(cb) {
      try {
        ws.close();
      } catch { ignore(); }
      cb();
    },
  });
  ws.addEventListener("message", (event) => stream.push(new Uint8Array(event.data as ArrayBuffer)));
  ws.addEventListener("close", () => stream.push(null));
  ws.addEventListener("error", () => stream.destroy(new Error("tunnel websocket failed")));
  return stream;
}

export async function connectGuest(fqdn: string, privateKey: string, timeoutMs: number): Promise<Client> {
  const sock = await tunnelStream(tunnelWsUrl(fqdn));
  const client = new Client();
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("tunnel connect timed out")), timeoutMs);
    client
      .on("ready", () => {
        clearTimeout(timer);
        resolve();
      })
      .on("error", (err: Error) => {
        clearTimeout(timer);
        reject(err);
      })
      .connect({ sock, username: "root", privateKey, hostVerifier: () => true, keepaliveInterval: 15_000 });
  });
  return client;
}

export async function pollGuestReady(
  privateKeyPath: string,
  fqdn: string,
  timeoutMs: number,
  signal: AbortSignal,
  log: Log,
): Promise<boolean> {
  const privateKey = await Deno.readTextFile(privateKeyPath).catch(() => "");
  if (!privateKey) return false;
  const deadline = Date.now() + timeoutMs;
  let attempt = 0;
  while (Date.now() < deadline && !signal.aborted) {
    attempt += 1;
    let client: Client | null = null;
    try {
      client = await connectGuest(fqdn, privateKey, 10_000);
      const ok = await new Promise<boolean>((resolve) => {
        client!.exec("true", (err, stream) => {
          if (err) return resolve(false);
          stream.on("close", (code: number | undefined) => resolve(code === 0));
          stream.resume();
        });
      });
      if (ok) {
        log("node_guest_ssh_ready", { fqdn, attempt });
        return true;
      }
    } catch (err) {
      log("node_guest_ssh_poll", { fqdn, attempt, error: String(err) });
    } finally {
      try {
        client?.end();
      } catch { ignore(); }
    }
    await new Promise((r) => setTimeout(r, 5_000));
  }
  return false;
}

export class BoundedTail {
  private chunks: Uint8Array[] = [];
  private size = 0;

  constructor(readonly limit: number) {}

  push(chunk: Uint8Array): void {
    this.chunks.push(chunk);
    this.size += chunk.byteLength;
    while (this.size > this.limit && this.chunks.length > 1) {
      this.size -= this.chunks.shift()!.byteLength;
    }
  }

  text(): string {
    const all = new Uint8Array(this.size);
    let offset = 0;
    for (const c of this.chunks) {
      all.set(c, offset);
      offset += c.byteLength;
    }
    const decoded = new TextDecoder().decode(all);
    return decoded.length > this.limit ? decoded.slice(decoded.length - this.limit) : decoded;
  }
}

export interface NodeSessionPlan {
  privateKeyPath: string;
  fqdn: string;
  program: string;
  joinDocument: string;
  forwardAddress: string;
  apiserverHost: string;
  apiserverPort: number;
  signal: AbortSignal;
  readyTimeoutMs?: number;
  tailBytes?: number;
  onJoining?: () => void;
  onForwarded?: () => void;
  log: Log;
}

export interface NodeSessionOutcome {
  code: number;
  reason: string;
  tail: string;
}

function forwardIn(client: Client, address: string, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    client.forwardIn(address, port, (err) => err ? reject(err) : resolve());
  });
}

function execNoPty(client: Client, program: string): Promise<ClientChannel> {
  return new Promise((resolve, reject) => {
    client.exec(program, (err, stream) => err ? reject(err) : resolve(stream));
  });
}

export type NodeSessionRunner = (plan: NodeSessionPlan) => Promise<NodeSessionOutcome>;

export const runNodeSession: NodeSessionRunner = async (plan) => {
  const tail = new BoundedTail(plan.tailBytes ?? 16 * 1024);
  const done = (code: number, reason: string): NodeSessionOutcome => ({ code, reason, tail: tail.text() });
  if (plan.signal.aborted) return done(0, "released before the session began");

  let client: Client | null = null;
  const upstreams = new Set<net.Socket>();
  try {
    const privateKey = await Deno.readTextFile(plan.privateKeyPath);
    client = await connectGuest(plan.fqdn, privateKey, 30_000);
    let forwarded = false;
    client.on("tcp connection", (_info, accept) => {
      const channel = accept();
      const upstream = net.connect(plan.apiserverPort, plan.apiserverHost);
      upstreams.add(upstream);
      upstream.on("close", () => upstreams.delete(upstream));
      upstream.on("error", () => channel.close());
      channel.on("error", () => upstream.destroy());
      channel.pipe(upstream).pipe(channel);
      if (!forwarded) {
        forwarded = true;
        plan.onForwarded?.();
      }
    });

    const stream = await execNoPty(client, plan.program);
    plan.onJoining?.();
    const closed = new Promise<number>((resolve) => stream.on("close", (code: number | undefined) => resolve(code ?? 0)));
    const ready = new Promise<boolean>((resolve) => {
      let seen = "";
      const timer = setTimeout(() => resolve(false), plan.readyTimeoutMs ?? 20 * 60_000);
      stream.on("data", (chunk: Uint8Array) => {
        tail.push(chunk);
        if (seen.length < 4096) {
          seen += new TextDecoder().decode(chunk);
          if (seen.includes(K3S_JOIN_READY_MARKER)) {
            clearTimeout(timer);
            resolve(true);
          }
        }
      });
      stream.stderr.on("data", (chunk: Uint8Array) => tail.push(chunk));
      closed.then(() => {
        clearTimeout(timer);
        resolve(false);
      });
      plan.signal.addEventListener("abort", () => {
        clearTimeout(timer);
        resolve(false);
      }, { once: true });
    });

    stream.write(Buffer.from(plan.joinDocument));
    if (!await ready) {
      stream.close();
      const code = await Promise.race([closed, new Promise<number>((r) => setTimeout(() => r(1), 5_000))]);
      return done(plan.signal.aborted ? 0 : (code || 1), plan.signal.aborted ? "released during join" : "the guest never reported it was ready to join");
    }

    await forwardIn(client, plan.forwardAddress, plan.apiserverPort);
    plan.log("node_forward_open", { fqdn: plan.fqdn, address: plan.forwardAddress, port: plan.apiserverPort });
    stream.write(Buffer.from(`${K3S_JOIN_GO}\n`));

    const aborted = new Promise<number>((resolve) => {
      if (plan.signal.aborted) return resolve(0);
      plan.signal.addEventListener("abort", () => resolve(0), { once: true });
    });
    const code = await Promise.race([closed, aborted]);
    if (plan.signal.aborted) {
      stream.close();
      return done(0, "released");
    }
    return done(code === 0 ? 1 : code, "the node's agent exited");
  } catch (err) {
    plan.log("node_session_failed", { fqdn: plan.fqdn, error: String(err) });
    return done(1, `node session failed: ${String(err)}`);
  } finally {
    for (const u of upstreams) u.destroy();
    try {
      client?.end();
    } catch { ignore(); }
  }
};
