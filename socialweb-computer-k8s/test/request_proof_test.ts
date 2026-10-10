import { assertEquals } from "@std/assert";
import { createRequestProofVerifier } from "@publicdomainrelay/socialweb-computer-k8s-request-proof";
import type { OAuthSessionData } from "@publicdomainrelay/socialweb-computer-common";

const DID = "did:plc:proofowner";
const ORIGIN = "https://k8s.example.com";
const enc = new TextEncoder();

function b64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

async function keypair() {
  const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  return {
    pair,
    publicJwk: await crypto.subtle.exportKey("jwk", pair.publicKey),
    privateJwk: await crypto.subtle.exportKey("jwk", pair.privateKey),
  };
}

function session(publicJwk: JsonWebKey, privateJwk: JsonWebKey): OAuthSessionData {
  return {
    accessJwt: "a",
    refreshJwt: "r",
    userDid: DID,
    handle: "owner.example.com",
    pds: "https://pds.example.com",
    dpopPublicJwk: publicJwk,
    dpopPrivateJwk: privateJwk,
  } as OAuthSessionData;
}

async function sign(key: CryptoKey, claims: Record<string, unknown>, head: Record<string, unknown> = { typ: "swc-proof+jwt", alg: "ES256" }) {
  const input = `${b64url(enc.encode(JSON.stringify(head)))}.${b64url(enc.encode(JSON.stringify(claims)))}`;
  const sig = new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, enc.encode(input)));
  return `SWC-Proof ${input}.${b64url(sig)}`;
}

function claims(overrides: Record<string, unknown> = {}) {
  return {
    iss: DID,
    htm: "POST",
    htu: `${ORIGIN}/api/clusters/list`,
    iat: 1_000_000,
    jti: crypto.randomUUID(),
    ...overrides,
  };
}

async function harness() {
  const k = await keypair();
  const stored = new Map([[DID, session(k.publicJwk, k.privateJwk)]]);
  const verifier = createRequestProofVerifier({
    sessions: { get: (did) => Promise.resolve(stored.get(did)) },
    publicOrigin: ORIGIN,
    nowSec: () => 1_000_000,
  });
  return { k, stored, verifier };
}

const req = (authorization: string, path = "/api/clusters/list", method = "POST") => ({ method, path, authorization });

Deno.test("a proof signed with the stored session's key identifies the account", async () => {
  const { k, verifier } = await harness();
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims()))), { did: DID });
});

Deno.test("a proof signed by any other key is refused", async () => {
  const { verifier } = await harness();
  const stranger = await keypair();
  assertEquals(await verifier.verify(req(await sign(stranger.pair.privateKey, claims()))), null);
});

Deno.test("a proof is bound to its method, path and origin", async () => {
  const { k, verifier } = await harness();
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims()), "/api/clusters/delete")), null);
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims()), "/api/clusters/list", "GET")), null);
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims({ htu: "https://evil.example.com/api/clusters/list" })))), null);
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims({ htu: `${ORIGIN}/api/clusters/list?x=1` })))), null);
});

Deno.test("a stale or future proof is refused", async () => {
  const { k, verifier } = await harness();
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims({ iat: 1_000_000 - 61 })))), null);
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims({ iat: 1_000_000 + 61 })))), null);
});

Deno.test("a proof cannot be replayed", async () => {
  const { k, verifier } = await harness();
  const auth = await sign(k.pair.privateKey, claims());
  assertEquals(await verifier.verify(req(auth)), { did: DID });
  assertEquals(await verifier.verify(req(auth)), null);
});

Deno.test("an account with no stored session is refused even with a valid signature", async () => {
  const { k, verifier } = await harness();
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims({ iss: "did:plc:nobody" })))), null);
});

Deno.test("a DPoP proof for the PDS is not accepted here", async () => {
  const { k, verifier } = await harness();
  const auth = await sign(k.pair.privateKey, claims(), { typ: "dpop+jwt", alg: "ES256" });
  assertEquals(await verifier.verify(req(auth)), null);
});

Deno.test("a missing or malformed header is refused", async () => {
  const { verifier } = await harness();
  assertEquals(await verifier.verify({ method: "POST", path: "/api/clusters/list", authorization: undefined }), null);
  assertEquals(await verifier.verify(req("Bearer x")), null);
  assertEquals(await verifier.verify(req("SWC-Proof a.b")), null);
});

Deno.test("a re-login with a new key retires proofs signed by the old one", async () => {
  const { k, stored, verifier } = await harness();
  const next = await keypair();
  stored.set(DID, session(next.publicJwk, next.privateJwk));
  assertEquals(await verifier.verify(req(await sign(k.pair.privateKey, claims()))), null);
  assertEquals(await verifier.verify(req(await sign(next.pair.privateKey, claims()))), { did: DID });
});

Deno.test("the page's own proof signer produces what the door accepts", async () => {
  const { k, verifier } = await harness();
  const g = globalThis as unknown as { window?: { location: { origin: string } } };
  g.window = { location: { origin: ORIGIN } };
  try {
    const { requestProof } = await import("../web/lib/k8s-api.js");
    const live = createRequestProofVerifier({
      sessions: { get: () => Promise.resolve(session(k.publicJwk, k.privateJwk)) },
      publicOrigin: ORIGIN,
    });
    const jwt = await requestProof(session(k.publicJwk, k.privateJwk), "POST", "/api/clusters/list");
    assertEquals(await live.verify(req(`SWC-Proof ${jwt}`)), { did: DID });
    assertEquals(await verifier.verify(req(`SWC-Proof ${jwt}`, "/api/clusters/create")), null);
  } finally {
    delete g.window;
  }
});
