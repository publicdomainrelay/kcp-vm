import * as jose from "jose";
import { checkRBACPolicy } from "@publicdomainrelay/rbac-atproto";
import { UnauthorizedException } from "@publicdomainrelay/oidc-issuer-abc";
import type { RbacRecordShape } from "@publicdomainrelay/secrets-common";

export interface SecretsGrant {
  rbac: RbacRecordShape;
  issuerUri: string;
  expectedAud: string;
}

export interface AuthorizedRequest {
  sub: string;
}

export interface SecretsAuthorizer {
  install(grant: SecretsGrant): void;
  revoke(): void;
  readonly granted: boolean;
  authorize(token: string, path: string, method: string): Promise<AuthorizedRequest>;
}

export interface CreateSecretsAuthorizerOpts {
  clockToleranceSec?: number;
  fetch?: typeof fetch;
}

/**
 * Validates a guest's exchanged workload identity token against an in-memory
 * grant. The requester is not an OIDC issuer, so verification goes through the
 * provider's published JWKS -- never through oidc-issuer-hono's OIDCToken.validate,
 * which requires configureOidc() and would generate a signing key we never use.
 */
export function createSecretsAuthorizer(
  opts: CreateSecretsAuthorizerOpts = {},
): SecretsAuthorizer {
  const doFetch = opts.fetch ?? globalThis.fetch;
  const clockTolerance = opts.clockToleranceSec ?? 5;
  const jwksCache = new Map<string, jose.JWTVerifyGetKey>();
  let grant: SecretsGrant | null = null;

  /**
   * Resolve the issuer's signing keys through the injected fetch. jose's
   * createRemoteJWKSet does its own network calls, which would bypass any fetch
   * the caller patched in (local dispatchers, tests), so discovery and the JWKS
   * are fetched here and turned into a local key set.
   */
  async function getJwks(issuerUri: string, reload = false): Promise<jose.JWTVerifyGetKey> {
    if (!reload) {
      const cached = jwksCache.get(issuerUri);
      if (cached) return cached;
    }
    const base = issuerUri.replace(/\/$/, "");
    const discoveryUrl = `${base}/.well-known/openid-configuration`;
    const confRes = await doFetch(discoveryUrl);
    if (!confRes.ok) {
      throw new UnauthorizedException(`issuer discovery failed ${discoveryUrl}: ${confRes.status}`);
    }
    const conf = await confRes.json() as { jwks_uri?: string };
    if (!conf.jwks_uri) {
      throw new UnauthorizedException(`issuer ${issuerUri} published no jwks_uri`);
    }
    const jwksRes = await doFetch(conf.jwks_uri);
    if (!jwksRes.ok) {
      throw new UnauthorizedException(`jwks fetch failed ${conf.jwks_uri}: ${jwksRes.status}`);
    }
    const set = jose.createLocalJWKSet(await jwksRes.json() as jose.JSONWebKeySet);
    jwksCache.set(issuerUri, set);
    return set;
  }

  return {
    install(next: SecretsGrant): void {
      grant = next;
    },
    revoke(): void {
      grant = null;
    },
    get granted(): boolean {
      return grant !== null;
    },
    async authorize(token: string, path: string, method: string): Promise<AuthorizedRequest> {
      const active = grant;
      if (!active) throw new UnauthorizedException("no active secrets grant");
      if (!token) throw new UnauthorizedException("missing bearer token");

      let unverified: jose.JWTPayload;
      try {
        unverified = jose.decodeJwt(token);
      } catch (err) {
        throw new UnauthorizedException(`malformed token: ${String(err)}`);
      }
      const aud = Array.isArray(unverified.aud) ? unverified.aud[0] : unverified.aud;
      if (aud !== active.expectedAud) {
        throw new UnauthorizedException(
          `audience mismatch: got ${String(aud)} want ${active.expectedAud}`,
        );
      }

      const verify = async (jwks: jose.JWTVerifyGetKey) =>
        (await jose.jwtVerify(token, jwks, {
          issuer: active.issuerUri,
          audience: active.expectedAud,
          clockTolerance,
        })).payload;

      let payload: jose.JWTPayload;
      try {
        payload = await verify(await getJwks(active.issuerUri));
      } catch (err) {
        if (err instanceof UnauthorizedException) throw err;
        // A key we have never seen means the issuer rotated; reload once before
        // treating the token as invalid.
        if (!(err instanceof jose.errors.JWKSNoMatchingKey)) {
          throw new UnauthorizedException(`token verification failed: ${String(err)}`);
        }
        try {
          payload = await verify(await getJwks(active.issuerUri, true));
        } catch (retryErr) {
          if (retryErr instanceof UnauthorizedException) throw retryErr;
          throw new UnauthorizedException(`token verification failed: ${String(retryErr)}`);
        }
      }

      const sub = payload.sub;
      if (!sub) throw new UnauthorizedException("token has no sub");

      checkRBACPolicy(active.rbac as never, sub, path, method);
      return { sub };
    },
  };
}
