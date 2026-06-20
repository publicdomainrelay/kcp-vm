import type { PermissionPolicyHandler } from "@publicdomainrelay/compute-deno-abc";
import type { SigningKey } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerManifestRecord } from "@publicdomainrelay/compute-deno-common";
import { GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID } from "@publicdomainrelay/compute-deno-common";
import { signComputeServiceAuth } from "./service-auth.ts";

export function createRemotePermissionPolicyHandler(opts: {
  serviceEndpoint: string;
  signingKey: SigningKey;
  issuerDid: string;
}): PermissionPolicyHandler {
  const { serviceEndpoint } = opts;
  const aud = opts.serviceEndpoint.replace(/^https?:\/\//, "");

  return {
    async evaluate(manifest: WorkerManifestRecord) {
      const jwt = await signComputeServiceAuth(
        opts.signingKey,
        `did:web:${aud}`,
        GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID,
      );
      const res = await fetch(`https://${aud}/xrpc/${GATE_REGISTRY_WORKER_MANIFEST_PERMISSIONS_NSID}`, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          authorization: `Bearer ${jwt}`,
        },
        body: JSON.stringify({ manifest }),
      });
      return await res.json() as { allow: boolean; violations: Array<{ service: string; scope: string; policyId: string; msg: string }> };
    },
  };
}
