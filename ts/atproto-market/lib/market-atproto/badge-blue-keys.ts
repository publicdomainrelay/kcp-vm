// badgeBlueKeys key-binding helpers.
//
// A producer that cannot publish its attestation did:key in its DID document
// (e.g. an OAuth-author bidder writing to the account PDS it OAuth'd with) binds
// the key by writing a com.publicdomainrelay.temp.badgeBlueKeys record to its
// own repo at a DETERMINISTIC rkey — base58btc(sha256(`${did}:${keyId}`)[0:24])
// [0:32] — so a verifier checks the binding with a single getRecord, never a
// listRecords scan.

import { getPdsEndpoint } from "@atproto/common-web";
import type { IdResolver } from "@atproto/identity";

const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
const BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";

/** Base-58 BTC encode (no multibase prefix). */
export function base58btcEncode(bytes: Uint8Array): string {
  let zeroCount = 0;
  while (zeroCount < bytes.length && bytes[zeroCount] === 0) zeroCount++;
  let value = 0n;
  for (let i = 0; i < bytes.length; i++) value = (value << 8n) | BigInt(bytes[i]);
  const result: string[] = [];
  while (value > 0n) {
    result.unshift(BASE58_ALPHABET[Number(value % 58n)]);
    value /= 58n;
  }
  for (let i = 0; i < zeroCount; i++) result.unshift("1");
  return result.join("");
}

/**
 * Deterministic rkey for a badgeBlueKeys record binding `keyId` to `did`:
 * base58btc(sha256(`${did}:${keyId}`)[0:24])[0:32]. Same (did, keyId) always
 * yields the same rkey, so both the minting producer and any verifier derive it
 * without coordination.
 */
export async function badgeBlueKeysRkey(did: string, keyId: string): Promise<string> {
  const hash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(`${did}:${keyId}`)),
  );
  return base58btcEncode(hash.slice(0, 24)).slice(0, 32);
}

/**
 * Verifier side: is `key` bound to `did` via a badgeBlueKeys record? Computes the
 * deterministic rkey and does a single getRecord against the author's PDS. Never
 * throws.
 */
export async function keyBoundByBadgeBlueKey(
  idResolver: IdResolver,
  did: string,
  key: string,
): Promise<boolean> {
  try {
    const rkey = await badgeBlueKeysRkey(did, key);
    const doc = await idResolver.did.resolve(did);
    if (!doc) return false;
    const pds = getPdsEndpoint(doc);
    if (!pds) return false;
    const url = `${pds}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(did)}&collection=${encodeURIComponent(BADGE_BLUE_KEYS_NSID)}&rkey=${rkey}`;
    const res = await fetch(url, { signal: AbortSignal.timeout(8000) });
    if (!res.ok) return false;
    const data = await res.json() as { value?: { keyId?: unknown; challenge?: unknown } };
    return data.value?.keyId === key && data.value?.challenge === did;
  } catch {
    return false;
  }
}

/**
 * Memoized {@link keyBoundByBadgeBlueKey}: caches each (did, key) result so the
 * getRecord fetch happens once per signing key, not per signature entry or per
 * request — mirroring `createDidKeyResolver`'s per-DID document cache. Producers
 * mint their badgeBlueKeys record at boot, before any record is verified, so a
 * permanent cache is safe.
 */
export function createKeyBoundByBadgeBlueKey(idResolver: IdResolver): (did: string, key: string) => Promise<boolean> {
  const cache = new Map<string, boolean>();
  return async (did: string, key: string): Promise<boolean> => {
    const cacheKey = `${did}#${key}`;
    const cached = cache.get(cacheKey);
    if (cached !== undefined) return cached;
    const ok = await keyBoundByBadgeBlueKey(idResolver, did, key);
    cache.set(cacheKey, ok);
    return ok;
  };
}

/**
 * Producer side: write a badgeBlueKeys record binding `keyId` to `did` at the
 * deterministic rkey. `writeRecord` must place the record at the exact rkey
 * (putRecord-style), not auto-assign a TID.
 */
export async function createBadgeBlueKeysRecord(opts: {
  did: string;
  keyId: string;
  service: string;
  writeRecord(
    did: string,
    collection: string,
    rkey: string,
    record: Record<string, unknown>,
  ): Promise<{ uri: string; cid: string }>;
}): Promise<{ uri: string; cid: string }> {
  const rkey = await badgeBlueKeysRkey(opts.did, opts.keyId);
  return opts.writeRecord(opts.did, BADGE_BLUE_KEYS_NSID, rkey, {
    $type: BADGE_BLUE_KEYS_NSID,
    keyId: opts.keyId,
    challenge: opts.did,
    service: opts.service,
    createdAt: new Date().toISOString(),
  });
}
