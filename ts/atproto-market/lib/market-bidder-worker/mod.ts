// Deno worker sandbox bidder callbacks — extracted from hono-factory-compute-bidder.
// Handles workerManifest RFPs: registers manifest, creates instance, starts
// worker on accept. No Hono, no route wiring. Separate from VM compute.

import type { IdResolver } from "@atproto/identity";
import { createAttestationCid, type RecordMap } from "@atiproto/atproto-attestation";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import type { WorkerInstanceRunner, WorkerManifestStore } from "@publicdomainrelay/compute-deno-abc";
import { WORKER_MANIFEST_NSID, WORKER_INSTANCE_NSID } from "@publicdomainrelay/compute-deno-common";
import type { StrongRef as ComputeStrongRef } from "@publicdomainrelay/compute-deno-common";
import type {
  AttestationKeypair,
  SubmitRfpCallback,
  SubmitAcceptCallback,
} from "@publicdomainrelay/market-atproto";
import type { RecordResolver } from "@publicdomainrelay/market-abc";
import {
  BID_NSID,
  RECEIPT_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_BID_LXM,
  strongRef,
  type Logger,
} from "@publicdomainrelay/market-common";

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
}

export interface WorkerBidderDeps {
  did: string;
  attestationKp: AttestationKeypair;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  idResolver: IdResolver;
  relay: { proxyRef: string };
  workerManifestStore: WorkerManifestStore;
  workerRunner: WorkerInstanceRunner;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string }>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: RecordResolver;
}

function refKey(ref: { uri: string; cid: string }): string {
  return `${ref.uri}#${ref.cid}`;
}

export function createWorkerBidderCallbacks(deps: WorkerBidderDeps): {
  rfp: Record<string, Record<string, SubmitRfpCallback>>;
  accept: SubmitAcceptCallback;
} {
  const {
    did, attestationKp, signer, relay,
    workerManifestStore, workerRunner, log, activeContracts,
    createRepoRecord, createSignedRepoRecord, callService, resolve,
  } = deps;

  const onRfp: SubmitRfpCallback = async ({ rfpUri, rfpCid, rfp, issuerDid, log: cbLog }) => {
    cbLog("info", "bidder received worker RFP", { rfpUri, rfpCid, issuerDid });

    const nowIso = new Date().toISOString();

    const { uri: payloadUri, cid: payloadCid } = await createRepoRecord(
      "com.publicdomainrelay.temp.market.bids.free",
      { $type: "com.publicdomainrelay.temp.market.bids.free", cost: 0, createdAt: nowIso },
    );

    const bidRecord: Record<string, unknown> = {
      $type: BID_NSID,
      rfp: strongRef(rfpUri, rfpCid),
      payload: strongRef(payloadUri, payloadCid),
      submitAccept: `${did}#pdr_temp_market`,
      createdAt: nowIso,
    };
    const { uri: bidUri, cid: bidCid } = await createSignedRepoRecord(
      BID_NSID, bidRecord, relay.proxyRef,
    );

    cbLog("info", "bidder created worker bid", { bidUri, bidCid, payloadUri });

    const submitBidUrl = rfp.submitBid as string | undefined;
    if (submitBidUrl) {
      try {
        const res = await callService(submitBidUrl, SUBMIT_BID_NSID, SUBMIT_BID_LXM, {
          uri: bidUri, cid: bidCid,
        });
        cbLog("info", "bidder submitted bid to requester", { status: res.status, ok: res.ok });
      } catch (err) {
        cbLog("error", "bidder failed to submit bid", { error: String(err) });
      }
    }

    return { body: { ok: true, bidUri, bidCid } };
  };

  const onAccept: SubmitAcceptCallback = async ({
    acceptUri, acceptCid, accept, issuerDid, resolve: acceptResolve, log: cbLog,
  }) => {
    cbLog("info", "bidder received accept for worker", { acceptUri, acceptCid, issuerDid });

    const nowIso = new Date().toISOString();
    const rfpRef = accept.rfp as { uri: string; cid: string } | undefined;

    if (!rfpRef) {
      return { status: 400, body: { error: "InvalidRequest", message: "missing rfp ref in accept" } };
    }

    const rfpResolved = await acceptResolve.resolve({ uri: rfpRef.uri, cid: rfpRef.cid });
    const rfpRecord = rfpResolved as Record<string, unknown> | null;
    const payloadRef = rfpRecord?.payload as { uri: string; cid: string } | undefined;
    if (!payloadRef) {
      return { status: 400, body: { error: "InvalidRequest", message: "missing payload in rfp" } };
    }

    const payload = await acceptResolve.resolve({ uri: payloadRef.uri, cid: payloadRef.cid });
    if (!payload) {
      return { status: 400, body: { error: "InvalidRequest", message: "payload not found" } };
    }

    const payloadRecord = payload as Record<string, unknown>;
    if (payloadRecord.$type !== WORKER_MANIFEST_NSID) {
      return { status: 400, body: { error: "InvalidRequest", message: "payload is not a worker manifest" } };
    }

    await workerManifestStore.register(
      payloadRecord as unknown as import("@publicdomainrelay/compute-deno-common").WorkerManifestRecord,
    );

    const { uri: instanceUri, cid: instanceCid } = await createRepoRecord(
      WORKER_INSTANCE_NSID,
      {
        $type: WORKER_INSTANCE_NSID,
        manifest: strongRef(payloadRef.uri, payloadRef.cid),
      },
    );

    const instanceRef: ComputeStrongRef = {
      $type: "com.atproto.repo.strongRef",
      uri: instanceUri,
      cid: instanceCid,
    };
    const manifestRef: ComputeStrongRef = {
      $type: "com.atproto.repo.strongRef",
      uri: payloadRef.uri,
      cid: payloadRef.cid,
    };
    await workerRunner.start(instanceRef, manifestRef);

    cbLog("info", "bidder started worker", { instanceUri });

    const providerIdPromise = Promise.resolve(instanceUri as string | number | undefined);

    const acceptBare: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(accept)) {
      if (k !== "_uri" && k !== "_cid") acceptBare[k] = v;
    }
    const receiptMetadata: Record<string, unknown> = {
      $type: RECEIPT_NSID,
      rfp: strongRef(rfpRef.uri, rfpRef.cid),
      bid: null,
      accept: strongRef(acceptUri, acceptCid),
      payload: null,
      submitEvent: `${did}#pdr_temp_compute_event`,
      createdAt: nowIso,
    };
    const bindCid = createAttestationCid(
      acceptBare as RecordMap, receiptMetadata as RecordMap, issuerDid,
    );
    const receiptRecord = { ...receiptMetadata, cid: bindCid.toString() };
    const { uri: receiptUri, cid: receiptCid } = await createSignedRepoRecord(
      RECEIPT_NSID, receiptRecord, relay.proxyRef,
    );

    const rkey = receiptUri.split("/").pop()!;
    activeContracts.set(refKey({ uri: receiptUri, cid: receiptCid }), {
      providerIdPromise,
      acceptAuthor: issuerDid,
    });

    cbLog("info", "bidder created receipt for worker", {
      receiptUri, receiptCid, acceptAuthor: issuerDid, activeCount: activeContracts.size,
    });

    return {
      body: {
        id: rkey, uri: receiptUri, cid: receiptCid,
        submitEvent: `${did}#pdr_temp_compute_event`,
      },
    };
  };

  return {
    rfp: {
      pdr_temp_market: { [WORKER_MANIFEST_NSID]: onRfp },
    },
    accept: onAccept,
  };
}
