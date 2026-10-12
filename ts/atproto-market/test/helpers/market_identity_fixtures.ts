import { Hono } from "@hono/hono";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signerFromKeypair, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { createPlcDirectoryFactory } from "@publicdomainrelay/hono-factory-did-plc-directory";
import { createGenesisOp, createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { ASSOCIATE_CONFIRM_NSID } from "@publicdomainrelay/market-lexicons";
import type { SessionInjector } from "@publicdomainrelay/atproto-oauth-server-abc";
import type { OAuthSessionData } from "@publicdomainrelay/atproto-helpers";
import type { MarketIdentity, MarketIdentityPendingAction } from "@publicdomainrelay/market-identity-abc";
import { parseOAuthQrUrl } from "@publicdomainrelay/market-identity-abc";

export const OAUTH_QR_NSID = "com.fedfork.atprotoOauthQR";

export interface Served {
  url: string;
  close(): Promise<void>;
}

export function serveOnPort0(fetchFn: (r: Request) => Response | Promise<Response>): Promise<Served> {
  const { promise, resolve } = Promise.withResolvers<Served>();
  const ac = new AbortController();
  const server = Deno.serve({
    port: 0,
    hostname: "127.0.0.1",
    signal: ac.signal,
    onListen: (addr) => {
      resolve({
        url: `http://127.0.0.1:${(addr as Deno.NetAddr).port}`,
        close: async () => {
          ac.abort();
          await server.finished;
        },
      });
    },
  }, fetchFn);
  return promise;
}

export async function startPlcDirectory(): Promise<Served> {
  return serveOnPort0(createPlcDirectoryFactory().app.fetch);
}

export interface FakeKcp {
  fetch: typeof fetch;
  server: string;
  workspace: string;
  writes: Array<{ method: string; path: string; body: string }>;
  put(obj: MarketIdentity): void;
  read(name: string): MarketIdentity | undefined;
  patchSpec(name: string, edit: (spec: MarketIdentity["spec"] & object) => void): void;
  bumpResourceVersion(name: string): void;
  serve(): Promise<Served>;
}

export function createFakeKcp(workspace = "root:bidder"): FakeKcp {
  const objects = new Map<string, MarketIdentity>();
  const writes: FakeKcp["writes"] = [];
  let version = 1;
  const base = `/clusters/${workspace}/apis/socialweb.computer/v1alpha1/marketidentities`;
  const notFound = () => Response.json({ kind: "Status", reason: "NotFound", code: 404 }, { status: 404 });
  async function handle(req: Request): Promise<Response> {
    const path = decodeURIComponent(new URL(req.url).pathname);
    const body = req.method === "GET" ? "" : await req.text();
    if (req.method !== "GET") writes.push({ method: req.method, path, body });
    const m = path.startsWith(`${base}/`) ? /^([^/]+)(\/status)?$/.exec(path.slice(base.length + 1)) : null;
    const obj = m ? objects.get(m[1]) : undefined;
    if (!m || !obj) return notFound();
    if (req.method === "GET" && !m[2]) return Response.json(obj);
    if (req.method === "PATCH" && m[2]) {
      if (req.headers.get("content-type") !== "application/json-patch+json") {
        return Response.json({ kind: "Status", reason: "UnsupportedMediaType", code: 415 }, { status: 415 });
      }
      const ops = JSON.parse(body) as Array<{ op: string; path: string; value: unknown }>;
      for (const op of ops) {
        if (op.op !== "add" || op.path !== "/status") return Response.json({ kind: "Status", reason: "Invalid", code: 422 }, { status: 422 });
        obj.status = structuredClone(op.value) as MarketIdentity["status"];
      }
      obj.metadata = { ...obj.metadata, resourceVersion: String(++version) };
      return Response.json(obj);
    }
    if (req.method === "PATCH" && !m[2]) {
      if (req.headers.get("content-type") !== "application/merge-patch+json") {
        return Response.json({ kind: "Status", reason: "UnsupportedMediaType", code: 415 }, { status: 415 });
      }
      const patch = JSON.parse(body) as { metadata?: { resourceVersion?: string }; spec?: Record<string, unknown>; status?: unknown };
      if (patch.status !== undefined) return Response.json({ kind: "Status", reason: "Invalid", code: 422 }, { status: 422 });
      const want = patch.metadata?.resourceVersion;
      if (want && want !== obj.metadata?.resourceVersion) {
        return Response.json({ kind: "Status", reason: "Conflict", code: 409 }, { status: 409 });
      }
      const spec = { ...obj.spec } as Record<string, unknown>;
      for (const [k, v] of Object.entries(patch.spec ?? {})) {
        if (v === null) delete spec[k];
        else spec[k] = v;
      }
      obj.spec = spec as MarketIdentity["spec"];
      obj.metadata = { ...obj.metadata, generation: Number(obj.metadata?.generation ?? 1) + 1, resourceVersion: String(++version) };
      return Response.json(obj);
    }
    return Response.json({ kind: "Status", reason: "MethodNotAllowed", code: 405 }, { status: 405 });
  }
  const server = "https://kcp.test";
  return {
    fetch: ((input: string | URL | Request, init?: RequestInit) => handle(new Request(input, init))) as typeof fetch,
    server,
    workspace,
    writes,
    put(obj) {
      objects.set(obj.metadata!.name!, structuredClone({ ...obj, metadata: { ...obj.metadata, generation: 1, resourceVersion: String(++version) } }));
    },
    read(name) {
      const obj = objects.get(name);
      return obj ? structuredClone(obj) : undefined;
    },
    patchSpec(name, edit) {
      const obj = objects.get(name);
      if (!obj?.spec) throw new Error(`fake kcp has no marketidentity ${name}`);
      edit(obj.spec);
      obj.metadata = { ...obj.metadata, generation: Number(obj.metadata?.generation ?? 1) + 1, resourceVersion: String(++version) };
      writes.push({ method: "PATCH", path: `${base}/${name}`, body: JSON.stringify({ spec: obj.spec }) });
    },
    bumpResourceVersion(name) {
      const obj = objects.get(name);
      if (obj) obj.metadata = { ...obj.metadata, resourceVersion: String(++version) };
    },
    serve() {
      return serveOnPort0(handle);
    },
  };
}

export interface FakeQrRelay extends Served {
  deposits: number;
}

function jwtIssuer(token: string): string | null {
  try {
    const b64 = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    return (JSON.parse(atob(b64)) as { iss?: string }).iss ?? null;
  } catch {
    return null;
  }
}

export async function startFakeQrRelay(): Promise<FakeQrRelay> {
  const entries = new Map<string, { nonce: string; session: OAuthSessionData | null }>();
  const app = new Hono();
  let deposits = 0;
  app.post(`/xrpc/${OAUTH_QR_NSID}`, async (c) => {
    const body = await c.req.json() as Record<string, unknown> & { cliDid?: string; nonce?: string };
    if (!body.cliDid?.startsWith("did:") || !body.nonce) return c.json({ error: "InvalidRequest" }, 400);
    const { cliDid, nonce, ...session } = body;
    entries.set(cliDid, { nonce, session: session.accessJwt ? session as unknown as OAuthSessionData : null });
    deposits++;
    return c.json({ ok: true });
  });
  app.get(`/xrpc/${OAUTH_QR_NSID}`, (c) => {
    const token = /^Bearer (.+)$/.exec(c.req.header("authorization") ?? "")?.[1];
    const iss = token ? jwtIssuer(token) : null;
    if (!iss) return c.json({ error: "Unauthorized" }, 401);
    const entry = entries.get(iss);
    if (!entry?.session) return c.json({ error: "notReady" }, 404);
    entries.delete(iss);
    return c.json(entry.session);
  });
  const served = await serveOnPort0(app.fetch);
  return {
    ...served,
    get deposits() {
      return deposits;
    },
  };
}

export interface TestPds extends Served {
  sessionInjector: SessionInjector;
  createAccount(handle: string, password?: string): Promise<{ did: string; handle: string }>;
}

export async function startOAuthPds(): Promise<TestPds> {
  const kp = await Secp256k1Keypair.create({ exportable: true });
  const pds = createRepoFactory({
    storage: new MemoryStorage(),
    signer: signerFromKeypair(kp),
    oauthServer: { enabled: true, issuer: "http://127.0.0.1:0" },
  });
  const served = await serveOnPort0(pds.app.fetch);
  return {
    ...served,
    sessionInjector: pds.sessionInjector!,
    async createAccount(handle, password = "test-password") {
      const res = await fetch(`${served.url}/xrpc/com.atproto.server.createAccount`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ handle, email: `${handle}@example.test`, password }),
      });
      if (!res.ok) throw new Error(`createAccount ${handle}: ${res.status} ${await res.text()}`);
      const out = await res.json() as { did: string };
      return { did: out.did, handle };
    },
  };
}

export async function completeOAuthQrWithHonoPds(opts: {
  action: MarketIdentityPendingAction | undefined;
  pds: TestPds;
  account: { did: string; handle: string };
}): Promise<OAuthSessionData> {
  const url = opts.action?.url;
  if (!url) throw new Error(`pending action carries no url: ${JSON.stringify(opts.action)}`);
  const parsed = parseOAuthQrUrl(url);
  if (!parsed) throw new Error(`not an oauth QR url: ${url}`);
  const { sessionData } = await opts.pds.sessionInjector.injectSession({ userDid: opts.account.did, handle: opts.account.handle });
  const session = { ...sessionData, pds: opts.pds.url } as OAuthSessionData;
  const res = await fetch(`${new URL(url).origin}/xrpc/${OAUTH_QR_NSID}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cliDid: parsed.cliDid, nonce: parsed.nonce, ...session }),
  });
  if (!res.ok) throw new Error(`qr relay refused the session: ${res.status} ${await res.text()}`);
  await res.body?.cancel();
  return structuredClone(session);
}

export interface AssociationCaller {
  did: string;
  confirm(target: { did: string; fetch: (r: Request) => Response | Promise<Response> }): Promise<Response>;
}

export async function createAssociationCaller(plcDirectoryUrl: string, handle: string): Promise<AssociationCaller> {
  const kp = await Secp256k1Keypair.create({ exportable: true });
  const { did, op } = await createGenesisOp({
    rotationKeys: [kp.did()],
    verificationMethods: { atproto: kp.did() },
    alsoKnownAs: [`at://${handle}`],
    services: {},
    sign: (bytes: Uint8Array) => kp.sign(bytes),
  });
  await createPlcDirectoryClient({ plcDirectoryUrl }).submitOp(did, op);
  const signer = { did: () => did, sign: (bytes: Uint8Array) => kp.sign(bytes) };
  return {
    did,
    async confirm(target) {
      const token = await signServiceAuth(signer, { aud: target.did, lxm: ASSOCIATE_CONFIRM_NSID });
      return await target.fetch(new Request(`https://local.test/xrpc/${ASSOCIATE_CONFIRM_NSID}`, {
        method: "POST",
        headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: "{}",
      }));
    },
  };
}

export async function waitFor<T>(what: string, probe: () => T | undefined | null | false | Promise<T | undefined | null | false>, timeoutMs = 20_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  let last: unknown;
  while (Date.now() < deadline) {
    const got = await probe();
    if (got) return got;
    last = got;
    await new Promise((r) => setTimeout(r, 20));
  }
  throw new Error(`timed out waiting for ${what} (last ${JSON.stringify(last)})`);
}
