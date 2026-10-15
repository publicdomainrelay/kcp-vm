import { createFactory } from "@hono/hono/factory";
import type { Context } from "@hono/hono";
import type { WebCaller, WebRequestAuth, WebSignIn } from "@publicdomainrelay/socialweb-computer-abc";
import { BADGE_BLUE_KEYS_NSID, REQUESTER_ASSOCIATE_SERVICE } from "@publicdomainrelay/socialweb-computer-common";

export interface KeysFactoryOptions {
  signIn: Pick<WebSignIn, "agentFor">;
  auth: WebRequestAuth;
  maxBodyBytes?: number;
  maxKeys?: number;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

const DEFAULT_MAX_BODY = 8 * 1024;
const DEFAULT_MAX_KEYS = 100;
const KEY_ALGORITHMS =
  /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)$/;
const TID_ALPHABET = "234567abcdefghijklmnopqrstuvwxyz";

function tid(): string {
  let n = BigInt(Date.now()) * 1000n + BigInt(Math.floor(Math.random() * 1000));
  let out = "";
  for (let i = 0; i < 13; i++) {
    out = TID_ALPHABET[Number(n & 31n)] + out;
    n >>= 5n;
  }
  return out;
}

function parsePublicKey(text: string): { algo: string; key: string; comment: string } | null {
  const parts = text.trim().split(/\s+/);
  if (parts.length < 2) return null;
  if (!KEY_ALGORITHMS.test(parts[0])) return null;
  if (!/^[A-Za-z0-9+/]+=*$/.test(parts[1])) return null;
  return { algo: parts[0], key: parts[1], comment: parts.slice(2).join(" ") };
}

export function createKeysFactory(opts: KeysFactoryOptions) {
  const maxBodyBytes = opts.maxBodyBytes ?? DEFAULT_MAX_BODY;
  const maxKeys = opts.maxKeys ?? DEFAULT_MAX_KEYS;
  const log = opts.log ?? (() => {});

  async function caller(c: Context): Promise<WebCaller | Response> {
    try {
      const who = await opts.auth.authenticate(c.req);
      if (!who) return c.json({ error: "Unauthorized", message: "sign in again" }, 401);
      return who;
    } catch (err) {
      log("keys_identify_failed", { error: String(err) });
      return c.json({ error: "the vault is not answering" }, 503);
    }
  }

  function failed(c: Context, did: string, action: string, err: unknown): Response {
    log("keys_failed", { did, action, error: String(err) });
    return c.json({ error: `could not ${action}: ${err instanceof Error ? err.message : String(err)}` }, 502);
  }

  return createFactory({
    initApp: (app) => {
      app.get("/keys", async (c) => {
        const who = await caller(c);
        if (who instanceof Response) return who;
        try {
          const found = await opts.signIn.agentFor(who).listRecords(who.did, BADGE_BLUE_KEYS_NSID, { limit: maxKeys });
          const keys = found.records
            .filter((r) => r.value.service === REQUESTER_ASSOCIATE_SERVICE && r.value.challenge === who.did)
            .map((r) => ({
              rkey: r.uri.split("/").pop() ?? "",
              name: typeof r.value.name === "string" && r.value.name ? r.value.name : "unnamed",
              keyId: String(r.value.keyId ?? ""),
            }));
          return c.json({ keys }, 200, { "cache-control": "no-store" });
        } catch (err) {
          return failed(c, who.did, "list keys", err);
        }
      });

      app.post("/keys", async (c) => {
        const who = await caller(c);
        if (who instanceof Response) return who;
        const declared = Number(c.req.header("content-length") ?? "0");
        if (Number.isFinite(declared) && declared > maxBodyBytes) return c.json({ error: "body too large" }, 413);
        const body = await c.req.json().catch(() => null) as { publicKey?: unknown; name?: unknown } | null;
        const parsed = typeof body?.publicKey === "string" ? parsePublicKey(body.publicKey) : null;
        if (!parsed) return c.json({ error: "that does not look like an OpenSSH public key" }, 400);
        const name = typeof body?.name === "string" && body.name.trim() ? body.name.trim().slice(0, 128) : parsed.comment || "key";
        const rkey = tid();
        try {
          await opts.signIn.agentFor(who).createRecord(who.did, BADGE_BLUE_KEYS_NSID, rkey, {
            $type: BADGE_BLUE_KEYS_NSID,
            keyId: `${parsed.algo} ${parsed.key}`,
            name,
            challenge: who.did,
            service: REQUESTER_ASSOCIATE_SERVICE,
            createdAt: new Date().toISOString(),
          });
          log("keys_registered", { did: who.did, rkey });
          return c.json({ rkey });
        } catch (err) {
          return failed(c, who.did, "register the key", err);
        }
      });

      app.delete("/keys/:rkey", async (c) => {
        const who = await caller(c);
        if (who instanceof Response) return who;
        const rkey = c.req.param("rkey");
        if (!/^[A-Za-z0-9._~-]{1,512}$/.test(rkey)) return c.json({ error: "invalid key id" }, 400);
        try {
          await opts.signIn.agentFor(who).applyWrites(who.did, [{ action: "delete", collection: BADGE_BLUE_KEYS_NSID, rkey }]);
          log("keys_removed", { did: who.did, rkey });
          return c.json({ rkey });
        } catch (err) {
          return failed(c, who.did, "remove the key", err);
        }
      });
    },
  });
}
