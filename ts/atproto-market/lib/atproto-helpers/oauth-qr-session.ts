// OAuth QR session persistence — save/reload sessions across CLI restarts.
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { AtprotoAgentLike, OAuthSessionData } from "./agent.ts";
import { createOAuthAgentFromSession } from "./agent.ts";

export type { OAuthSessionData };

function defaultSessionPath(label?: string): string {
  const home = (() => { try { return Deno.env.get("HOME") || Deno.env.get("USERPROFILE") || "/tmp"; } catch { return "/tmp"; } })();
  const name = label ? `oauth-qr-session-${label}.json` : "oauth-qr-session.json";
  return `${home}/.cache/pdr-market/${name}`;
}

/** Try to restore a previously saved OAuth QR session. Returns agent if valid, null if expired or missing. */
export async function tryRestoreOAuthQRSession(opts: {
  logger?: StructuredLoggerInterface;
  sessionPath?: string;
  label?: string;
}): Promise<(AtprotoAgentLike & { sessionData: OAuthSessionData }) | null> {
  const path = opts.sessionPath ?? defaultSessionPath(opts.label);
  let data: OAuthSessionData;
  try { data = JSON.parse(await Deno.readTextFile(path)) as OAuthSessionData; } catch { return null; }
  if (!data?.accessJwt) return null;
  try {
    const agent = await createOAuthAgentFromSession(data, opts);
    // Validate by calling listRecords on the PDS
    const info = await agent.listRecords(data.userDid, "com.publicdomainrelay.temp.badgeBlueKeys", { limit: 1 });
    if (!info || !("records" in info)) throw new Error("session validation failed");
    opts.logger?.info("oauth_qr_session_restored", { userDid: data.userDid, handle: data.handle });
    return agent;
  } catch {
    opts.logger?.warn("oauth_qr_session_restore_failed", { userDid: data.userDid });
    try { await Deno.remove(path); } catch { /* ignore */ }
    return null;
  }
}

/** Save an OAuth QR session to disk. Called after a successful transfer. */
export async function saveOAuthQRSession(
  session: OAuthSessionData,
  opts?: { sessionPath?: string; label?: string },
): Promise<void> {
  const path = opts?.sessionPath ?? defaultSessionPath(opts?.label);
  const dir = path.split("/").slice(0, -1).join("/");
  try { await Deno.mkdir(dir, { recursive: true }); } catch { /* ignore */ }
  await Deno.writeTextFile(path, JSON.stringify(session, null, 2));
}
