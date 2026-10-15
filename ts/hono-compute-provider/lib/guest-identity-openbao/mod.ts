import * as jose from "jose";
import {
  type GuestIdentity,
  type GuestIdentityReply,
  type GuestLoginStep,
  type GuestTokenClaims,
  GuestTokenRefused,
  type GuestTokenVerifier,
} from "@publicdomainrelay/guest-identity-abc";
import { GUEST_KCP_API, guestAudience, parseGuestAudience } from "@publicdomainrelay/guest-identity-common";

export interface OpenBaoGuestIdentityEndpoint {
  address: string;
  caCert?: string;
  clientCert?: string;
  clientKey?: string;
  certLogin?: { mount: string; role: string };
}

export interface OpenBaoGuestIdentityOptions {
  endpoint: () => Promise<OpenBaoGuestIdentityEndpoint>;
  mount: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 10_000;
const MOUNT = /^[a-z0-9][a-z0-9-]{0,62}$/;
const SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,250}$/;
const OWN_TOKEN_RENEW_BEFORE_MS = 60_000;

export function createOpenBaoGuestIdentity(opts: OpenBaoGuestIdentityOptions): GuestIdentity {
  if (!MOUNT.test(opts.mount)) throw new Error(`guest identity: ${JSON.stringify(opts.mount)} is not an auth mount name`);
  const baseFetch = opts.fetch ?? fetch;
  let held: { key: string; client?: Deno.HttpClient } | null = null;
  let own: { token: string; renewAt: number } | null = null;

  function clientFor(e: OpenBaoGuestIdentityEndpoint): Deno.HttpClient | undefined {
    if (opts.fetch) return undefined;
    const key = [e.caCert, e.clientCert, e.clientKey].join("\n");
    if (held?.key === key) return held.client;
    held?.client?.close();
    own = null;
    const client = e.caCert || (e.clientCert && e.clientKey)
      ? Deno.createHttpClient({
        ...(e.caCert ? { caCerts: [e.caCert] } : {}),
        ...(e.clientCert && e.clientKey ? { cert: e.clientCert, key: e.clientKey } : {}),
      })
      : undefined;
    held = { key, client };
    return client;
  }

  async function send(
    method: string,
    path: string,
    init: { vaultToken?: string; body?: unknown } = {},
  ): Promise<GuestIdentityReply & { endpoint: OpenBaoGuestIdentityEndpoint }> {
    const e = await opts.endpoint();
    const client = clientFor(e);
    const headers = new Headers({ accept: "application/json" });
    if (init.body !== undefined) headers.set("content-type", "application/json");
    if (init.vaultToken) headers.set("x-vault-token", init.vaultToken);
    const res = await baseFetch(`${e.address.replace(/\/+$/, "")}/v1/${path}`, {
      method,
      headers,
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(client ? { client } : {}),
    } as RequestInit);
    const text = await res.text();
    let parsed: unknown = null;
    try {
      parsed = text ? JSON.parse(text) : null;
    } catch {
      parsed = { errors: [text.slice(0, 300)] };
    }
    return { status: res.status, body: parsed, endpoint: e };
  }

  async function ownToken(): Promise<string> {
    if (own && Date.now() < own.renewAt) return own.token;
    const probe = await opts.endpoint();
    const login = probe.certLogin;
    if (!login) throw new Error("guest identity: the endpoint names no cert login for this process's own token");
    const res = await send("POST", `auth/${login.mount}/login`, { body: { name: login.role } });
    const auth = (res.body as { auth?: { client_token?: string; lease_duration?: number } } | null)?.auth;
    if (res.status !== 200 || !auth?.client_token) {
      throw new Error(`guest identity: cert login as ${login.role}: ${res.status} ${JSON.stringify(res.body).slice(0, 200)}`);
    }
    const leaseMs = (auth.lease_duration ?? 0) * 1000;
    own = { token: auth.client_token, renewAt: Date.now() + Math.max(leaseMs - OWN_TOKEN_RENEW_BEFORE_MS, leaseMs / 2) };
    return own.token;
  }

  async function authed(method: string, path: string, body?: unknown): Promise<GuestIdentityReply> {
    for (let attempt = 0;; attempt++) {
      const reply = await send(method, path, { vaultToken: await ownToken(), body });
      if (reply.status === 403 && attempt === 0) {
        own = null;
        continue;
      }
      return { status: reply.status, body: reply.body };
    }
  }

  return {
    async step(step: GuestLoginStep, body: Record<string, string>) {
      const { status, body: out } = await send("POST", `auth/${opts.mount}/${step}`, { body });
      return { status, body: out };
    },
    async token(role: string, vaultToken: string) {
      if (!SEGMENT.test(role)) return { status: 400, body: { errors: ["role is not a role name"] } };
      const { status, body } = await send("GET", `identity/oidc/token/${role}`, { vaultToken });
      return { status, body };
    },
    async discovery() {
      const { status, body } = await send("GET", "identity/oidc/.well-known/openid-configuration");
      return { status, body };
    },
    async keys() {
      const { status, body } = await send("GET", "identity/oidc/.well-known/keys");
      return { status, body };
    },
    async boundHostKey(computevm: string) {
      if (!SEGMENT.test(computevm)) return undefined;
      const reply = await authed("GET", `auth/${opts.mount}/role/${computevm}`);
      if (reply.status !== 200) return undefined;
      const data = (reply.body as { data?: { host_key?: unknown; bound?: unknown } } | null)?.data;
      return data?.bound === true && typeof data.host_key === "string" && data.host_key ? data.host_key : undefined;
    },
    async configureIssuer(ingressUrl: string) {
      const issuer = ingressUrl.replace(/\/+$/, "");
      const held = await authed("GET", "identity/oidc/config");
      const current = (held.body as { data?: { issuer?: unknown } } | null)?.data?.issuer;
      if (held.status === 200 && current === issuer) return;
      const wrote = await authed("PUT", "identity/oidc/config", { issuer });
      if (wrote.status !== 200 && wrote.status !== 204) {
        throw new Error(`guest identity: setting the OIDC issuer: ${wrote.status} ${JSON.stringify(wrote.body).slice(0, 200)}`);
      }
    },
  };
}

export interface OpenBaoTokenVerifierOptions {
  identity: GuestIdentity;
  issuer: () => string;
  clockToleranceSec?: number;
}

export function createOpenBaoTokenVerifier(opts: OpenBaoTokenVerifierOptions): GuestTokenVerifier {
  let jwks: jose.JWTVerifyGetKey | null = null;

  async function load(): Promise<jose.JWTVerifyGetKey> {
    const reply = await opts.identity.keys();
    const keys = (reply.body as { keys?: unknown } | null)?.keys;
    if (reply.status !== 200 || !Array.isArray(keys)) throw new GuestTokenRefused(`the issuer's keys are not readable: ${reply.status}`);
    jwks = jose.createLocalJWKSet({ keys: keys as jose.JWK[] });
    return jwks;
  }

  async function check(token: string, audience: string, keySet: jose.JWTVerifyGetKey) {
    return (await jose.jwtVerify(token, keySet, {
      issuer: opts.issuer(),
      audience,
      clockTolerance: opts.clockToleranceSec ?? 5,
    })).payload;
  }

  return {
    async verify(token: string): Promise<GuestTokenClaims> {
      let unverified: jose.JWTPayload;
      try {
        unverified = jose.decodeJwt(token);
      } catch (err) {
        throw new GuestTokenRefused(`malformed token: ${String(err)}`);
      }
      const rawAud = Array.isArray(unverified.aud) ? unverified.aud[0] : unverified.aud;
      const parsed = typeof rawAud === "string" ? parseGuestAudience(rawAud) : null;
      if (!rawAud || !parsed) throw new GuestTokenRefused("the token's audience is not api://<api>?actx=<context>");
      if (parsed.api !== GUEST_KCP_API) throw new GuestTokenRefused(`the token's audience is not for ${GUEST_KCP_API}`);
      const audience = guestAudience(parsed.api, parsed.actx);
      let payload: jose.JWTPayload;
      try {
        payload = await check(token, audience, jwks ?? await load());
      } catch (err) {
        if (!(err instanceof jose.errors.JWKSNoMatchingKey)) throw new GuestTokenRefused(`token verification failed: ${String(err)}`);
        try {
          payload = await check(token, audience, await load());
        } catch (retry) {
          throw new GuestTokenRefused(`token verification failed: ${String(retry)}`);
        }
      }
      const computevm = (payload as Record<string, unknown>)["computevm"];
      if (typeof computevm !== "string" || computevm !== parsed.actx) {
        throw new GuestTokenRefused("the token's computevm claim is not the audience's context");
      }
      return { aud: audience, api: parsed.api, actx: parsed.actx, computevm, claims: payload as Record<string, unknown> };
    },
  };
}

