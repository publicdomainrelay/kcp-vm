import type { Hono } from "@hono/hono";
import type { ContentfulStatusCode } from "@hono/hono/utils/http-status";
import { HTTPError } from "@publicdomainrelay/http-error";
import type { LoggerInterface } from "@publicdomainrelay/logger";

function httpStatusOf(err: unknown): number | undefined {
  const status = (err as { status?: unknown })?.status;
  return typeof status === "number" && status >= 400 && status < 600 ? status : undefined;
}

export function registerErrorMiddleware(app: Hono, logger?: LoggerInterface): void {
  app.onError((err, c) => {
    if (err instanceof HTTPError) {
      return c.json(
        { error: "http_error", code: err.status, detail: err.detail },
        err.status as ContentfulStatusCode,
      );
    }
    const status = httpStatusOf(err);
    if (status !== undefined) {
      return c.json(
        { error: "http_error", code: status, detail: (err as Error).message },
        status as ContentfulStatusCode,
      );
    }
    if (logger) {
      logger.error((err as Error).stack ?? String(err), { component: "error-middleware" });
    } else {
      console.error("[err]", (err as Error).stack ?? err);
    }
    return c.json({ error: "internal", detail: (err as Error).message }, 500);
  });
}
