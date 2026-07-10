import { Hono } from "@hono/hono";
import { verifySignature } from "@atproto/crypto";

const OAUTH_QR_NSID = "com.fedfork.atprotoOauthQR";

// ── Service auth JWT verification ──────────────────────────────────────────

function base64urlToBytes(b64: string): Uint8Array {
  const padded = b64.replace(/-/g, "+").replace(/_/g, "/");
  const pad = (4 - (padded.length % 4)) % 4;
  return Uint8Array.from(atob(padded + "=".repeat(pad)), (c) => c.charCodeAt(0));
}

function utf8Encode(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

interface JwtPayload {
  iss: string;
  aud: string;
  exp: number;
  lxm?: string;
  [k: string]: unknown;
}

interface JwtHeader {
  alg: string;
  typ?: string;
  [k: string]: unknown;
}

async function resolveAtprotoKey(did: string): Promise<string> {
  if (did.startsWith("did:plc:")) {
    const res = await fetch(`https://plc.directory/${did}`);
    if (!res.ok) throw new Error(`PLC resolution failed: ${res.status}`);
    const doc = await res.json();
    const vm = (doc.verificationMethod || []).find(
      (m: { id: string }) => m.id === `${did}#atproto` || m.id === "#atproto",
    );
    if (!vm) throw new Error(`no #atproto verificationMethod for ${did}`);
    return vm.publicKeyMultibase;
  }
  if (did.startsWith("did:web:")) {
    const domain = did.slice("did:web:".length);
    const res = await fetch(`https://${domain}/.well-known/did.json`);
    if (!res.ok) throw new Error(`did:web resolution failed: ${res.status}`);
    const doc = await res.json();
    const vm = (doc.verificationMethod || []).find(
      (m: { id: string }) => m.id === `${did}#atproto` || m.id === "#atproto",
    );
    if (!vm) throw new Error(`no #atproto verificationMethod for ${did}`);
    return vm.publicKeyMultibase;
  }
  throw new Error(`unsupported DID method: ${did.split(":")[1]}`);
}

async function verifyServiceAuthJwt(
  token: string,
  expectedAud: string,
  expectedLxm: string,
): Promise<string> {
  const parts = token.split(".");
  if (parts.length !== 3) throw new Error("malformed JWT");

  const header = JSON.parse(new TextDecoder().decode(base64urlToBytes(parts[0]))) as JwtHeader;
  if (header.alg !== "ES256K") throw new Error(`unsupported JWT alg: ${header.alg}`);

  const payload = JSON.parse(new TextDecoder().decode(base64urlToBytes(parts[1]))) as JwtPayload;

  if (typeof payload.exp !== "number") throw new Error("missing exp");
  if (Date.now() / 1000 > payload.exp) throw new Error("token expired");
  if (payload.lxm !== expectedLxm) {
    throw new Error(`lxm mismatch: expected ${expectedLxm}, got ${payload.lxm}`);
  }
  if (payload.aud !== expectedAud) {
    throw new Error(`aud mismatch: expected ${expectedAud}, got ${payload.aud}`);
  }
  if (!payload.iss || !payload.iss.startsWith("did:")) {
    throw new Error("invalid issuer DID");
  }

  const signingInput = utf8Encode(`${parts[0]}.${parts[1]}`);
  const sigBytes = base64urlToBytes(parts[2]);

  const publicKeyMultibase = await resolveAtprotoKey(payload.iss);
  const atprotoKey = `did:key:${publicKeyMultibase}`;
  const valid = await verifySignature(atprotoKey, signingInput, sigBytes);
  if (!valid) throw new Error("invalid JWT signature");

  return payload.iss;
}

// ── Session store (Deno.Kv) ────────────────────────────────────────────────

interface StoredEntry {
  nonce: string;
  session: OAuthSessionData | null;
  createdAt: number;
}

interface OAuthSessionData {
  accessJwt: string;
  refreshJwt: string;
  userDid: string;
  handle: string;
  pds: string;
  dpopPublicJwk: Record<string, string>;
  dpopPrivateJwk: Record<string, string>;
}

const SESSION_TTL_MS = 5 * 60 * 1000; // 5 minutes

const kvPath = Deno.env.get("KV_PATH") || undefined;
let kv: Deno.Kv;

async function getKv(): Promise<Deno.Kv> {
  if (!kv) kv = await Deno.openKv(kvPath);
  return kv;
}

async function loadEntry(cliDid: string): Promise<StoredEntry | null> {
  const k = await getKv();
  const result = await k.get<StoredEntry>(["oauthQr", cliDid]);
  if (!result.value) return null;
  // TTL check: expire old entries
  if (Date.now() - result.value.createdAt > SESSION_TTL_MS) {
    await k.delete(["oauthQr", cliDid]);
    return null;
  }
  return result.value;
}

async function storeEntry(cliDid: string, entry: StoredEntry): Promise<void> {
  const k = await getKv();
  await k.set(["oauthQr", cliDid], entry);
}

// ── Hono app ───────────────────────────────────────────────────────────────

const app = new Hono();

// POST: browser stores session after OAuth completes
app.post(`/xrpc/${OAUTH_QR_NSID}`, async (c) => {
  let body: Record<string, unknown>;
  try {
    body = await c.req.json();
  } catch {
    return c.json({ error: "InvalidRequest", message: "invalid JSON" }, 400);
  }

  const cliDid = body.cliDid as string | undefined;
  const nonce = body.nonce as string | undefined;
  const accessJwt = body.accessJwt as string | undefined;
  const refreshJwt = body.refreshJwt as string | undefined;
  const userDid = body.userDid as string | undefined;
  const handle = body.handle as string | undefined;
  const pds = body.pds as string | undefined;
  const dpopPublicJwk = body.dpopPublicJwk as Record<string, string> | undefined;
  const dpopPrivateJwk = body.dpopPrivateJwk as Record<string, string> | undefined;

  if (!cliDid?.startsWith("did:") || !nonce) {
    return c.json({ error: "InvalidRequest", message: "missing cliDid or nonce" }, 400);
  }

  const entry = await loadEntry(cliDid);
  if (!entry) {
    // First contact — store nonce for this transfer attempt
    await storeEntry(cliDid, { nonce, session: null, createdAt: Date.now() });
  } else if (entry.nonce !== nonce) {
    // Nonce changed — allow if no session stored yet (new QR scan), reject if session exists
    if (entry.session) {
      return c.json({ error: "Forbidden", message: "nonce mismatch" }, 403);
    }
    // No session yet — update nonce to match new QR code
    await storeEntry(cliDid, { nonce, session: null, createdAt: Date.now() });
  }

  if (accessJwt && refreshJwt && userDid && handle && pds && dpopPublicJwk && dpopPrivateJwk) {
    const session: OAuthSessionData = {
      accessJwt, refreshJwt, userDid, handle, pds,
      dpopPublicJwk, dpopPrivateJwk,
    };
    await storeEntry(cliDid, { nonce: entry?.nonce ?? nonce, session, createdAt: Date.now() });
    return c.json({ ok: true });
  }

  // No session data yet — initial POST from browser (just registers nonce)
  return c.json({ ok: true, status: "waiting" });
});

// GET: CLI polls for session
app.get(`/xrpc/${OAUTH_QR_NSID}`, async (c) => {
  const authHeader = c.req.header("authorization");
  if (!authHeader) {
    return c.json({ error: "Unauthorized", message: "missing Authorization header" }, 401);
  }

  const match = /^Bearer (.+)$/.exec(authHeader.trim());
  if (!match) {
    return c.json({ error: "Unauthorized", message: "malformed Authorization header" }, 401);
  }
  const token = match[1];

  const hostname = c.req.header("host") ?? "qr.fedfork.com";
  const audDid = `did:web:${hostname.split(":")[0]}`;

  let issuerDid: string;
  try {
    issuerDid = await verifyServiceAuthJwt(token, audDid, OAUTH_QR_NSID);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    return c.json({ error: "Unauthorized", message: msg }, 401);
  }

  const entry = await loadEntry(issuerDid);
  if (!entry || !entry.session) {
    return c.json({ error: "notReady" }, 404);
  }

  return c.json(entry.session);
});

// ── Serve ──────────────────────────────────────────────────────────────────

const port = parseInt(Deno.env.get("PORT") ?? "5557");
Deno.serve({ port }, app.fetch);
console.log(`oauth-qr backend listening on :${port}`);
