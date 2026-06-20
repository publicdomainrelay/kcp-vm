import type { PermissionPolicyHandler } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerManifestRecord, PermissionPolicyResult } from "@publicdomainrelay/compute-deno-common";
import { WORKER_MANIFEST_NSID } from "@publicdomainrelay/compute-deno-common";

const ALLOWED_NET_PERMISSIONS = new Set(["net"]);

export function createAllowNetOnlyPolicyHandler(): PermissionPolicyHandler {
  return {
    async evaluate(manifest: WorkerManifestRecord): Promise<PermissionPolicyResult> {
      const violations: Array<{ service: string; scope: string; policyId: string; msg: string }> = [];
      const perms = manifest.permissions;
      if (perms) {
        for (const [key, val] of Object.entries(perms)) {
          if (val === undefined || val === false) continue;
          if (!ALLOWED_NET_PERMISSIONS.has(key)) {
            violations.push({
              service: WORKER_MANIFEST_NSID,
              scope: "com.publicdomainrelay.temp.compute.deno.registerWorkerManifest",
              policyId: "allow-net-only",
              msg: `Permission "${key}" not allowed; only net is permitted by built-in policy`,
            });
          }
        }
      }
      return { allow: violations.length === 0, violations };
    },
  };
}

export const BUILTIN_HANDLERS: Record<string, () => PermissionPolicyHandler> = {
  "allow-net": createAllowNetOnlyPolicyHandler,
};
