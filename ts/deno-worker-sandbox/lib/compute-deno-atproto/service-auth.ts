import { DenoComputeError } from "@publicdomainrelay/compute-deno-common";
import type { SigningKey } from "@publicdomainrelay/compute-deno-abc";
import { verifySignatureUtf8, formatDidKey, multibaseToBytes, SECP256K1_JWT_ALG, P256_JWT_ALG } from "@atproto/crypto";

function b64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function b64urlJson(value: unknown): string {
  return b64url(new TextEncoder().encode(JSON.stringify(value)));
}

function b64urlDecode(s: string): string {
  return atob(s.replace(/-/g, "+").replace(/_/g, "/"));
}

export async function signComputeServiceAuth(
  signingKey: SigningKey,
  aud: string,
  lxm?: string,
  expiresInSec?: number,
): Promise<string> {
  const iss = signingKey.did();
  const now = Math.floor(Date.now() / 1000);
  const exp = now + (expiresInSec ?? 60);
  const header = { typ: "JWT", alg: "ES256K" };
  const payload: Record<string, unknown> = {
    iss,
    aud,
    iat: now,
    exp,
    jti: b64url(crypto.getRandomValues(new Uint8Array(16))),
  };
  if (lxm) payload.lxm = lxm;
  const signingInput = `${b64urlJson(header)}.${b64urlJson(payload)}`;
  const sig = await signingKey.sign(new TextEncoder().encode(signingInput));
  return `${signingInput}.${b64url(sig)}`;
}

export async function verifyComputeServiceAuth(
  authHeader: string | null | undefined,
  hostname: string,
  lxm: string,
  strictAuth: boolean,
): Promise<{ issuerDid: string }> {
  if (!authHeader) {
    throw new DenoComputeError("missing Authorization header", 401, "AuthRequired");
  }

  const parts = authHeader.split(" ");
  if (parts.length !== 2 || parts[0].toLowerCase() !== "bearer") {
    throw new DenoComputeError("Authorization must be Bearer <token>", 401, "AuthRequired");
  }

  const token = parts[1];
  const jwtParts = token.split(".");
  if (jwtParts.length !== 3) {
    throw new DenoComputeError("malformed JWT — expected 3 parts", 401, "AuthRequired");
  }
  const [headerB64, payloadB64, sigB64] = jwtParts;

  let header: Record<string, unknown>;
  let payload: Record<string, unknown>;
  try {
    header = JSON.parse(b64urlDecode(headerB64));
    payload = JSON.parse(b64urlDecode(payloadB64));
  } catch {
    throw new DenoComputeError("failed to decode service auth JWT", 401, "AuthRequired");
  }

  const audDid = `did:web:${hostname}`;
  if (payload.aud !== audDid) {
    throw new DenoComputeError(
      `aud mismatch: expected ${audDid}`,
      401,
      "AuthRequired",
    );
  }

  if (payload.lxm !== lxm) {
    throw new DenoComputeError(
      `lxm mismatch: expected ${lxm}`,
      401,
      "AuthRequired",
    );
  }

  if (typeof payload.exp === "number" && payload.exp * 1000 < Date.now()) {
    throw new DenoComputeError("service auth token expired", 401, "AuthRequired");
  }

  const issuerDid = payload.iss as string;
  if (!issuerDid?.startsWith("did:")) {
    throw new DenoComputeError("invalid issuer DID", 401, "AuthRequired");
  }

  if (strictAuth) {
    await verifyJwtSignature(issuerDid, header, headerB64, payloadB64, sigB64, hostname);
  }

  return { issuerDid };
}

async function verifyJwtSignature(
  issuerDid: string,
  header: Record<string, unknown>,
  headerB64: string,
  payloadB64: string,
  sigB64: string,
  hostname: string,
): Promise<void> {
  if (issuerDid === "did:plc:local" || hostname === "localhost") {
    return;
  }

  let didDoc: Record<string, unknown>;

  if (issuerDid.startsWith("did:web:")) {
    const domain = issuerDid.slice("did:web:".length);
    const url = `https://${domain}/.well-known/did.json`;
    try {
      const res = await fetch(url);
      if (!res.ok) {
        throw new DenoComputeError(
          `failed to resolve issuer DID: HTTP ${res.status}`,
          401,
          "AuthRequired",
        );
      }
      didDoc = await res.json() as Record<string, unknown>;
    } catch (err) {
      if (err instanceof DenoComputeError) throw err;
      throw new DenoComputeError(
        `failed to resolve issuer DID: ${String(err)}`,
        401,
        "AuthRequired",
      );
    }
  } else {
    throw new DenoComputeError(
      `unsupported DID method for signature verification`,
      401,
      "AuthRequired",
    );
  }

  const vmArray = didDoc.verificationMethod as Array<Record<string, unknown>> | undefined;
  if (!vmArray?.length) {
    throw new DenoComputeError("no verification methods in DID document", 401, "AuthRequired");
  }

  const atprotoKey = vmArray.find((vm) =>
    vm.id === "#atproto" || vm.id === `${issuerDid}#atproto`
  );
  if (!atprotoKey) {
    throw new DenoComputeError("no #atproto verification key in DID document", 401, "AuthRequired");
  }

  const publicKeyMultibase = atprotoKey.publicKeyMultibase as string | undefined;
  if (!publicKeyMultibase) {
    throw new DenoComputeError("unsupported key format, expected Multikey", 401, "AuthRequired");
  }

  const alg = header.alg as string | undefined ?? "ES256K";

  let keyBytes: Uint8Array;
  try {
    keyBytes = multibaseToBytes(publicKeyMultibase);
  } catch {
    throw new DenoComputeError("invalid multibase key encoding", 401, "AuthRequired");
  }

  const jwtAlg = alg === "ES256" ? P256_JWT_ALG : SECP256K1_JWT_ALG;
  const didKey = formatDidKey(jwtAlg, keyBytes);

  const signingInput = `${headerB64}.${payloadB64}`;

  let valid = false;
  try {
    valid = await verifySignatureUtf8(didKey, signingInput, sigB64);
  } catch {
    valid = false;
  }

  if (!valid) {
    throw new DenoComputeError("JWT signature verification failed", 401, "AuthRequired");
  }
}

function base64UrlDecodeToBytes(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  const padded = b64.padEnd(b64.length + ((4 - (b64.length % 4)) % 4), "=");
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
