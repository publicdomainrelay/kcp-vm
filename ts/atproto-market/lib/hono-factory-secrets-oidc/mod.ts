import { Hono } from "@hono/hono";
import { SECRETS_ROUTE } from "@publicdomainrelay/secrets-common";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import type { SecretsAuthorizer } from "@publicdomainrelay/secrets-oidc";

export interface SecretsFactoryOptions {
  authorizer: SecretsAuthorizer;
  getSecrets: () => SecretEntry[];
  route?: string;
  log?: (event: string, extra?: Record<string, unknown>) => void;
}

export function extractBearer(authHeader: string | undefined): string {
  if (!authHeader) return "";
  const [scheme, value] = authHeader.split(" ");
  return scheme?.toLowerCase() === "bearer" ? (value ?? "") : "";
}

export function createSecretsApp(opts: SecretsFactoryOptions): Hono {
  const route = opts.route ?? SECRETS_ROUTE;
  const log = opts.log ?? (() => {});
  const app = new Hono();

  app.get(route, async (c) => {
    const token = extractBearer(c.req.header("Authorization"));
    try {
      const { sub } = await opts.authorizer.authorize(token, route, c.req.method);
      const secrets = opts.getSecrets();
      log("secrets_served", { sub, count: secrets.length });
      return c.json(secrets);
    } catch (err) {
      log("secrets_denied", { error: String(err) });
      return c.json({ id: "unauthorized", message: String(err) }, 401);
    }
  });

  return app;
}
