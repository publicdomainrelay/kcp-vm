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
import type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
} from "@publicdomainrelay/market-bidder-abc";
import {
  BID_NSID,
  RECEIPT_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_BID_LXM,
  EVENT_NSID,
  SUBMIT_EVENT_NSID,
  SUBMIT_EVENT_LXM,
  strongRef,
  type Logger,
  type StrongRef,
} from "@publicdomainrelay/market-common";
import { COMPUTE_VM_NSID, COMPUTE_EVENTS_VM_DELETE_NSID, COMPUTE_EVENTS_VM_ONNETWORK_NSID } from "@publicdomainrelay/market-common";

export interface VmBidderDeps {
  did: string;
  attestationKp: AttestationKeypair;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  idResolver: IdResolver;
  relay: { ingressRef: string; ingressUrl: string };
  computeProvider: ComputeProvider;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  onContractChange?: (event: ContractEvent) => void;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: RecordResolver;
  acceptToContract?: Map<string, import("@publicdomainrelay/market-bidder-abc").GuestContractEntry>;
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
    computeProvider, log, activeContracts, onContractChange,
    createRepoRecord, createSignedRepoRecord, callService, resolve,
  } = deps;

  const registered = new Set<string>();

  const onRfp: SubmitRfpCallback = async ({ rfpUri, rfpCid, rfp, issuerDid, log: cbLog }) => {
    cbLog("info", "bidder received VM RFP", { rfpUri, rfpCid, issuerDid });

    if (rfp.policy) {
      const { evaluateRfpPolicy } = await import("@publicdomainrelay/market-policy");
      const result = await evaluateRfpPolicy({
        policyRef: rfp.policy as StrongRef,
        subjectDid: did,
        rootRequesterDid: issuerDid,
        counterpartyDid: issuerDid,
        resolve: (ref) => resolve.resolve(ref),
        signer,
        log: (level, msg, meta) => cbLog(level as "info" | "warn" | "error", msg, meta),
      });
      if (!result.allow) {
        cbLog("warn", "policy rejected bid", { violations: result.violations });
        return { body: { ok: false, error: "policy rejected", violations: result.violations } };
      }
    }

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
      submitAccept: relay.ingressUrl,
      createdAt: nowIso,
    };
    const { uri: bidUri, cid: bidCid, record: signedBid } = await createSignedRepoRecord(
      BID_NSID, bidRecord, did,
    );

    cbLog("info", "bidder created VM bid", { bidUri, bidCid, payloadUri });

    const submitBidUrl = rfp.submitBid as string | undefined;
    if (submitBidUrl) {
      try {
        const res = await callService(submitBidUrl, SUBMIT_BID_NSID, SUBMIT_BID_LXM, {
          uri: bidUri, cid: bidCid, record: signedBid,
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
    let provisionIp: string | undefined;

    if (rfpRef) {
      providerIdPromise = Promise.race([
        (async (): Promise<string | number | undefined> => {
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
              cbLog("error", "bidder failed to resolve bidConfig", { error: String(err) });
            }
          }

          // Abort if bidConfig could not be resolved — provisioning
          // would produce an incomplete accept.json that causes
          // fedproxy-client to fatal-exit inside the guest.
          if (!bidConfigResolved) {
            cbLog("error", "aborting provision — bidConfig not resolved");
            return undefined;
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
          provisionIp = (result.metadata?.ip) as string | undefined;
          cbLog("info", "bidder provisioned VM", { providerId: result.providerId, metadata: result.metadata });
          return result.providerId;
        })(),
        new Promise<undefined>((_, reject) =>
          setTimeout(() => reject(new Error("provisioning timed out after 10 minutes")), 600_000)
        ),
      ]).catch((err) => {
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
      submitEvent: relay.ingressUrl,
      createdAt: nowIso,
    };
    const bindCid = createAttestationCid(
      acceptBare as RecordMap, receiptMetadata as RecordMap, issuerDid,
    );
    const receiptRecord = { ...receiptMetadata, cid: bindCid.toString() };
    const { uri: receiptUri, cid: receiptCid } = await createSignedRepoRecord(
      RECEIPT_NSID, receiptRecord, did,
    );

    const rkey = receiptUri.split("/").pop()!;
    const rk = refKey({ uri: receiptUri, cid: receiptCid });
    activeContracts.set(rk, {
      providerIdPromise,
      acceptAuthor: issuerDid,
      receiptUri,
      receiptCid,
      acceptedAt: nowIso,
    });

    onContractChange?.({ type: "accepted", key: rk, receiptUri, receiptCid, acceptAuthor: issuerDid, acceptedAt: nowIso });
    // Populate accept→receipt map for guest event endpoint lookups
    if (deps.acceptToContract) {
      deps.acceptToContract.set(refKey({ uri: acceptUri, cid: acceptCid }), {
        receiptKey: rk, receiptUri, receiptCid,
        submitEventUrl: (accept as { submitEvent?: string }).submitEvent,
      });
    }
    providerIdPromise.then(async (providerId) => {
      onContractChange?.({
        type: providerId ? "provisioned" : "provisioning-failed",
        key: rk, receiptUri, receiptCid, acceptAuthor: issuerDid, acceptedAt: nowIso, providerId,
      });

      // Always emit vm.onNetwork via firehose (record on bidder PDS, firehose distributes).
      // Previously gated on submitEventUrl which is empty with --no-ingress-proxy.
      if (providerId && !registered.has(rk)) {
        registered.add(rk);
        const nowIso = new Date().toISOString();

        createRepoRecord(COMPUTE_EVENTS_VM_ONNETWORK_NSID, {
          $type: COMPUTE_EVENTS_VM_ONNETWORK_NSID,
          address: provisionIp,
          createdAt: nowIso,
        }).then(({ uri: vmOnNetworkUri, cid: vmOnNetworkCid }) => {
          return createSignedRepoRecord(EVENT_NSID, {
            $type: EVENT_NSID,
            receipt: strongRef(receiptUri, receiptCid),
            payload: strongRef(vmOnNetworkUri, vmOnNetworkCid),
          }, did);
        }).then(({ uri: eventUri, cid: eventCid, record: eventRecord }) => {
          cbLog("info", "vm.onNetwork event created on PDS (firehose)", { receiptKey: rk, ip: provisionIp, eventUri });

          // Best-effort: also push via submitEvent XRPC if endpoint available.
          const submitEventUrl = (accept as { submitEvent?: string }).submitEvent;
          if (submitEventUrl) {
            callService(submitEventUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_LXM, {
              uri: eventUri, cid: eventCid, record: eventRecord,
            }).then(() => {
              cbLog("info", "vm.onNetwork event submitted to requester", { receiptKey: rk });
            }).catch((err: unknown) => {
              cbLog("error", "failed to submit vm.onNetwork event", { receiptKey: rk, error: String(err) });
            });
          }
        }).catch((err: unknown) => {
          cbLog("error", "vm.onNetwork record creation failed", { receiptKey: rk, error: String(err) });
        });
      }
    });

    cbLog("info", "bidder created receipt for VM", {
      receiptUri, receiptCid, acceptAuthor: issuerDid, activeCount: activeContracts.size,
    });

    return {
      body: {
        id: rkey, uri: receiptUri, cid: receiptCid,
        submitEvent: relay.ingressUrl,
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

    // Keyed by uri#cid at creation time, but the receipt record is re-written
    // afterwards (remote proof), so its current CID — the one a requester reads
    // back off the firehose — no longer matches. The URI identifies the contract;
    // authority is the acceptAuthor check below, not the CID.
    let contractKey: string | undefined = activeContracts.has(rk) ? rk : undefined;
    if (!contractKey) {
      for (const [key, c] of activeContracts) {
        if (c.receiptUri === receiptRef.uri) {
          contractKey = key;
          ctx.log("info", "submitEvent: receipt matched by uri (cid re-written)", {
            receiptKey: key, eventCid: receiptRef.cid,
          });
          break;
        }
      }
    }
    if (!contractKey) {
      ctx.log("warn", "submitEvent: unknown receipt", { receiptKey: rk });
      return { status: 400, body: { error: "InvalidRequest", message: "unknown receipt" } };
    }

    const contract = activeContracts.get(contractKey)!;
    if (contract.acceptAuthor !== ctx.issuerDid) {
      ctx.log("warn", "submitEvent: issuerDid mismatch", {
        expected: contract.acceptAuthor, got: ctx.issuerDid,
      });
      return { status: 403, body: { error: "Forbidden", message: "not the accept author" } };
    }

    const reason = "vm.delete event received";
    const providerId = await contract.providerIdPromise;

    let destroyed = false;
    if (providerId !== undefined) {
      try {
        await computeProvider.destroy(providerId);
        ctx.log("info", "submitEvent: VM destroyed", { providerId, reason });
        destroyed = true;
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
    } else {
      destroyed = true; // no VM existed, clean by definition
    }

    if (destroyed) {
      onContractChange?.({
        type: "terminated", key: contractKey,
        receiptUri: contract.receiptUri!, receiptCid: contract.receiptCid!,
        acceptAuthor: contract.acceptAuthor, acceptedAt: contract.acceptedAt!,
        terminatedAt: new Date().toISOString(), providerId,
      });
      activeContracts.delete(contractKey);
      ctx.log("info", "submitEvent: vm deleted", {
        receiptKey: contractKey, remaining: activeContracts.size,
      });
    } else {
      onContractChange?.({
        type: "termination-failed", key: rk,
        receiptUri: contract.receiptUri!, receiptCid: contract.receiptCid!,
        acceptAuthor: contract.acceptAuthor, acceptedAt: contract.acceptedAt!,
        terminatedAt: new Date().toISOString(), providerId,
      });
      ctx.log("warn", "submitEvent: vm not deleted — contract kept for retry", {
        receiptKey: rk, remaining: activeContracts.size,
      });
    }

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

export function createComputeProviderHooks(opts: {
  provider: ComputeProvider;
}): MarketBidderProviderRef {
  const { provider } = opts;
  return {
    serviceId: "pdr_temp_market",
    appliesTo: ["com.publicdomainrelay.temp.compute.vm"],
    setup: provider.setup,
    teardown: provider.teardown,
    buildCallbacks(deps: CallbackFactoryDeps): CallbackSet {
      const vm = createVmBidderCallbacks({
        did: deps.did,
        attestationKp: deps.attestationKp,
        signer: deps.signer,
        idResolver: deps.idResolver,
        relay: deps.relay,
        computeProvider: provider,
        log: deps.log,
        activeContracts: deps.activeContracts,
        onContractChange: deps.onContractChange,
        createRepoRecord: deps.createRepoRecord,
        createSignedRepoRecord: deps.createSignedRepoRecord,
        callService: deps.callService,
        resolve: deps.resolve,
        acceptToContract: deps.acceptToContract,
      });
      return { rfpCallbacks: vm.rfp, onAccept: vm.accept, eventCallbacks: vm.event };
    },
  };
}
