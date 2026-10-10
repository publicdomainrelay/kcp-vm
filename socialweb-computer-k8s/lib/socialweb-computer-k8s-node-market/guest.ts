// @ts-types="npm:@types/ssh2@^1"
import { Buffer } from "node:buffer";
import { Duplex } from "node:stream";
import net from "node:net";
import { spawn } from "node:child_process";
import { IROH_SCHEME } from "@publicdomainrelay/requester-xrpc";
import { Client, type ClientChannel } from "ssh2";
import { K3S_JOIN_GO, K3S_JOIN_READY_MARKER } from "@publicdomainrelay/socialweb-computer-k8s-common";

function ignore(): void {
  return;
}

export type Log = (event: string, data?: Record<string, unknown>) => void;

/**
 * The guest's transport is an iroh endpoint: the guest runs a dumbpipe listener
 * in front of its own sshd, mints a ticket for it, and reports that ticket to
 * the market. The door reaches it by running `dumbpipe connect <ticket>`, whose
 * stdio IS the connection -- so one bidi stream, no relay in the middle, and no
 * package the guest has to fetch from the door's registry before it can be
 * reached at all.
 */
function dumbpipeStream(ticket: string, dumbpipePath: string, log?: Log): Duplex {
  // The guest announces its endpoint as `iroh://<ticket>` on the same on-network
  // report the websocket transports announce a hostname on, so the scheme is
  // stripped here rather than by every caller.
  const bare = ticket.startsWith(IROH_SCHEME) ? ticket.slice(IROH_SCHEME.length) : ticket;
  const child = spawn(dumbpipePath, ["connect", bare], { stdio: ["pipe", "pipe", "pipe"] });
  const stream = new Duplex({
    read() { ignore(); },
    write(chunk: Uint8Array, _encoding, cb) {
      try {
        child.stdin.write(chunk);
        cb();
      } catch (err) {
        cb(err as Error);
      }
    },
    final(cb) {
      try {
        child.stdin.end();
        child.kill();
      } catch { ignore(); }
      cb();
    },
  });
  child.stdout.on("data", (chunk: Uint8Array) => stream.push(chunk));
  child.stdout.on("end", () => stream.push(null));
  child.on("error", (err: Error) => stream.destroy(err));
  child.on("exit", (code: number | null) => {
    if (code !== 0 && code !== null) log?.("node_guest_dumbpipe_exit", { code });
    stream.push(null);
  });
  child.stderr.on("data", (chunk: Uint8Array) =>
    log?.("node_guest_dumbpipe_stderr", { line: redactTransportLine(new TextDecoder().decode(chunk).trim().slice(0, 200)) }));
  return stream;
}

const ENDPOINT_KEY_ANNOUNCEMENT = /\bsecret key\b/i;

const THIRTY_TWO_BYTES_OF_HEX = /\b[0-9a-f]{64}\b/gi;

export function redactTransportLine(line: string): string {
  if (ENDPOINT_KEY_ANNOUNCEMENT.test(line)) {
    return "[redacted: the transport announced its own endpoint key]";
  }
  return line.replace(THIRTY_TWO_BYTES_OF_HEX, "[redacted]");
}

/** Opens one SSH client to a guest's transport address, however that transport is spoken. */
export type GuestConnector = (
  ticket: string,
  privateKey: string,
  timeoutMs: number,
  log?: Log,
) => Promise<Client>;

/** The connector this door uses in production: one dumbpipe per session. */
export function dumbpipeConnector(dumbpipePath: string): GuestConnector {
  return (ticket, privateKey, timeoutMs, log) => connectGuest(ticket, privateKey, timeoutMs, dumbpipePath, log);
}

export async function connectGuest(
  ticket: string,
  privateKey: string,
  timeoutMs: number,
  dumbpipePath = "dumbpipe",
  log?: Log,
): Promise<Client> {
  const sock = dumbpipeStream(ticket, dumbpipePath, log);
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
  ticket: string,
  timeoutMs: number,
  signal: AbortSignal,
  log: Log,
  connect: GuestConnector,
): Promise<boolean> {
  const privateKey = await Deno.readTextFile(privateKeyPath).catch(() => "");
  if (!privateKey) return false;
  const deadline = Date.now() + timeoutMs;
  let attempt = 0;
  while (Date.now() < deadline && !signal.aborted) {
    attempt += 1;
    let client: Client | null = null;
    try {
      client = await connect(ticket, privateKey, 10_000, log);
      const ok = await new Promise<boolean>((resolve) => {
        client!.exec("true", (err, stream) => {
          if (err) return resolve(false);
          stream.on("close", (code: number | undefined) => resolve(code === 0));
          stream.resume();
        });
      });
      if (ok) {
        log("node_guest_ssh_ready", { ticket: ticket.slice(0, 24), attempt });
        return true;
      }
    } catch (err) {
      log("node_guest_ssh_poll", { ticket: ticket.slice(0, 24), attempt, error: String(err) });
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
  ticket: string;
  connect: GuestConnector;
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
    client = await plan.connect(plan.ticket, privateKey, 30_000, plan.log);
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
    plan.log("node_forward_open", { ticket: plan.ticket.slice(0, 24), address: plan.forwardAddress, port: plan.apiserverPort });
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
    plan.log("node_session_failed", { ticket: plan.ticket.slice(0, 24), error: String(err) });
    return done(1, `node session failed: ${String(err)}`);
  } finally {
    for (const u of upstreams) u.destroy();
    try {
      client?.end();
    } catch { ignore(); }
  }
};
