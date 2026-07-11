// OAuth QR session persistence — save/reload sessions across CLI restarts.
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { AtprotoAgentLike, OAuthSessionData } from "./agent.ts";
import { createOAuthAgentFromSession, OAuthSessionExpiredError } from "./agent.ts";

export { OAuthSessionExpiredError };
export type { OAuthSessionData };

function defaultSessionPath(label?: string, handle?: string): string {
  const home = (() => { try { return Deno.env.get("HOME") || Deno.env.get("USERPROFILE") || "/tmp"; } catch { return "/tmp"; } })();
  const safe = (s: string) => s.replace(/[^a-zA-Z0-9_-]/g, "_");
  let name: string;
  if (label && handle) {
    name = `oauth-qr-session-${safe(label)}-${safe(handle)}.json`;
  } else if (label) {
    name = `oauth-qr-session-${label}.json`;
  } else {
    name = "oauth-qr-session.json";
  }
  return `${home}/.cache/pdr-market/${name}`;
}

/** Try to restore a previously saved OAuth QR session. Returns agent if valid, null if expired or missing. */
export async function tryRestoreOAuthQRSession(opts: {
  logger?: StructuredLoggerInterface;
  sessionPath?: string;
  label?: string;
  /** AT Protocol handle (e.g. "alice.bsky.social") — keys the cache file per account. */
  handle?: string;
  autoRefreshThresholdMs?: number;
  onSessionExpired?: (err: OAuthSessionExpiredError) => void;
}): Promise<(AtprotoAgentLike & { sessionData: OAuthSessionData; dispose(): void; proactiveRefresh(): Promise<void> }) | null> {
  const path = opts.sessionPath ?? defaultSessionPath(opts.label, opts.handle);
  let data: OAuthSessionData;
  try { data = JSON.parse(await Deno.readTextFile(path)) as OAuthSessionData; } catch { return null; }
  if (!data?.accessJwt) return null;
  try {
    const agent = await createOAuthAgentFromSession(data, {
      logger: opts.logger,
      sessionPath: path,
      autoRefreshThresholdMs: opts.autoRefreshThresholdMs,
      onSessionExpired: opts.onSessionExpired,
      saveSession: async (updatedSession: OAuthSessionData) => {
        await Deno.writeTextFile(path, JSON.stringify(updatedSession, null, 2));
      },
    });
    // Force a token refresh to verify the refresh token is still valid.
    // If the refresh token was already consumed (e.g. by a prior process),
    // this will throw OAuthSessionExpiredError, which we catch below to
    // delete the stale session file and trigger a fresh QR auth flow.
    await agent.proactiveRefresh();
    // Validate by calling listRecords on the PDS
    const info = await agent.listRecords(data.userDid, "com.publicdomainrelay.temp.badgeBlueKeys", { limit: 1 });
    if (!info || !("records" in info)) throw new Error("session validation failed");
    opts.logger?.info("oauth_qr_session_restored", { userDid: data.userDid, handle: data.handle });
    return agent;
  } catch (err) {
    if (err instanceof OAuthSessionExpiredError) {
      opts.logger?.warn("oauth_qr_session_expired_deleting", { path, userDid: data.userDid });
      try { await Deno.remove(path); } catch { /* ignore */ }
      return null;
    }
    opts.logger?.warn("oauth_qr_session_restore_failed", { userDid: data.userDid });
    try { await Deno.remove(path); } catch { /* ignore */ }
    return null;
  }
}

/** Save an OAuth QR session to disk. Called after a successful transfer. */
export async function saveOAuthQRSession(
  session: OAuthSessionData,
  opts?: { sessionPath?: string; label?: string; /** AT Protocol handle — keys the cache file per account. */ handle?: string },
): Promise<void> {
  const path = opts?.sessionPath ?? defaultSessionPath(opts?.label, opts?.handle);
  const dir = path.split("/").slice(0, -1).join("/");
  try { await Deno.mkdir(dir, { recursive: true }); } catch { /* ignore */ }
  await Deno.writeTextFile(path, JSON.stringify(session, null, 2));
}
