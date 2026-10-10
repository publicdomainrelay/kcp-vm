import { ReplayLedger } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import type { ProvedRequest, RequestProofVerifier } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  REQUEST_PROOF_MAX_SKEW_SEC,
  REQUEST_PROOF_SCHEME,
  REQUEST_PROOF_TYP,
  type RequestProofClaims,
} from "@publicdomainrelay/socialweb-computer-k8s-common";
import type { OAuthSessionData } from "@publicdomainrelay/socialweb-computer-common";

export interface StoredSessionLookup {
  get(did: string): Promise<OAuthSessionData | undefined>;
}

export interface RequestProofVerifierOptions {
  sessions: StoredSessionLookup;
  publicOrigin?: string;
  maxSkewSec?: number;
  nowSec?: () => number;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

const decoder = new TextDecoder();
const encoder = new TextEncoder();

function base64UrlDecode(segment: string): Uint8Array<ArrayBuffer> {
  const padded = segment.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(segment.length / 4) * 4, "=");
  const binary = atob(padded);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

function decodeJson(segment: string): Record<string, unknown> | null {
  try {
    const value = JSON.parse(decoder.decode(base64UrlDecode(segment)));
    return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
  } catch {
    return null;
  }
}

function publicJwk(jwk: Record<string, unknown>): JsonWebKey {
  return { kty: jwk.kty as string, crv: jwk.crv as string, x: jwk.x as string, y: jwk.y as string };
}

function isClaims(value: Record<string, unknown>): value is Record<string, unknown> & RequestProofClaims {
  return typeof value.iss === "string" &&
    typeof value.htm === "string" &&
    typeof value.htu === "string" &&
    typeof value.iat === "number" &&
    typeof value.jti === "string" && value.jti.length >= 16;
}

export function createRequestProofVerifier(opts: RequestProofVerifierOptions): RequestProofVerifier {
  const maxSkewSec = opts.maxSkewSec ?? REQUEST_PROOF_MAX_SKEW_SEC;
  const nowSec = opts.nowSec ?? (() => Math.floor(Date.now() / 1000));
  const log = opts.log ?? (() => {});
  const publicOrigin = opts.publicOrigin?.replace(/\/+$/, "");
  const replays = new ReplayLedger(maxSkewSec);

  function targetMatches(htu: string, path: string): boolean {
    let target: URL;
    try {
      target = new URL(htu);
    } catch {
      return false;
    }
    if (target.search || target.hash) return false;
    if (publicOrigin) return `${target.origin}${target.pathname}` === `${publicOrigin}${path}`;
    return target.pathname === path;
  }

  function refuse(reason: string, data: Record<string, unknown> = {}): null {
    log("request_proof_refused", { reason, ...data });
    return null;
  }

  return {
    async verify(request: ProvedRequest): Promise<{ did: string } | null> {
      const header = request.authorization ?? "";
      const prefix = `${REQUEST_PROOF_SCHEME} `;
      if (!header.startsWith(prefix)) return refuse("no proof");
      const parts = header.slice(prefix.length).trim().split(".");
      if (parts.length !== 3) return refuse("malformed proof");

      const head = decodeJson(parts[0]);
      const claims = decodeJson(parts[1]);
      if (!head || !claims) return refuse("malformed proof");
      if (head.typ !== REQUEST_PROOF_TYP || head.alg !== "ES256") return refuse("wrong proof type");
      if (!isClaims(claims)) return refuse("incomplete claims");
      if (!claims.iss.startsWith("did:")) return refuse("issuer is not a DID");
      if (claims.htm.toUpperCase() !== request.method.toUpperCase()) return refuse("method mismatch", { did: claims.iss });
      if (!targetMatches(claims.htu, request.path)) return refuse("target mismatch", { did: claims.iss });

      const now = nowSec();
      if (Math.abs(now - claims.iat) > maxSkewSec) return refuse("stale proof", { did: claims.iss });

      const stored = await opts.sessions.get(claims.iss);
      if (!stored?.dpopPublicJwk) return refuse("no session for issuer", { did: claims.iss });

      let key: CryptoKey;
      try {
        key = await crypto.subtle.importKey(
          "jwk",
          publicJwk(stored.dpopPublicJwk as Record<string, unknown>),
          { name: "ECDSA", namedCurve: "P-256" },
          false,
          ["verify"],
        );
      } catch {
        return refuse("stored key unusable", { did: claims.iss });
      }

      let signature: Uint8Array<ArrayBuffer>;
      try {
        signature = base64UrlDecode(parts[2]);
      } catch {
        return refuse("malformed signature", { did: claims.iss });
      }
      const ok = await crypto.subtle.verify(
        { name: "ECDSA", hash: "SHA-256" },
        key,
        signature,
        encoder.encode(`${parts[0]}.${parts[1]}`),
      );
      if (!ok) return refuse("bad signature", { did: claims.iss });
      if (!replays.claim(claims.jti, now)) return refuse("replayed proof", { did: claims.iss });

      return { did: claims.iss };
    },
  };
}
