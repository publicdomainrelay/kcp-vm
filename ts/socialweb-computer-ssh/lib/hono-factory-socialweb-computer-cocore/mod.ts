import { createFactory } from "@hono/hono/factory";
import type { Context } from "@hono/hono";
import type { CocorePairing, WebCaller, WebRequestAuth } from "@publicdomainrelay/socialweb-computer-abc";

export interface CocorePairFactoryOptions {
  pairing: CocorePairing;
  auth: WebRequestAuth;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export function createCocorePairFactory(opts: CocorePairFactoryOptions) {
  const log = opts.log ?? (() => {});

  type Authenticated = { ok: true; caller: WebCaller } | { ok: false; response: Response };

  async function authenticated(c: Context): Promise<Authenticated> {
    let caller: WebCaller | null;
    try {
      caller = await opts.auth.authenticate(c.req);
    } catch (err) {
      log("cocore_identify_failed", { error: String(err) });
      return { ok: false, response: c.json({ error: "the vault is not answering" }, 503) };
    }
    if (!caller) return { ok: false, response: c.json({ error: "Unauthorized" }, 401) };
    return { ok: true, caller };
  }

  return createFactory({
    initApp: (app) => {
      app.post("/cocore/status", async (c) => {
        const check = await authenticated(c);
        if (!check.ok) return check.response;
        const token = await opts.pairing.token(check.caller.did);
        return c.json({
          paired: token !== undefined,
          accountDid: token?.accountDid ?? null,
          pairedAt: token?.pairedAt ?? null,
        });
      });

      app.post("/cocore/pair/start", async (c) => {
        const check = await authenticated(c);
        if (!check.ok) return check.response;
        try {
          const started = await opts.pairing.start(check.caller.did);
          return c.json(started);
        } catch (err) {
          log("cocore_pairing_start_failed", { did: check.caller.did, error: String(err) });
          return c.json({ error: String(err instanceof Error ? err.message : err) }, 502);
        }
      });

      app.post("/cocore/pair/poll", async (c) => {
        const body = await c.req.json().catch(() => null) as { pairId?: unknown } | null;
        const pairId = typeof body?.pairId === "string" ? body.pairId : "";
        if (!pairId) return c.json({ error: "expected a pairId" }, 400);
        return c.json({ status: await opts.pairing.poll(pairId) });
      });
    },
  });
}
