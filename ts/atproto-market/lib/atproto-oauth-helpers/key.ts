// Concrete Web Crypto Key for @atproto/oauth-client.
// Implements DPoP signing via Web Crypto API (Deno, browsers).

import { Key } from "@atproto/jwk";

// Minimal JWK shape for the abstract Key superclass
function buildJwk(publicJwk: Record<string, unknown>, privateJwk: Record<string, unknown>, alg: string, kid: string) {
  return { ...publicJwk, ...privateJwk, alg, kid, use: "sig" as const, key_ops: ["sign", "verify"] as const };
}

export class WebCryptoKey extends Key {
  readonly #cryptoKey: CryptoKey;
  readonly #alg: string;
  readonly #publicJwk: Record<string, unknown>;

  constructor(
    cryptoKey: CryptoKey,
    publicJwk: Record<string, unknown>,
    privateJwk: Record<string, unknown>,
    alg: string,
    kid: string,
  ) {
    super(buildJwk(publicJwk, privateJwk, alg, kid) as never);
    this.#cryptoKey = cryptoKey;
    this.#alg = alg;
    this.#publicJwk = publicJwk;
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async createJwt(header: any, payload: any): Promise<`${string}.${string}.${string}`> {
    const enc = new TextEncoder();
    const hdr = JSON.stringify({ ...header, alg: this.#alg, kid: this.jwk.kid });
    const pld = JSON.stringify(payload);
    const input = `${base64url(hdr)}.${base64url(pld)}`;
    const sig = await crypto.subtle.sign(
      { name: "ECDSA", hash: { name: "SHA-256" } },
      this.#cryptoKey,
      enc.encode(input),
    );
    const signed = `${input}.${base64url(String.fromCharCode(...new Uint8Array(sig)))}` as `${string}.${string}.${string}`;
    return signed;
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async verifyJwt<C extends string = never>(_token: string, _options?: any): Promise<any> {
    throw new Error("WebCryptoKey.verifyJwt not implemented");
  }
}

function base64url(input: string): string {
  return btoa(input).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Create a Key for @atproto/oauth-client from Web Crypto key pair. */
export async function createWebCryptoKey(algs: string[]): Promise<Key> {
  const alg = algs.includes("ES256") ? "ES256" : algs[0];
  const keyPair = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign", "verify"],
  );
  const privateJwk = await crypto.subtle.exportKey("jwk", keyPair.privateKey) as Record<string, unknown>;
  const publicJwk = await crypto.subtle.exportKey("jwk", keyPair.publicKey) as Record<string, unknown>;
  const kid = crypto.randomUUID();
  return new WebCryptoKey(keyPair.privateKey, publicJwk, privateJwk, alg, kid);
}
