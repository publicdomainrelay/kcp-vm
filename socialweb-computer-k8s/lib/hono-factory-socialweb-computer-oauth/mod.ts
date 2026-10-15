import { createFactory } from "@hono/hono/factory";
import type { Context } from "@hono/hono";
import type { WebCaller, WebRequestAuth, WebRequestLike, WebSignedInUser, WebSignIn } from "@publicdomainrelay/socialweb-computer-abc";
import { COCORE_APP_WELL_KNOWN_PATH } from "@publicdomainrelay/socialweb-computer-common";

export const WEB_SESSION_COOKIE = "swc_session";
export const WEB_OAUTH_STATE_COOKIE = "swc_oauth_state";
export const WEB_OAUTH_CALLBACK_PATH = "/oauth/callback";

const DEFAULT_COOKIE_MAX_AGE_SEC = 2_592_000;
const DEFAULT_CLIENT_METADATA_CACHE_MS = 60_000;
const OAUTH_STATE_MAX_AGE_SEC = 600;
const DEFAULT_MAX_BODY = 4 * 1024;
const MAX_IDENTIFIER_LENGTH = 256;
const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export function cookieValue(header: string | undefined, name: string): string | undefined {
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() === name) return part.slice(eq + 1).trim();
  }
  return undefined;
}

interface CookieAttributes {
  maxAge: number;
  sameSite: "Strict" | "Lax";
  path: string;
  secure: boolean;
}

function serializeCookie(name: string, value: string, attrs: CookieAttributes): string {
  return `${name}=${value}; Path=${attrs.path}; Max-Age=${attrs.maxAge}; HttpOnly; SameSite=${attrs.sameSite}${attrs.secure ? "; Secure" : ""}`;
}

export interface WebRequestAuthOptions {
  signIn: WebSignIn;
  publicOrigin?: string;
  cookieName?: string;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export function createWebRequestAuth(opts: WebRequestAuthOptions): WebRequestAuth {
  const cookieName = opts.cookieName ?? WEB_SESSION_COOKIE;
  const expectedOrigin = opts.publicOrigin?.replace(/\/+$/, "");
  const log = opts.log ?? (() => {});

  function originAllowed(request: WebRequestLike): boolean {
    if (SAFE_METHODS.has(request.method.toUpperCase())) return true;
    const origin = request.header("origin");
    if (!origin) return false;
    if (expectedOrigin) return origin === expectedOrigin;
    try {
      return new URL(origin).host === request.header("host");
    } catch {
      return false;
    }
  }

  return {
    async authenticate(request): Promise<WebCaller | null> {
      const token = cookieValue(request.header("cookie"), cookieName);
      if (!token) return null;
      if (!originAllowed(request)) {
        log("web_origin_refused", { method: request.method, origin: request.header("origin") ?? null });
        return null;
      }
      const user = await opts.signIn.identify(token);
      return user ? { ...user, token } : null;
    },
  };
}

export interface WebFactoryOptions {
  signIn: WebSignIn;
  auth: WebRequestAuth;
  publicOrigin?: string;
  sshPublicHost?: string;
  appDid?: string;
  cookieName?: string;
  cookieMaxAgeSec?: number;
  clientMetadataPath?: string;
  clientMetadataCacheMs?: number;
  maxBodyBytes?: number;
  onSignedIn?: (user: WebSignedInUser) => Promise<void> | void;
  onSignedOut?: (user: WebSignedInUser) => Promise<void> | void;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export function createWebFactory(opts: WebFactoryOptions) {
  const clientMetadataPath = opts.clientMetadataPath ?? "/oauth-client-metadata.json";
  const clientMetadataCacheMs = opts.clientMetadataCacheMs ?? DEFAULT_CLIENT_METADATA_CACHE_MS;
  const maxBodyBytes = opts.maxBodyBytes ?? DEFAULT_MAX_BODY;
  const cookieName = opts.cookieName ?? WEB_SESSION_COOKIE;
  const cookieMaxAge = opts.cookieMaxAgeSec ?? DEFAULT_COOKIE_MAX_AGE_SEC;
  const log = opts.log ?? (() => {});
  const publicOrigin = opts.publicOrigin?.replace(/\/+$/, "");
  const secure = publicOrigin?.startsWith("https://") ?? false;
  let metadata: { at: number; doc: Record<string, unknown> } | null = null;

  const sessionCookie = (value: string, maxAge: number) =>
    serializeCookie(cookieName, value, { maxAge, sameSite: "Strict", path: "/", secure });
  const stateCookie = (value: string, maxAge: number) =>
    serializeCookie(WEB_OAUTH_STATE_COOKIE, value, { maxAge, sameSite: "Lax", path: "/oauth", secure });

  function back(c: Context, error?: string): Response {
    const target = error ? `/?${new URLSearchParams({ error }).toString()}` : "/";
    const res = c.redirect(target, 302);
    res.headers.append("set-cookie", stateCookie("", 0));
    res.headers.set("cache-control", "no-store");
    return res;
  }

  return createFactory({
    initApp: (app) => {
      app.get("/healthz", (c) => c.json({ ok: true }));

      app.get("/connect.json", (c) => {
        const origin = publicOrigin ?? new URL(c.req.url).origin;
        return c.json({ sshHost: opts.sshPublicHost ?? new URL(origin).hostname });
      });

      app.get(COCORE_APP_WELL_KNOWN_PATH, (c) => {
        if (!opts.appDid) return c.notFound();
        return c.json({ did: opts.appDid });
      });

      app.get(clientMetadataPath, async (c) => {
        try {
          if (!metadata || Date.now() - metadata.at > clientMetadataCacheMs) {
            metadata = { at: Date.now(), doc: await opts.signIn.clientMetadata() };
          }
          return c.json(metadata.doc, 200, { "cache-control": `public, max-age=${Math.floor(clientMetadataCacheMs / 1000)}` });
        } catch (err) {
          log("client_metadata_unavailable", { error: String(err) });
          return c.json({ error: "client metadata is not available from the vault" }, 503);
        }
      });

      app.post("/oauth/start", async (c) => {
        if (!publicOrigin) return c.json({ error: "this deployment has no public-origin, so it cannot sign anyone in" }, 503);
        const declared = Number(c.req.header("content-length") ?? "0");
        if (Number.isFinite(declared) && declared > maxBodyBytes) return c.json({ error: "body too large" }, 413);
        const body = await c.req.json().catch(() => null) as { handle?: unknown } | null;
        const identifier = typeof body?.handle === "string" ? body.handle.trim().replace(/^@/, "") : "";
        if (!identifier || identifier.length > MAX_IDENTIFIER_LENGTH || /\s/.test(identifier)) {
          return c.json({ error: "expected a handle or a DID" }, 400);
        }
        try {
          const started = await opts.signIn.start(identifier, `${publicOrigin}${WEB_OAUTH_CALLBACK_PATH}`);
          log("web_sign_in_started", { identifier });
          return c.json({ authorizationUrl: started.authorizationUrl }, 200, {
            "set-cookie": stateCookie(started.state, OAUTH_STATE_MAX_AGE_SEC),
            "cache-control": "no-store",
          });
        } catch (err) {
          log("web_sign_in_start_failed", { identifier, error: String(err) });
          return c.json({ error: `could not start the sign-in: ${err instanceof Error ? err.message : String(err)}` }, 502);
        }
      });

      app.get(WEB_OAUTH_CALLBACK_PATH, async (c) => {
        const denied = c.req.query("error");
        if (denied) return back(c, c.req.query("error_description") ?? denied);
        const state = c.req.query("state") ?? "";
        const code = c.req.query("code") ?? "";
        if (!state || !code) return back(c, "the authorization server returned no code");
        if (cookieValue(c.req.header("cookie"), WEB_OAUTH_STATE_COOKIE) !== state) {
          log("web_sign_in_state_not_bound", {});
          return back(c, "this sign-in was not started in this browser; start it again");
        }
        try {
          const grant = await opts.signIn.complete({ state, code, iss: c.req.query("iss") ?? undefined });
          await opts.onSignedIn?.({ did: grant.did, handle: grant.handle, pds: grant.pds });
          log("web_signed_in", { did: grant.did, handle: grant.handle });
          const res = back(c);
          res.headers.append("set-cookie", sessionCookie(grant.token, cookieMaxAge));
          return res;
        } catch (err) {
          log("web_sign_in_failed", { error: String(err) });
          return back(c, `sign-in failed: ${err instanceof Error ? err.message : String(err)}`);
        }
      });

      app.get("/session", async (c) => {
        let caller: WebCaller | null;
        try {
          caller = await opts.auth.authenticate(c.req);
        } catch (err) {
          log("web_identify_failed", { error: String(err) });
          return c.json({ error: "the vault is not answering" }, 503);
        }
        if (!caller) return c.json({ error: "Unauthorized", message: "sign in" }, 401);
        return c.json({ did: caller.did, handle: caller.handle }, 200, { "cache-control": "no-store" });
      });

      app.delete("/session", async (c) => {
        let caller: WebCaller | null;
        try {
          caller = await opts.auth.authenticate(c.req);
        } catch (err) {
          log("web_identify_failed", { error: String(err) });
          return c.json({ error: "the vault is not answering" }, 503);
        }
        if (!caller) return c.json({ error: "Unauthorized", message: "sign in" }, 401, { "set-cookie": sessionCookie("", 0) });
        try {
          const result = await opts.signIn.signOut(caller);
          await opts.onSignedOut?.({ did: caller.did, handle: caller.handle, pds: caller.pds });
          log("web_signed_out", { did: caller.did, revoked: result.revoked, revokeError: result.revokeError ?? null });
          return c.json({ revoked: result.revoked, ...(result.revokeError ? { revokeError: result.revokeError } : {}) }, 200, {
            "set-cookie": sessionCookie("", 0),
          });
        } catch (err) {
          log("web_sign_out_failed", { did: caller.did, error: String(err) });
          return c.json({ error: "could not sign out" }, 502, { "set-cookie": sessionCookie("", 0) });
        }
      });
    },
  });
}
