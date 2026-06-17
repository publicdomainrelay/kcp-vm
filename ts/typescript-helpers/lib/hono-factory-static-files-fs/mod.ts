import { Hono } from "@hono/hono";
import { serveStatic } from "@hono/hono/deno";
import { registerErrorMiddleware } from "@publicdomainrelay/hono-error-middleware";
import type { LoggerInterface } from "@publicdomainrelay/logger";
import { EventBus } from "@publicdomainrelay/event-bus";
import { HTTPError } from "@publicdomainrelay/http-error";

export interface StaticFileEvent {
  type: "file-served" | "file-not-found";
  path: string;
}

export function createStaticFilesApp(
  servePath: string,
  logger: LoggerInterface,
  bus: EventBus<StaticFileEvent>,
): Hono {
  const app = new Hono();

  registerErrorMiddleware(app, logger);

  app.use("*", async (c, next) => {
    logger.info(`${c.req.method} ${c.req.path}`);
    await next();
    bus.publish({ type: "file-served", path: c.req.path });
  });

  app.use("*", serveStatic({ root: servePath }));

  app.notFound((c) => {
    const path = c.req.path;
    bus.publish({ type: "file-not-found", path });
    throw new HTTPError(404, path);
  });

  return app;
}
