import type { MiddlewareHandler } from "@hono/hono";

const WRITE_PATH = /\/xrpc\/com\.atproto\.repo\.(createRecord|putRecord|deleteRecord|applyWrites)$/;

const POINTS: Readonly<Record<string, number>> = { create: 3, update: 2, delete: 1 };

const SINGLE_ACTION: Readonly<Record<string, string>> = {
  createRecord: "create",
  putRecord: "update",
  deleteRecord: "delete",
};

export interface PdsWriteLimitFileOptions {
  path: string;
  now?: () => number;
}

interface LimitWindow {
  raw: string;
  points: number;
  windowMs: number;
  startedAt: number;
  used: number;
}

function costOf(nsid: string, body: unknown): number {
  const single = SINGLE_ACTION[nsid];
  if (single) return POINTS[single];
  const writes = (body as { writes?: Array<{ $type?: unknown }> } | null)?.writes;
  if (!Array.isArray(writes)) return 0;
  return writes.reduce((sum, op) => sum + (POINTS[String(op.$type).split("#").pop() ?? ""] ?? 0), 0);
}

export function pdsWriteLimitFile(opts: PdsWriteLimitFileOptions): MiddlewareHandler {
  const now = opts.now ?? Date.now;
  let window: LimitWindow | undefined;
  return async (c, next) => {
    const match = c.req.method === "POST" ? WRITE_PATH.exec(c.req.path) : null;
    if (!match) return await next();
    let raw: string;
    try {
      raw = await Deno.readTextFile(opts.path);
    } catch {
      window = undefined;
      return await next();
    }
    const at = now();
    if (window?.raw !== raw) {
      const parsed = JSON.parse(raw) as { points: number; windowSec: number };
      window = { raw, points: parsed.points, windowMs: parsed.windowSec * 1000, startedAt: at, used: 0 };
    } else if (at - window.startedAt >= window.windowMs) {
      window.startedAt = at;
      window.used = 0;
    }
    const cost = costOf(match[1], await c.req.raw.clone().json().catch(() => null));
    const resetAtMs = window.startedAt + window.windowMs;
    const limited = window.used + cost > window.points;
    if (!limited) window.used += cost;
    const headers: Record<string, string> = {
      "ratelimit-limit": String(window.points),
      "ratelimit-remaining": String(Math.max(0, window.points - window.used)),
      "ratelimit-reset": String(Math.ceil(resetAtMs / 1000)),
      "ratelimit-policy": `${window.points};w=${window.windowMs / 1000}`,
    };
    if (limited) {
      return c.json(
        { error: "RateLimitExceeded", message: "Rate Limit Exceeded" },
        429,
        { ...headers, "retry-after": String(Math.max(1, Math.ceil((resetAtMs - at) / 1000))) },
      );
    }
    for (const [name, value] of Object.entries(headers)) c.header(name, value);
    await next();
  };
}
