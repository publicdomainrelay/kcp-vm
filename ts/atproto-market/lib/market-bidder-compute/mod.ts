// VM compute bidder callbacks — extracted from hono-factory-compute-bidder.
// Handles compute.vm RFPs: creates bid config, provisions VM on accept,
// destroys VM on vm.delete event. No Hono, no route wiring.

import { TID } from "@atproto/common";
import type { IdResolver } from "@atproto/identity";
import { createAttestationCid, type RecordMap } from "@atiproto/atproto-attestation";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { ComputeProvider } from "@publicdomainrelay/compute-provider-abc";
import {
  attestationFor,
  toStorableEntry,
  createRecordResolver,
  type AttestationKeypair,
  type InlineAttestation,
  type SubmitRfpCallback,
  type SubmitAcceptCallback,
  type EventDispatchContext,
} from "@publicdomainrelay/market-atproto";
import type { RecordResolver } from "@publicdomainrelay/market-abc";
import {
  BID_NSID,
  RECEIPT_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_BID_LXM,
  strongRef,
  type Logger,
  type StrongRef,
} from "@publicdomainrelay/market-common";
import { COMPUTE_VM_NSID, COMPUTE_EVENTS_VM_DELETE_NSID } from "@publicdomainrelay/market-common";

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
}

export interface VmBidderDeps {
  did: string;
  attestationKp: AttestationKeypair;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  idResolver: IdResolver;
  relay: { proxyRef: string };
  computeProvider: ComputeProvider;
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

export function createVmBidderCallbacks(deps: VmBidderDeps): {
  rfp: Record<string, Record<string, SubmitRfpCallback>>;
  accept: SubmitAcceptCallback;
  event: Record<string, Record<string, (ctx: EventDispatchContext) => Promise<{ status?: number; body?: unknown } | void>>>;
} {
  const {
    did, attestationKp, signer, idResolver, relay,
    computeProvider, log, activeContracts,
    createRepoRecord, createSignedRepoRecord, callService, resolve,
  } = deps;

  const onRfp: SubmitRfpCallback = async ({ rfpUri, rfpCid, rfp, issuerDid, log: cbLog }) => {
    cbLog("info", "bidder received VM RFP", { rfpUri, rfpCid, issuerDid });

    const nowIso = new Date().toISOString();

    const configRef = await computeProvider.createBidConfig(nowIso);
    const bidConfigRef = { uri: configRef.uri, cid: configRef.cid };
    cbLog("info", "bidder created bid config", { configUri: configRef.uri });

    const { uri: payloadUri, cid: payloadCid } = await createRepoRecord(
      "com.publicdomainrelay.temp.market.bids.free",
      { $type: "com.publicdomainrelay.temp.market.bids.free", cost: 0, createdAt: nowIso },
    );

    const bidRecord: Record<string, unknown> = {
      $type: BID_NSID,
      rfp: strongRef(rfpUri, rfpCid),
      payload: strongRef(payloadUri, payloadCid),
      bidConfig: strongRef(bidConfigRef.uri, bidConfigRef.cid),
      submitAccept: `${did}#pdr_temp_market`,
      createdAt: nowIso,
    };
    const { uri: bidUri, cid: bidCid } = await createSignedRepoRecord(
      BID_NSID, bidRecord, relay.proxyRef,
    );

    cbLog("info", "bidder created VM bid", { bidUri, bidCid, payloadUri });

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
    cbLog("info", "bidder received accept for VM", { acceptUri, acceptCid, issuerDid });

    const nowIso = new Date().toISOString();
    const rfpRef = accept.rfp as { uri: string; cid: string } | undefined;
    const bidRef = accept.bid as { uri: string; cid: string } | undefined;

    let providerIdPromise: Promise<string | number | undefined> = Promise.resolve(undefined);

    if (rfpRef) {
      providerIdPromise = (async (): Promise<string | number | undefined> => {
        const rfpResolved = await acceptResolve.resolve({ uri: rfpRef.uri, cid: rfpRef.cid });
        const rfpRecord = rfpResolved as Record<string, unknown> | null;
        const payloadRef = rfpRecord?.payload as { uri: string; cid: string } | undefined;
        if (!payloadRef) return undefined;
        const payload = await acceptResolve.resolve({ uri: payloadRef.uri, cid: payloadRef.cid });
        if (!payload) return undefined;

        const vm = payload as Record<string, unknown> | null;
        if (!vm) return undefined;

        let bidConfigResolved: { uri: string; cid: string; value: unknown } | null = null;
        if (bidRef) {
          try {
            const bidResolved = await acceptResolve.resolve({
              uri: bidRef.uri, cid: bidRef.cid,
            }) as Record<string, unknown> | null;
            const cfgRef = bidResolved?.bidConfig as { uri: string; cid: string } | undefined;
            if (cfgRef) {
              const cfgValue = await acceptResolve.resolve({ uri: cfgRef.uri, cid: cfgRef.cid });
              bidConfigResolved = { uri: cfgRef.uri, cid: cfgRef.cid, value: cfgValue };
            }
          } catch (err) {
            cbLog("warn", "bidder failed to resolve bidConfig", { error: String(err) });
          }
        }

        const bundle = {
          $type: "com.publicdomainrelay.temp.market.accept",
          accept: { uri: acceptUri, cid: acceptCid },
          rfp: { uri: rfpRef.uri, cid: rfpRef.cid },
          bid: bidRef ? { uri: bidRef.uri, cid: bidRef.cid } : null,
          bid_config: bidConfigResolved,
          vm: { uri: payloadRef.uri, cid: payloadRef.cid, value: vm },
        };
        const vmWithBundle = {
          ...vm,
          user_data: computeProvider.injectAcceptBundle(
            (vm.user_data as string) ?? "", bundle,
          ),
          _uri: payloadRef.uri,
          _cid: payloadRef.cid,
        };
        const result = await computeProvider.provision(vmWithBundle as Parameters<ComputeProvider["provision"]>[0], issuerDid);
        cbLog("info", "bidder provisioned VM", { providerId: result.providerId });
        return result.providerId;
      })().catch((err) => {
        cbLog("error", "bidder failed to provision VM", { error: String(err) });
        return undefined;
      });
    }

    const acceptBare: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(accept)) {
      if (k !== "_uri" && k !== "_cid") acceptBare[k] = v;
    }
    const receiptMetadata: Record<string, unknown> = {
      $type: RECEIPT_NSID,
      rfp: rfpRef ? strongRef(rfpRef.uri, rfpRef.cid) : null,
      bid: bidRef ? strongRef(bidRef.uri, bidRef.cid) : null,
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

    cbLog("info", "bidder created receipt for VM", {
      receiptUri, receiptCid, acceptAuthor: issuerDid, activeCount: activeContracts.size,
    });

    return {
      body: {
        id: rkey, uri: receiptUri, cid: receiptCid,
        submitEvent: `${did}#pdr_temp_compute_event`,
      },
    };
  };

  const onVmDelete = async (
    ctx: EventDispatchContext,
  ): Promise<{ status?: number; body?: unknown } | void> => {
    const receiptRef = ctx.event.receipt as { uri: string; cid: string } | undefined;
    if (!receiptRef) {
      ctx.log("warn", "submitEvent: no receipt in event", { uri: ctx.uri });
      return { status: 400, body: { error: "InvalidRequest", message: "missing receipt in event" } };
    }
    const rk = refKey(receiptRef);
    ctx.log("info", "submitEvent vm.delete", { receiptKey: rk, issuerDid: ctx.issuerDid });

    if (!activeContracts.has(rk)) {
      ctx.log("warn", "submitEvent: unknown receipt", { receiptKey: rk });
      return { status: 400, body: { error: "InvalidRequest", message: "unknown receipt" } };
    }

    const contract = activeContracts.get(rk)!;
    if (contract.acceptAuthor !== ctx.issuerDid) {
      ctx.log("warn", "submitEvent: issuerDid mismatch", {
        expected: contract.acceptAuthor, got: ctx.issuerDid,
      });
      return { status: 403, body: { error: "Forbidden", message: "not the accept author" } };
    }

    const reason = "vm.delete event received";
    const providerId = await contract.providerIdPromise;
    if (providerId !== undefined) {
      try {
        await computeProvider.destroy(providerId);
        ctx.log("info", "submitEvent: VM destroyed", { providerId, reason });
      } catch (err) {
        ctx.log("error", "submitEvent: failed to destroy VM", {
          providerId, error: String(err),
        });
      }
      if (computeProvider.teardown) {
        computeProvider.teardown().then(() =>
          log("info", "bidder_compute_provider_teardown_done", { did }),
        );
      }
    }

    activeContracts.delete(rk);
    ctx.log("info", "submitEvent: vm deleted", {
      receiptKey: rk, remaining: activeContracts.size,
    });

    return { body: { ok: true } };
  };

  return {
    rfp: {
      pdr_temp_market: { [COMPUTE_VM_NSID]: onRfp },
    },
    accept: onAccept,
    event: {
      pdr_temp_compute_event: { [COMPUTE_EVENTS_VM_DELETE_NSID]: onVmDelete },
    },
  };
}
