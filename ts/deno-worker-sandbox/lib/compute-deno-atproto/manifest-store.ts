import type { WorkerManifestStore, SigningKey } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerManifestRecord } from "@publicdomainrelay/compute-deno-common";
import type { StrongRef } from "@publicdomainrelay/compute-deno-common";
import { WORKER_MANIFEST_NSID, DenoComputeError } from "@publicdomainrelay/compute-deno-common";
import { createInlineAttestationForRecord, signatureToBase64Url } from "./signing.ts";

export type { SigningKey };

export interface PdsClient {
  createRecord(
    did: string,
    collection: string,
    record: Record<string, unknown>,
    rkey?: string,
  ): Promise<{ uri: string; cid: string }>;
  getRecord(
    did: string,
    collection: string,
    rkey: string,
  ): Promise<{ uri: string; cid: string; value: Record<string, unknown> } | null>;
}

export function createDenoComputeManifestStore(
  pds: PdsClient,
  did: string,
): WorkerManifestStore {
  return {
    async register(
      record: WorkerManifestRecord,
      signingKey?: SigningKey,
    ): Promise<StrongRef> {
      const recordObj: Record<string, unknown> = {
        $type: WORKER_MANIFEST_NSID,
        lock: record.lock,
        json: record.json,
        bundle: record.bundle,
      };
      if (record.source) {
        recordObj.source = {};
        if (record.source.tangled) {
          (recordObj.source as Record<string, unknown>).tangled = record.source.tangled;
        }
        if (record.source.git) {
          (recordObj.source as Record<string, unknown>).git = record.source.git;
        }
      }
      if (record.config !== undefined) recordObj.config = record.config;
      if (record.configref) recordObj.configref = record.configref;

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

      const result = await pds.createRecord(did, WORKER_MANIFEST_NSID, recordObj);
      return { $type: "com.atproto.repo.strongRef", uri: result.uri, cid: result.cid };
    },

    async get(uri: string): Promise<WorkerManifestRecord | null> {
      const parsed = parseAtUri(uri);
      if (!parsed) return null;
      const result = await pds.getRecord(parsed.did, parsed.collection, parsed.rkey);
      if (!result) return null;
      const v = result.value;
      return {
        lock: v.lock as string,
        json: v.json as string,
        bundle: v.bundle as string,
        config: v.config as string | undefined,
        configref: v.configref as StrongRef | undefined,
        signatures: v.signatures as unknown[] | undefined,
      };
    },
  };
}

function parseAtUri(uri: string): { did: string; collection: string; rkey: string } | null {
  const m = /^at:\/\/([^/]+)\/([^/]+)\/(.+)$/.exec(uri);
  if (!m) return null;
  return { did: m[1], collection: m[2], rkey: m[3] };
}
