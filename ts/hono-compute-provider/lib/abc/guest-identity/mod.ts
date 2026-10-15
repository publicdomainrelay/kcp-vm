import { GUEST_OIDC_SUBJECT_CLAIM, guestAudience, parseGuestAudience } from "@publicdomainrelay/guest-identity-common";

export type GuestLoginStep = "bind" | "challenge" | "login";

export const GUEST_LOGIN_FIELDS: Readonly<Record<GuestLoginStep, readonly string[]>> = {
  bind: ["role", "bind_secret", "host_key", "signature"],
  challenge: ["role"],
  login: ["role", "challenge", "signature"],
};

export function isGuestLoginStep(step: string): step is GuestLoginStep {
  return Object.hasOwn(GUEST_LOGIN_FIELDS, step);
}

export function guestLoginBody(step: GuestLoginStep, input: unknown): Record<string, string> | null {
  if (!input || typeof input !== "object") return null;
  const out: Record<string, string> = {};
  for (const field of GUEST_LOGIN_FIELDS[step]) {
    const value = (input as Record<string, unknown>)[field];
    if (typeof value !== "string" || value === "" || value.length > 4096) return null;
    out[field] = value;
  }
  return out;
}

export interface GuestIdentityReply {
  status: number;
  body: unknown;
}

export interface GuestIdentity {
  step(step: GuestLoginStep, body: Record<string, string>): Promise<GuestIdentityReply>;
  token(role: string, vaultToken: string): Promise<GuestIdentityReply>;
  discovery(): Promise<GuestIdentityReply>;
  keys(): Promise<GuestIdentityReply>;
  boundHostKey(computevm: string): Promise<string | undefined>;
  configureIssuer(ingressUrl: string): Promise<void>;
}

export interface GuestTokenClaims {
  aud: string;
  api: string;
  actx: string;
  computevm: string;
  claims: Record<string, unknown>;
}

export interface GuestTokenVerifier {
  verify(token: string): Promise<GuestTokenClaims>;
}

export class GuestTokenRefused extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = "GuestTokenRefused";
  }
}

export class TokenBuckets {
  #held = new Map<string, { tokens: number; at: number }>();

  constructor(
    readonly capacity: number,
    readonly refillPerSecond: number,
    readonly maxKeys = 4096,
  ) {}

  take(key: string, nowMs: number): boolean {
    const held = this.#held.get(key) ?? { tokens: this.capacity, at: nowMs };
    const earned = Math.max(0, nowMs - held.at) / 1000 * this.refillPerSecond;
    const tokens = Math.min(this.capacity, held.tokens + earned);
    this.#held.delete(key);
    if (this.#held.size >= this.maxKeys) {
      const oldest = this.#held.keys().next().value;
      if (oldest !== undefined) this.#held.delete(oldest);
    }
    if (tokens < 1) {
      this.#held.set(key, { tokens, at: nowMs });
      return false;
    }
    this.#held.set(key, { tokens: tokens - 1, at: nowMs });
    return true;
  }
}

export interface GuestGrantWish {
  api: string;
  actx: string;
  scopes: string[];
  claims: { name: string; value: string }[];
}

export interface RbacRecordLike {
  roles?: Record<string, { definition: { iss?: string; aud?: string; sub: string; policies?: string[] } }>;
}

export function grantWishesFromRbac(
  records: readonly RbacRecordLike[],
  opts: { issuer: string; subject: string },
): GuestGrantWish[] {
  const byAudience = new Map<string, GuestGrantWish>();
  for (const record of records) {
    for (const role of Object.values(record.roles ?? {})) {
      const def = role?.definition;
      if (!def || def.sub !== opts.subject) continue;
      if (def.iss && def.iss !== opts.issuer) continue;
      const parsed = typeof def.aud === "string" ? parseGuestAudience(def.aud) : null;
      if (!parsed) continue;
      const key = guestAudience(parsed.api, parsed.actx);
      const held = byAudience.get(key) ?? {
        api: parsed.api,
        actx: parsed.actx,
        scopes: [],
        claims: [{ name: GUEST_OIDC_SUBJECT_CLAIM, value: opts.subject }],
      };
      for (const policy of def.policies ?? []) if (!held.scopes.includes(policy)) held.scopes.push(policy);
      byAudience.set(key, held);
    }
  }
  return [...byAudience.values()].sort((a, b) => guestAudience(a.api, a.actx).localeCompare(guestAudience(b.api, b.actx)));
}
