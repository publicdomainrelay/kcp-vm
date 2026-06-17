import { encode as cborEncode } from "@ipld/dag-cbor";
import { base32 } from "multiformats/bases/base32";
import type { Operation, PlcOp, TombstoneOp, LogEntry } from "@publicdomainrelay/did-plc";

function toBase64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64url(b64url: string): Uint8Array {
  let b64 = b64url.replace(/-/g, "+").replace(/_/g, "/");
  while (b64.length % 4 !== 0) b64 += "=";
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

async function sha256(bytes: Uint8Array): Promise<Uint8Array> {
  const buf = await crypto.subtle.digest("SHA-256", bytes.buffer as ArrayBuffer);
  return new Uint8Array(buf);
}

export async function computeOperationCid(signedBytes: Uint8Array): Promise<string> {
  const hash = await sha256(signedBytes);
  const mh = new Uint8Array([0x12, 0x20, ...hash]);
  const cidBytes = new Uint8Array([0x01, 0x71, ...mh]);
  return "b" + base32.baseEncode(cidBytes);
}

export function validateOperationStructure(op: unknown): string | null {
  if (!op || typeof op !== "object") return "Operation must be an object";

  const o = op as Record<string, unknown>;
  const type = o.type;

  if (type === "plc_operation") {
    if (!Array.isArray(o.rotationKeys)) return "rotationKeys must be an array";
    if (o.verificationMethods !== undefined && typeof o.verificationMethods !== "object")
      return "verificationMethods must be an object";
    if (o.alsoKnownAs !== undefined && !Array.isArray(o.alsoKnownAs))
      return "alsoKnownAs must be an array";
    if (o.services !== undefined && typeof o.services !== "object")
      return "services must be an object";
    if (typeof o.sig !== "string") return "sig must be a string";
    if (o.prev !== null && o.prev !== undefined && typeof o.prev !== "string")
      return "prev must be null or a CID string";
    return null;
  }

  if (type === "plc_tombstone") {
    if (typeof o.prev !== "string") return "prev must be a CID string";
    if (typeof o.sig !== "string") return "sig must be a string";
    return null;
  }

  if (type === "create") {
    if (typeof o.signingKey !== "string") return "signingKey must be a string";
    if (typeof o.recoveryKey !== "string") return "recoveryKey must be a string";
    if (typeof o.handle !== "string") return "handle must be a string";
    if (typeof o.service !== "string") return "service must be a string";
    if (typeof o.sig !== "string") return "sig must be a string";
    return null;
  }

  return `Unknown operation type: ${type}`;
}

export async function verifyOperationSignature(
  op: PlcOp | TombstoneOp,
  signerDid: string,
  verifySig: (did: string, data: Uint8Array, sig: Uint8Array) => Promise<boolean>,
): Promise<boolean> {
  const { sig: _sig, ...unsigned } = op;
  const unsignedBytes = cborEncode(unsigned);
  const sigBytes = fromBase64url(op.sig);
  return verifySig(signerDid, unsignedBytes, sigBytes);
}

export function validatePrevChain(
  prev: string | null,
  existingOps: LogEntry[],
): string | null {
  if (prev === null) {
    if (existingOps.length > 0) {
      return "Genesis operation requires empty log";
    }
    return null;
  }

  const prevOp = existingOps.find((e) => e.cid === prev && !e.nullified);
  if (!prevOp) {
    return `prev CID not found in current chain: ${prev}`;
  }
  return null;
}

export function validateRotationKeyAuth(
  prevOp: PlcOp,
  signerDid: string,
): boolean {
  return prevOp.rotationKeys.includes(signerDid);
}
