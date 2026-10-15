import { createFactory } from "@hono/hono/factory";
import type { Context } from "@hono/hono";
import { ClusterGatewayError } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import type { ClusterCaller, ClusterGateway, PinnedDownloads } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  isClusterName,
  kubeconfigFilename,
  ttlChoicesUpTo,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

export interface CredentialHelper {
  os: string;
  arch: string;
  path: string;
}

export interface ClusterApiRequest {
  method: string;
  header(name: string): string | undefined;
}

export interface ClusterApiFactoryOptions {
  gateway: ClusterGateway;
  caller: (request: ClusterApiRequest) => Promise<ClusterCaller | null>;
  maxKubeconfigTtlSec: number;
  kcpPublicHost?: string;
  credentialHelpers?: CredentialHelper[];
  maxBodyBytes?: number;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

const DEFAULT_MAX_BODY = 4 * 1024;

type NameAction = "create" | "up" | "down" | "delete";

export function createClusterApiFactory(opts: ClusterApiFactoryOptions) {
  const maxBodyBytes = opts.maxBodyBytes ?? DEFAULT_MAX_BODY;
  const log = opts.log ?? (() => {});
  const ttlChoices = ttlChoicesUpTo(opts.maxKubeconfigTtlSec);

  async function caller(c: Context): Promise<ClusterCaller | Response> {
    let who: ClusterCaller | null;
    try {
      who = await opts.caller(c.req);
    } catch (err) {
      log("cluster_api_auth_unavailable", { error: String(err) });
      return c.json({ error: "Unavailable", message: "the vault is not answering; try again" }, 503);
    }
    if (!who) return c.json({ error: "Unauthorized", message: "sign in again" }, 401);
    return who;
  }

  async function body(c: Context): Promise<Record<string, unknown> | Response> {
    const declared = Number(c.req.header("content-length") ?? "0");
    if (Number.isFinite(declared) && declared > maxBodyBytes) return c.json({ error: "BodyTooLarge" }, 413);
    const text = await c.req.text();
    if (text.length > maxBodyBytes) return c.json({ error: "BodyTooLarge" }, 413);
    if (!text) return {};
    try {
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
    } catch {
      return c.json({ error: "InvalidRequest", message: "expected a JSON object" }, 400);
    }
    return c.json({ error: "InvalidRequest", message: "expected a JSON object" }, 400);
  }

  function failure(c: Context, did: string, action: string, err: unknown): Response {
    if (err instanceof ClusterGatewayError) {
      log("cluster_api_gateway_error", { did, action, status: err.status, code: err.code, message: err.message });
      const status = err.status >= 400 && err.status < 600 ? err.status : 502;
      return c.json({ error: err.code, message: err.message }, status as 400);
    }
    log("cluster_api_failed", { did, action, error: String(err) });
    return c.json({ error: "InternalError", message: "the cluster request failed" }, 500);
  }

  async function named(c: Context): Promise<{ caller: ClusterCaller; name: string; input: Record<string, unknown> } | Response> {
    const who = await caller(c);
    if (who instanceof Response) return who;
    const input = await body(c);
    if (input instanceof Response) return input;
    if (!isClusterName(input.name)) {
      return c.json({ error: "InvalidRequest", message: "name must be a lowercase DNS label of at most 63 characters" }, 400);
    }
    return { caller: who, name: input.name, input };
  }

  function nameAction(action: NameAction) {
    return async (c: Context) => {
      const req = await named(c);
      if (req instanceof Response) return req;
      try {
        const out = action === "create"
          ? await opts.gateway.create(req.caller, req.name)
          : action === "up"
          ? await opts.gateway.up(req.caller, req.name)
          : action === "down"
          ? await opts.gateway.down(req.caller, req.name)
          : await opts.gateway.remove(req.caller, req.name);
        log("cluster_api_ok", { did: req.caller.did, action, name: req.name, phase: out.phase });
        return c.json({ name: req.name, phase: out.phase });
      } catch (err) {
        return failure(c, req.caller.did, action, err);
      }
    };
  }

  return createFactory({
    initApp: (app) => {
      app.get("/api/config", (c) =>
        c.json({
          ttlChoicesSec: ttlChoices,
          maxKubeconfigTtlSec: opts.maxKubeconfigTtlSec,
          kcpPublicHost: opts.kcpPublicHost ?? null,
          credentialHelpers: opts.credentialHelpers ?? [],
        }));

      app.post("/api/clusters/list", async (c) => {
        const who = await caller(c);
        if (who instanceof Response) return who;
        try {
          return c.json({ clusters: await opts.gateway.list(who) });
        } catch (err) {
          return failure(c, who.did, "list", err);
        }
      });

      app.post("/api/clusters/create", nameAction("create"));
      app.post("/api/clusters/up", nameAction("up"));
      app.post("/api/clusters/down", nameAction("down"));
      app.post("/api/clusters/delete", nameAction("delete"));

      app.post("/api/clusters/kubeconfig", async (c) => {
        const req = await named(c);
        if (req instanceof Response) return req;
        const requested = req.input.ttlSeconds;
        if (req.input.noExpiry !== undefined && typeof req.input.noExpiry !== "boolean") {
          return c.json({ error: "InvalidRequest", message: "noExpiry must be a boolean" }, 400);
        }
        const noExpiry = req.input.noExpiry === true;
        if (noExpiry && requested !== undefined) {
          return c.json({ error: "InvalidRequest", message: "noExpiry and ttlSeconds are mutually exclusive" }, 400);
        }
        let ttl: number | undefined;
        if (requested !== undefined) {
          if (typeof requested !== "number" || !ttlChoices.includes(requested)) {
            return c.json({ error: "InvalidRequest", message: `ttlSeconds must be one of ${ttlChoices.join(", ")}` }, 400);
          }
          ttl = requested;
        }
        try {
          const grant = await opts.gateway.kubeconfig(req.caller, req.name, ttl, noExpiry);
          log("cluster_api_ok", { did: req.caller.did, action: "kubeconfig", name: req.name, ttlSeconds: ttl ?? null, noExpiry });
          c.header("Cache-Control", "no-store");
          return c.json({ name: req.name, filename: kubeconfigFilename(req.name), kubeconfig: grant.kubeconfig });
        } catch (err) {
          return failure(c, req.caller.did, "kubeconfig", err);
        }
      });
    },
  });
}

export interface PinnedDownloadsFactoryOptions {
  downloads: PinnedDownloads;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export const PINNED_DOWNLOAD_CACHE_CONTROL = "public, max-age=31536000, immutable";

export function createPinnedDownloadsFactory(opts: PinnedDownloadsFactoryOptions) {
  const log = opts.log ?? (() => {});
  return createFactory({
    initApp: (app) => {
      app.get("/downloads/:sha256/:name", (c) => {
        const sha256 = c.req.param("sha256");
        const name = c.req.param("name");
        const bytes = opts.downloads.get(sha256, name);
        if (!bytes) return c.json({ error: "NotFound", message: "no download with that sha256 and name" }, 404);
        log("pinned_download_served", { name, sha256, bytes: bytes.length });
        return c.body(bytes, 200, {
          "content-type": "application/octet-stream",
          "content-length": String(bytes.length),
          "etag": `"${sha256}"`,
          "cache-control": PINNED_DOWNLOAD_CACHE_CONTROL,
        });
      });
    },
  });
}
