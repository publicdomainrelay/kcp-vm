import type { ContentfulStatusCode } from "@hono/hono/utils/http-status";
import type { LoggerInterface } from "@publicdomainrelay/logger";

function httpStatusOf(err: unknown): number | undefined {
  const status = (err as { status?: unknown })?.status;
  return typeof status === "number" && status >= 400 && status < 600 ? status : undefined;
}

export function registerErrorMiddleware(
  app: { onError(handler: (err: unknown, c: { json(data: unknown, status?: number): unknown }) => unknown): void },
  logger: LoggerInterface,
): void {
  app.onError((err: unknown, c) => {
    const toJson = (err as { toJSON?: () => Record<string, unknown> }).toJSON;
    if (typeof toJson === "function") {
      return c.json(toJson.call(err), ((err as { status?: number }).status ?? 500) as ContentfulStatusCode);
    }
    const status = httpStatusOf(err);
    if (status !== undefined) {
      return c.json(
        { error: "http_error", code: status, detail: (err as Error).message },
        status as ContentfulStatusCode,
      );
    }
    logger.error((err as Error).stack ?? String(err), { component: "error-middleware" });
    return c.json({ error: "internal", detail: (err as Error).message }, 500);
  });
}
