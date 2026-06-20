import { Hono } from "@hono/hono";
import type { PermissionPolicyHandler } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerManifestRecord } from "@publicdomainrelay/compute-deno-common";
import { GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID } from "@publicdomainrelay/compute-deno-common";
import { DenoComputeError } from "@publicdomainrelay/compute-deno-common";
import { BUILTIN_HANDLERS } from "./builtin-policy-handlers.ts";

export async function createLoopbackPolicyHandler(handlerName: string): Promise<{
  handler: PermissionPolicyHandler;
  server: { shutdown: () => void };
  url: string;
}> {
  const factory = BUILTIN_HANDLERS[handlerName];
  if (!factory) throw new DenoComputeError(`Unknown built-in handler: ${handlerName}`, 500, "InternalError");
  const policyHandler = factory();

  const app = new Hono();

  app.post(`/xrpc/${GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID}`, async (c) => {
    let body: { manifest?: WorkerManifestRecord };
    try { body = await c.req.json(); } catch {
      return c.json({ error: "Invalid JSON body" }, 400);
    }
    if (!body.manifest) {
      return c.json({ error: "manifest is required" }, 400);
    }
    const result = await policyHandler.evaluate(body.manifest);
    return c.json(result);
  });

  const listenPromise = new Promise<{ shutdown: () => void; url: string }>((resolve) => {
    const server = Deno.serve({ hostname: "127.0.0.1", port: 0, onListen({ hostname, port }) {
      resolve({
        shutdown: () => server.shutdown(),
        url: `http://${hostname}:${port}`,
      });
    } }, app.fetch);
  });

  const { shutdown, url } = await listenPromise;

  const handler: PermissionPolicyHandler = {
    async evaluate(manifest: WorkerManifestRecord) {
      const res = await fetch(`${url}/xrpc/${GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID}`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ manifest }),
      });
      return await res.json() as { allow: boolean; violations: Array<{ service: string; scope: string; policyId: string; msg: string }> };
    },
  };

  return { handler, server: { shutdown }, url };
}
