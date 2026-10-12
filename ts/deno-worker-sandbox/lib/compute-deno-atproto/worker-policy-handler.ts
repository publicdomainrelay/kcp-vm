import type { PermissionPolicyHandler } from "@publicdomainrelay/compute-deno-abc";
import type { PersistentWorker } from "@publicdomainrelay/sandbox-abc";
import type { WorkerManifestRecord } from "@publicdomainrelay/compute-deno-common";

const WORKER_SCRIPT = `
const ALLOWED = new Set(["net"]);
self.onmessage = (e) => {
  if (e.data?.type !== "evaluate") return;
  const manifest = e.data.manifest;
  const violations = [];
  const perms = manifest?.permissions;
  if (perms) {
    for (const [key, val] of Object.entries(perms)) {
      if (val === undefined || val === false) continue;
      if (!ALLOWED.has(key)) {
        violations.push({
          service: manifest.$type ?? "",
          scope: "com.publicdomainrelay.temp.compute.deno.registerWorkerManifest",
          policyId: "allow-net-only",
          msg: \`Permission "\${key}" not allowed; only net is permitted by built-in policy\`,
        });
      }
    }
  }
  self.postMessage({ type: "result", allow: violations.length === 0, violations });
};
`;

export function createWorkerPolicyHandler(
  _handlerName: string,
  createWorker: (url: string) => PersistentWorker,
): {
  handler: PermissionPolicyHandler;
  worker: PersistentWorker;
} {
  const workerUrl = `data:application/javascript;base64,${btoa(WORKER_SCRIPT)}`;
  const worker = createWorker(workerUrl);

  const handler: PermissionPolicyHandler = {
    async evaluate(manifest: WorkerManifestRecord) {
      return new Promise((resolve) => {
        worker.onMessage((msg: unknown) => {
          const data = msg as { type: string; allow: boolean; violations: Array<{ service: string; scope: string; policyId: string; msg: string }> };
          if (data.type === "result") {
            resolve({ allow: data.allow, violations: data.violations });
          }
        });
        worker.postMessage({ type: "evaluate", manifest });
      });
    },
  };

  return { handler, worker };
}
