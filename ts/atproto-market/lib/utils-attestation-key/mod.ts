function bytesToHex(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += b.toString(16).padStart(2, "0");
  return s;
}

function base64UrlEncode(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64UrlDecode(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  const padded = b64.padEnd(b64.length + ((4 - (b64.length % 4)) % 4), "=");
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export async function loadOrCreateAttestationKeyHex(jwkPath: string | URL): Promise<string> {
  let text: string | undefined;
  try {
    text = await Deno.readTextFile(jwkPath);
  } catch (err) {
    if (!(err instanceof Deno.errors.NotFound)) throw err;
  }

  if (text !== undefined) {
    const jwk = JSON.parse(text) as { kty?: string; crv?: string; d?: string };
    if (jwk.kty !== "EC" || jwk.crv !== "secp256k1" || typeof jwk.d !== "string") {
      throw new Error(`${jwkPath}: not a secp256k1 private JWK (refusing to overwrite)`);
    }
    return bytesToHex(base64UrlDecode(jwk.d));
  }

  const priv = crypto.getRandomValues(new Uint8Array(32));
  const jwk = { kty: "EC", crv: "secp256k1", use: "sig", alg: "ES256K", d: base64UrlEncode(priv) };
  await Deno.writeTextFile(jwkPath, JSON.stringify(jwk, null, 2) + "\n");
  try {
    await Deno.chmod(jwkPath, 0o600);
  } catch {
    // chmod unsupported
  }
  return bytesToHex(priv);
}
