import type { WorkerInstanceStore, SigningKey } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerInstanceRecord } from "@publicdomainrelay/compute-deno-common";
import type { StrongRef } from "@publicdomainrelay/compute-deno-common";
import { WORKER_INSTANCE_NSID, DenoComputeError, parseAtUri } from "@publicdomainrelay/compute-deno-common";
import type { PdsClient } from "./manifest-store.ts";
import { createInlineAttestationForRecord, signatureToBase64Url } from "./signing.ts";

export type { SigningKey };

export type { PdsClient } from "./manifest-store.ts";

export function createDenoComputeInstanceStore(
  pds: PdsClient,
  did: string,
): WorkerInstanceStore {
  return {
    async register(
      record: WorkerInstanceRecord,
      signingKey?: SigningKey,
    ): Promise<StrongRef> {
      const recordObj: Record<string, unknown> = {
        $type: WORKER_INSTANCE_NSID,
        manifest: record.manifest,
      };

      if (signingKey) {
        const attestation = await createInlineAttestationForRecord(
          { record: recordObj, repository: did, issuer: did },
          signingKey,
        );
        recordObj.signatures = [{
          $type: "network.attested.signature",
          key: attestation.key,
          cid: attestation.cid,
          signature: { $bytes: signatureToBase64Url(attestation.signature) },
          issuer: attestation.issuer,
          issuedAt: attestation.issuedAt,
        }];
      } else {
        recordObj.signatures = [];
      }

      const result = await pds.createRecord(did, WORKER_INSTANCE_NSID, recordObj);
      return { $type: "com.atproto.repo.strongRef", uri: result.uri, cid: result.cid };
    },

    async get(uri: string): Promise<WorkerInstanceRecord | null> {
      const parsed = parseAtUri(uri);
      if (!parsed) return null;
      const result = await pds.getRecord(parsed.did, parsed.collection, parsed.rkey);
      if (!result) return null;
      const v = result.value;
      return {
        manifest: v.manifest as StrongRef,
        signatures: v.signatures as unknown[] | undefined,
      };
    },

    async delete(_uri: string): Promise<void> {},
  };
}
