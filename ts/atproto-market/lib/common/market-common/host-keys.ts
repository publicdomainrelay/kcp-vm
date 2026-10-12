export interface VmHostKey {
  type: string;
  publicKey: string;
  fingerprint: string;
}

export type HostKeysCheck =
  | { ok: true; keys: VmHostKey[] }
  | { ok: false; reason: string };

export const MAX_VM_HOST_KEYS = 8;

const KEY_TYPE = /^[a-z0-9][a-z0-9.@-]{0,63}$/;
const KEY_BLOB = /^[A-Za-z0-9+/]{16,4096}={0,2}$/;

function base64Bytes(b64: string): Uint8Array<ArrayBuffer> | null {
  try {
    return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
}

export function sshKeyBlobType(publicKey: string): string | null {
  const bytes = base64Bytes(publicKey);
  if (!bytes || bytes.length < 4) return null;
  const len = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0);
  if (len === 0 || len > 64 || bytes.length < 4 + len) return null;
  return new TextDecoder().decode(bytes.subarray(4, 4 + len));
}

export async function sshHostKeyFingerprint(publicKey: string): Promise<string> {
  const bytes = base64Bytes(publicKey);
  if (!bytes) throw new Error("public key is not base64");
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return `SHA256:${btoa(String.fromCharCode(...digest)).replace(/=+$/, "")}`;
}

export async function checkHostKeys(raw: unknown): Promise<HostKeysCheck> {
  if (!Array.isArray(raw) || raw.length === 0) return { ok: false, reason: "keys is not a non-empty array" };
  if (raw.length > MAX_VM_HOST_KEYS) return { ok: false, reason: `more than ${MAX_VM_HOST_KEYS} keys` };
  const keys: VmHostKey[] = [];
  const types = new Set<string>();
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") return { ok: false, reason: "key is not an object" };
    const { type, publicKey, fingerprint } = entry as Record<string, unknown>;
    if (typeof type !== "string" || !KEY_TYPE.test(type)) return { ok: false, reason: "key type is malformed" };
    if (typeof publicKey !== "string" || !KEY_BLOB.test(publicKey)) {
      return { ok: false, reason: `${type} public key is not a base64 key blob` };
    }
    if (sshKeyBlobType(publicKey) !== type) return { ok: false, reason: `${type} key blob names another type` };
    if (types.has(type)) return { ok: false, reason: `${type} reported twice` };
    types.add(type);
    const computed = await sshHostKeyFingerprint(publicKey);
    if (fingerprint !== undefined && fingerprint !== computed) {
      return { ok: false, reason: `${type} fingerprint does not match its key` };
    }
    keys.push({ type, publicKey, fingerprint: computed });
  }
  keys.sort((a, b) => a.type < b.type ? -1 : a.type > b.type ? 1 : 0);
  return { ok: true, keys };
}

export function sameHostKeys(a: VmHostKey[], b: VmHostKey[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((k, i) => k.type === b[i].type && k.publicKey === b[i].publicKey);
}
