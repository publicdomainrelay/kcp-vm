import { DenoComputeError } from "@publicdomainrelay/compute-deno-common";
import type { SigningKey } from "@publicdomainrelay/compute-deno-abc";
import { Secp256k1Keypair } from "@atproto/crypto";
import * as dagCbor from "@ipld/dag-cbor";

export async function signerFromPrivateKeyHex(hex: string): Promise<SigningKey> {
  const kp = await Secp256k1Keypair.import(hex);
  return { did: () => kp.did(), sign: (bytes: Uint8Array) => kp.sign(bytes) };
}

const MULTIBASE_BASE32_PREFIX = "b";
const CIDV1_VERSION = 0x01;
const DAG_CBOR_CODEC = 0x71;
const SHA256_MULTIHASH = 0x12;
const SHA256_LENGTH = 0x20;

export interface SignAttestationInput {
  record: Record<string, unknown>;
  repository: string;
  issuer?: string;
}

export interface InlineAttestation {
  $type: "network.attested.signature";
  key: string;
  cid: string;
  signature: Uint8Array;
  issuer?: string;
  issuedAt?: string;
}

export async function createInlineAttestationForRecord(
  input: SignAttestationInput,
  key: SigningKey,
): Promise<InlineAttestation> {
  if (typeof input.record.$type !== "string" || input.record.$type.length === 0) {
    throw new DenoComputeError("record is missing $type", 400, "InvalidRequest");
  }

  const metadata: Record<string, unknown> = {
    $type: "network.attested.signature",
    key: key.did(),
    repository: input.repository,
  };
  if (input.issuer) metadata.issuer = input.issuer;
  metadata.issuedAt = new Date().toISOString();

  const preparedRecord: Record<string, unknown> = { ...input.record };
  delete preparedRecord.signatures;
  preparedRecord.$sig = metadata;

  const cidStr = await createAttestationCidStr(preparedRecord);

  const sig = await key.sign(cidBytesFromStr(cidStr));
  const lowSSig = normalizeLowS(sig);

  return {
    $type: "network.attested.signature",
    key: key.did(),
    cid: cidStr,
    signature: lowSSig,
    issuer: input.issuer,
    issuedAt: metadata.issuedAt as string,
  };
}

async function createAttestationCidStr(obj: Record<string, unknown>): Promise<string> {
  const encoded = dagCbor.encode(obj);
  const bytes = new Uint8Array(encoded);
  const hash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", bytes.buffer as ArrayBuffer),
  );
  const cidBytes = new Uint8Array(4 + hash.length);
  cidBytes[0] = CIDV1_VERSION;
  cidBytes[1] = DAG_CBOR_CODEC;
  cidBytes[2] = SHA256_MULTIHASH;
  cidBytes[3] = SHA256_LENGTH;
  cidBytes.set(hash, 4);
  return MULTIBASE_BASE32_PREFIX + base32Encode(cidBytes).toLowerCase();
}

function cidBytesFromStr(cidStr: string): Uint8Array {
  if (cidStr[0] !== MULTIBASE_BASE32_PREFIX) {
    throw new DenoComputeError("CID missing base32 prefix", 400, "InvalidRequest");
  }
  return base32Decode(cidStr.slice(1));
}

function base32Encode(bytes: Uint8Array): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  let bits = 0;
  let value = 0;
  let output = "";
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      output += alphabet[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) {
    output += alphabet[(value << (5 - bits)) & 31];
  }
  return output;
}

function base32Decode(input: string): Uint8Array {
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  const lookup = new Map<string, number>();
  for (let i = 0; i < alphabet.length; i++) lookup.set(alphabet[i], i);
  const result: number[] = [];
  let bits = 0;
  let value = 0;
  for (const ch of input.toLowerCase()) {
    const v = lookup.get(ch);
    if (v === undefined) throw new DenoComputeError(`Invalid base32 character: ${ch}`, 400, "InvalidRequest");
    value = (value << 5) | v;
    bits += 5;
    if (bits >= 8) {
      result.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return new Uint8Array(result);
}

function normalizeLowS(sig: Uint8Array): Uint8Array {
  if (sig.length < 64) return sig;
  const halfOrder = new Uint8Array([
    0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
    0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
    0x5d, 0x57, 0x6e, 0x73, 0x57, 0xa4, 0x50, 0x1d,
    0xdf, 0xe9, 0x2f, 0x46, 0x68, 0x1b, 0x20, 0xa0,
  ]);
  const r = sig.slice(0, 32);
  const s = sig.slice(32, 64);
  if (compareBytes(s, halfOrder) > 0) {
    const n = new Uint8Array([
      0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
      0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe,
      0xba, 0xae, 0xdc, 0xe6, 0xaf, 0x48, 0xa0, 0x3b,
      0xbf, 0xd2, 0x5e, 0x8c, 0xd0, 0x36, 0x41, 0x41,
    ]);
    const negS = new Uint8Array(32);
    let borrow = 0;
    for (let i = 31; i >= 0; i--) {
      const diff = n[i] - s[i] - borrow;
      if (diff < 0) {
        negS[i] = diff + 256;
        borrow = 1;
      } else {
        negS[i] = diff;
        borrow = 0;
      }
    }
    const result = new Uint8Array(64);
    result.set(r, 0);
    result.set(negS, 32);
    return result;
  }
  return sig;
}

function compareBytes(a: Uint8Array, b: Uint8Array): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    if (a[i] !== b[i]) return a[i] - b[i];
  }
  return a.length - b.length;
}

export function signatureToBase64Url(sig: Uint8Array): string {
  let bin = "";
  for (const b of sig) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
