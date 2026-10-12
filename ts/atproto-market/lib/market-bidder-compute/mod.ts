// VM compute bidder callbacks -- extracted from hono-factory-compute-bidder.
// Handles compute.vm RFPs: creates bid config, provisions VM on accept,
// destroys VM on vm.delete event. No Hono, no route wiring.

import { TID } from "@atproto/common";
import type { IdResolver } from "@atproto/identity";
import { createAttestationCid, type RecordMap } from "@atiproto/atproto-attestation";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { ComputeProvider, GuestGone } from "@publicdomainrelay/compute-provider-abc";
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
import { nsidFromUri } from "@publicdomainrelay/market-abc";
import type { PolicyEvalCtx, PolicyResult } from "@publicdomainrelay/policy-engine-abc";
import type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
  Tombstone,
} from "@publicdomainrelay/market-bidder-abc";
import { TerminatedReceipts } from "@publicdomainrelay/market-bidder-abc";
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
import {
  COMPUTE_VM_NSID,
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_ONNETWORK_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  VM_TERMINATED_REASONS,
  type VmTerminatedReason,
} from "@publicdomainrelay/market-common";

export interface VmTermination {
  reason: VmTerminatedReason;
  exitCode?: number;
}

export function terminatedReasonOf(gone: GuestGone): VmTermination {
  const exitCode = Number.isInteger(gone.exitCode) ? gone.exitCode : undefined;
  const withCode = (reason: VmTerminatedReason): VmTermination =>
    exitCode === undefined ? { reason } : { reason, exitCode };
  const known = VM_TERMINATED_REASONS.find((r) => r === gone.reason);
  if (known) return withCode(known);
  if (/\boom\b|out of memory/i.test(gone.reason)) return withCode("oom");
  if (/reboot/i.test(gone.reason)) return withCode("host_reboot");
  if (/identity/i.test(gone.reason)) return withCode("identity_mismatch");
  if (/exited/i.test(gone.reason) && exitCode === 0) return withCode("guest_poweroff");
  return withCode("vmm_exited");
}

interface TerminationTarget {
  key: string;
  receiptUri: string;
  receiptCid: string;
  submitEventUrl?: string;
}

interface TerminationFields {
  reason: VmTerminatedReason;
  exitCode?: number;
  observedAt: string;
  request?: StrongRef;
}

/**
 * The URL a guest is told to report its tunnel FQDN to, carried in the accept
 * bundle as `guest_onnetwork_url`.
 *
 * A guest reaches the bidder's own `/v1/on-network` (the route that holds the
 * accept-to-receipt map, wraps the record in a market.event and submits it to
 * the requester) through the same relay dispatcher its tunnel dials. The relay's
 * advertised URL carries no port -- TLS termination sits at 443 in a deployment
 * -- but a locally-run dispatcher listens on an ephemeral one, and the guest's
 * cloud-config already names it as `--ingress-proxy-host host:port`. When the
 * relay's hostname is behind such a dispatcher, that port is the one the guest
 * can actually connect to, and the dispatcher speaks plain HTTP. Otherwise the
 * relay's own URL is used unchanged.
 */
export function guestOnNetworkUrl(relayUrl: string, userData: string): string {
  const base = `${relayUrl.replace(/\/+$/, "")}/v1/on-network`;
  let relayHost = "";
  try {
    relayHost = new URL(relayUrl).host;
  } catch {
    return base;
  }
  const proxy = /--ingress-proxy-host[=\s]+(\S+)/.exec(userData)?.[1];
  if (!proxy) return base;
  const at = proxy.lastIndexOf(":");
  if (at <= 0) return base;
  const proxyHost = proxy.slice(0, at);
  const port = proxy.slice(at + 1);
  if (!/^\d+$/.test(port)) return base;
  if (relayHost !== proxyHost && !relayHost.endsWith(`.${proxyHost}`)) return base;
  return `http://${relayHost}:${port}/v1/on-network`;
}

export interface VmBidderDeps {
  /** How this bidder is willing to execute an RFP's attached policy. */
  policyExec?: { onlyRemote?: boolean; allowUntrusted?: boolean };
  /** Vouch/follow set lookup handed to locally executed policies. */
  getVouchedDids?: (did: string) => Promise<Set<string>>;
  /** Bidder DID -> operator DID, via bidder_associate records. */
  resolveOperatorDid?: (bidderDid: string) => Promise<string | null>;
  /** Policy engine evaluator -- evaluates RFP.policies[] refs in onRfp. */
  evaluator: {
    evaluatePolicies(input: {
      refs: Array<{ uri: string; cid: string }>;
      ctx: PolicyEvalCtx;
    }): Promise<PolicyResult>;
  };
  did: string;
  attestationKp: AttestationKeypair;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  idResolver: IdResolver;
  relay: { ingressRef: string; ingressUrl: string };
  computeProvider: ComputeProvider;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  onContractChange?: (event: ContractEvent) => void | Promise<void>;
  saveTombstone?: (tombstone: Tombstone) => Promise<void>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: RecordResolver;
  acceptToContract?: Map<string, import("@publicdomainrelay/market-bidder-abc").GuestContractEntry>;
  terminatedReceipts?: TerminatedReceipts;
}

function refKey(ref: { uri: string; cid: string }): string {
  return `${ref.uri}#${ref.cid}`;
}

function contractEventFields(key: string, contract: ActiveContract): Omit<ContractEvent, "type"> {
  const fields: Omit<ContractEvent, "type"> = {
    key,
    receiptUri: contract.receiptUri!,
    receiptCid: contract.receiptCid!,
    acceptAuthor: contract.acceptAuthor,
    acceptedAt: contract.acceptedAt!,
  };
  if (contract.acceptUri !== undefined) fields.acceptUri = contract.acceptUri;
  if (contract.acceptCid !== undefined) fields.acceptCid = contract.acceptCid;
  if (contract.submitEventUrl !== undefined) fields.submitEventUrl = contract.submitEventUrl;
  if (contract.provider !== undefined) fields.provider = contract.provider;
  return fields;
}

export function createVmBidderCallbacks(deps: VmBidderDeps): {
  rfp: Record<string, Record<string, SubmitRfpCallback>>;
  accept: SubmitAcceptCallback;
  event: Record<string, Record<string, (ctx: EventDispatchContext) => Promise<{ status?: number; body?: unknown } | void>>>;
  restore: () => Promise<void>;
} {
  const {
    did, attestationKp, signer, idResolver, relay,
    computeProvider, log, activeContracts, onContractChange,
    createRepoRecord, createSignedRepoRecord, callService, resolve,
  } = deps;

  const registered = new Set<string>();
  const tombstones = deps.terminatedReceipts ?? new TerminatedReceipts();
  const terminating = new Map<string, Promise<void>>();

  const publishTerminated = async (
    target: TerminationTarget,
    fields: TerminationFields,
    cbLog: Logger,
  ): Promise<void> => {
    try {
      const { uri: payloadUri, cid: payloadCid } = await createRepoRecord(COMPUTE_EVENTS_VM_TERMINATED_NSID, {
        $type: COMPUTE_EVENTS_VM_TERMINATED_NSID,
        reason: fields.reason,
        ...(fields.exitCode !== undefined ? { exitCode: fields.exitCode } : {}),
        observedAt: fields.observedAt,
        ...(fields.request ? { request: fields.request } : {}),
        createdAt: new Date().toISOString(),
      });
      const { uri: eventUri, cid: eventCid, record: eventRecord } = await createSignedRepoRecord(EVENT_NSID, {
        $type: EVENT_NSID,
        receipt: strongRef(target.receiptUri, target.receiptCid),
        payload: strongRef(payloadUri, payloadCid),
      }, did);
      cbLog("info", "vm.terminated event created on PDS (firehose)", {
        receiptKey: target.key, reason: fields.reason, exitCode: fields.exitCode, eventUri,
      });
      if (!target.submitEventUrl) return;
      const res = await callService(target.submitEventUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_LXM, {
        uri: eventUri, cid: eventCid, record: eventRecord,
      });
      cbLog("info", "vm.terminated event submitted to requester", {
        receiptKey: target.key, status: res.status, ok: res.ok,
      });
    } catch (err) {
      cbLog("error", "vm.terminated event failed", { receiptKey: target.key, error: String(err) });
    }
  };

  const saveChange = async (event: ContractEvent, cbLog: Logger): Promise<void> => {
    try {
      await onContractChange?.(event);
    } catch (err) {
      cbLog("error", "bidder_contract_save_failed", { receiptKey: event.key, type: event.type, error: String(err) });
    }
  };

  const recordTermination = async (
    key: string,
    contract: ActiveContract,
    reason: VmTerminatedReason,
    observedAt: string,
    providerId: string | number | undefined,
    cbLog: Logger,
  ): Promise<void> => {
    activeContracts.delete(key);
    if (contract.receiptUri) {
      const entry = { acceptAuthor: contract.acceptAuthor, reason, at: observedAt };
      tombstones.add(contract.receiptUri, entry);
      const tombstone: Tombstone = { receiptUri: contract.receiptUri, ...entry };
      if (contract.receiptCid !== undefined) tombstone.receiptCid = contract.receiptCid;
      if (contract.acceptUri !== undefined) tombstone.acceptUri = contract.acceptUri;
      if (contract.acceptCid !== undefined) tombstone.acceptCid = contract.acceptCid;
      try {
        await deps.saveTombstone?.(tombstone);
      } catch (err) {
        cbLog("error", "bidder_tombstone_save_failed", { receiptKey: key, error: String(err) });
      }
    }
    await saveChange({
      type: "terminated", ...contractEventFields(key, contract), terminatedAt: observedAt, providerId,
    }, cbLog);
  };

  const releaseGoneGuest = async (gone: GuestGone): Promise<void> => {
    const observedAt = new Date().toISOString();
    for (const [key, contract] of activeContracts) {
      const providerId = await contract.providerIdPromise?.catch(() => undefined);
      if (providerId === undefined || String(providerId) !== String(gone.id)) continue;
      if (!activeContracts.has(key)) return;
      if (contract.receiptUri && terminating.has(contract.receiptUri)) return;
      const termination = terminatedReasonOf(gone);
      await recordTermination(key, contract, termination.reason, observedAt, providerId, log);
      log("warn", "bidder_guest_gone", {
        receiptKey: key, providerId, reason: gone.reason, exitCode: gone.exitCode,
        terminatedReason: termination.reason, remaining: activeContracts.size,
      });
      if (contract.receiptUri && contract.receiptCid) {
        await publishTerminated(
          { key, receiptUri: contract.receiptUri, receiptCid: contract.receiptCid, submitEventUrl: contract.submitEventUrl },
          { ...termination, observedAt },
          log,
        );
      }
      return;
    }
    log("info", "bidder_guest_gone_no_contract", { providerId: gone.id, reason: gone.reason });
  };

  const restore = async (): Promise<void> => {
    for (const [key, contract] of [...activeContracts]) {
      if (!contract.restored || contract.provider !== computeProvider.name) continue;
      contract.restored = false;
      if (!activeContracts.has(key)) continue;
      const providerId = await contract.providerIdPromise?.catch(() => undefined);
      let gone: GuestGone | undefined;
      if (providerId === undefined) {
        gone = { id: "", reason: "vmm_exited" };
        log("warn", "bidder_restored_contract_without_guest", { receiptKey: key });
      } else if (computeProvider.adopt) {
        const adopted = await computeProvider.adopt(providerId).catch((err: unknown) => {
          log("error", "bidder_guest_adopt_failed", { receiptKey: key, providerId, error: String(err) });
          return undefined;
        });
        if (adopted?.state === "gone") gone = adopted.gone;
        else if (adopted) log("info", "bidder_guest_adopted", { receiptKey: key, providerId });
      }
      if (!gone) continue;
      const observedAt = new Date().toISOString();
      const termination = terminatedReasonOf(gone);
      await recordTermination(key, contract, termination.reason, observedAt, providerId, log);
      log("warn", "bidder_guest_gone_at_restore", {
        receiptKey: key, providerId, reason: gone.reason, exitCode: gone.exitCode,
        terminatedReason: termination.reason, remaining: activeContracts.size,
      });
      if (contract.receiptUri && contract.receiptCid) {
        void publishTerminated(
          { key, receiptUri: contract.receiptUri, receiptCid: contract.receiptCid, submitEventUrl: contract.submitEventUrl },
          { ...termination, observedAt },
          log,
        );
      }
    }
  };

  computeProvider.onGuestGone?.((gone) => {
    releaseGoneGuest(gone).catch((err: unknown) =>
      log("error", "bidder_guest_gone_release_failed", { providerId: gone.id, error: String(err) })
    );
  });

  const onRfp: SubmitRfpCallback = async ({ rfpUri, rfpCid, rfp, issuerDid, log: cbLog }) => {
    cbLog("info", "bidder received VM RFP", { rfpUri, rfpCid, issuerDid });

    // Evaluate each attached policy strongRef; first deny short-circuits.
    // Absent/empty policies = no restriction -- open to all bidders.
    const policyRefs = rfp.policies ?? [];
    for (const policyRef of policyRefs) {
      const result = await deps.evaluator.evaluatePolicies({
        refs: [policyRef],
        ctx: {
          policyName: "rfp-policy",
          args: {},
          perspective: "bidder",
          selfDid: did,
          subjectDid: did,
          rootRequesterDid: issuerDid,
          counterpartyDid: issuerDid,
          resolve: (ref) => resolve.resolve(ref),
          resolveOperatorDid: deps.resolveOperatorDid ?? (async () => null),
          getVouchedDids: deps.getVouchedDids ?? (async () => new Set<string>()),
          log: (level, msg, meta) => cbLog(level as "info" | "warn" | "error", msg, meta),
          demand: (() => {
            const payloadRef = rfp.payload as { uri: string; cid: string } | undefined;
            return payloadRef
              ? { rfpRef: { uri: rfpUri, cid: rfpCid }, payloadRef, payloadNsid: nsidFromUri(payloadRef.uri) }
              : undefined;
          })(),
        },
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

    const known = deps.acceptToContract?.get(refKey({ uri: acceptUri, cid: acceptCid }));
    if (known) {
      cbLog("info", "bidder_accept_already_contracted", {
        acceptUri, receiptUri: known.receiptUri, terminated: tombstones.has(known.receiptUri),
      });
      return {
        body: {
          id: known.receiptUri.split("/").pop(), uri: known.receiptUri, cid: known.receiptCid,
          submitEvent: relay.ingressUrl, alreadyAccepted: true,
        },
      };
    }

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
            // Which ref failed matters: "getRecord failed: 400" on its own does not
            // say whether the bid or the config was unresolvable, nor which one.
            let step = "bid";
            let stepRef: { uri: string; cid: string } = bidRef;
            try {
              const bidResolved = await acceptResolve.resolve({
                uri: bidRef.uri, cid: bidRef.cid,
              }) as Record<string, unknown> | null;
              const cfgRef = bidResolved?.bidConfig as { uri: string; cid: string } | undefined;
              if (cfgRef) {
                step = "config";
                stepRef = cfgRef;
                const cfgValue = await acceptResolve.resolve({ uri: cfgRef.uri, cid: cfgRef.cid });
                bidConfigResolved = { uri: cfgRef.uri, cid: cfgRef.cid, value: cfgValue };
              }
            } catch (err) {
              cbLog("error", "bidder failed to resolve bidConfig", {
                error: String(err), step, uri: stepRef.uri, cid: stepRef.cid,
              });
            }
          }

          // Abort if bidConfig could not be resolved -- provisioning
          // would produce an incomplete accept.json that causes
          // fedproxy-client to fatal-exit inside the guest.
          if (!bidConfigResolved) {
            cbLog("error", "aborting provision -- bidConfig not resolved");
            return undefined;
          }

          const bundle = {
            $type: "com.publicdomainrelay.temp.market.accept",
            accept: { uri: acceptUri, cid: acceptCid },
            rfp: { uri: rfpRef.uri, cid: rfpRef.cid },
            bid: bidRef ? { uri: bidRef.uri, cid: bidRef.cid } : null,
            bid_config: bidConfigResolved,
            vm: { uri: payloadRef.uri, cid: payloadRef.cid, value: vm },
            guest_onnetwork_url: guestOnNetworkUrl(relay.ingressUrl, (vm.user_data as string) ?? ""),
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
    const contract: ActiveContract = {
      providerIdPromise,
      acceptAuthor: issuerDid,
      receiptUri,
      receiptCid,
      acceptUri,
      acceptCid,
      acceptedAt: nowIso,
      submitEventUrl: (accept as { submitEvent?: string }).submitEvent,
      provider: computeProvider.name,
    };
    activeContracts.set(rk, contract);

    try {
      await onContractChange?.({ type: "accepted", ...contractEventFields(rk, contract) });
    } catch (err) {
      activeContracts.delete(rk);
      cbLog("error", "bidder_contract_save_failed", { receiptKey: rk, type: "accepted", error: String(err) });
      providerIdPromise.then((providerId) => {
        if (providerId !== undefined) return computeProvider.destroy(providerId);
      }).catch((destroyErr: unknown) =>
        cbLog("error", "bidder_unsaved_guest_destroy_failed", { receiptKey: rk, error: String(destroyErr) })
      );
      throw err;
    }
    // Populate accept->receipt map for guest event endpoint lookups
    if (deps.acceptToContract) {
      deps.acceptToContract.set(refKey({ uri: acceptUri, cid: acceptCid }), {
        receiptKey: rk, receiptUri, receiptCid,
        submitEventUrl: (accept as { submitEvent?: string }).submitEvent,
      });
    }
    providerIdPromise.then(async (providerId) => {
      if (activeContracts.has(rk)) {
        await saveChange({
          type: providerId ? "provisioned" : "provisioning-failed",
          ...contractEventFields(rk, contract), providerId,
        }, cbLog);
      }

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
    // afterwards (remote proof), so its current CID -- the one a requester reads
    // back off the firehose -- no longer matches. The URI identifies the contract;
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
      const tombstone = tombstones.get(receiptRef.uri);
      if (!tombstone) {
        ctx.log("warn", "submitEvent: unknown receipt", { receiptKey: rk });
        return { status: 400, body: { error: "InvalidRequest", message: "unknown receipt" } };
      }
      if (tombstone.acceptAuthor !== ctx.issuerDid) {
        ctx.log("warn", "submitEvent: issuerDid mismatch", {
          expected: tombstone.acceptAuthor, got: ctx.issuerDid,
        });
        return { status: 403, body: { error: "Forbidden", message: "not the accept author" } };
      }
      ctx.log("info", "submitEvent: vm.delete for already-terminated receipt", {
        receiptKey: rk, reason: tombstone.reason, terminatedAt: tombstone.at,
      });
      return { body: { ok: true, alreadyTerminated: true, reason: tombstone.reason } };
    }

    const contract = activeContracts.get(contractKey)!;
    if (contract.acceptAuthor !== ctx.issuerDid) {
      ctx.log("warn", "submitEvent: issuerDid mismatch", {
        expected: contract.acceptAuthor, got: ctx.issuerDid,
      });
      return { status: 403, body: { error: "Forbidden", message: "not the accept author" } };
    }

    const receiptUri = contract.receiptUri ?? receiptRef.uri;
    const inFlight = terminating.get(receiptUri);
    if (inFlight) {
      ctx.log("info", "submitEvent: vm.delete already in progress", { receiptKey: contractKey });
      await inFlight;
      return { body: { ok: true, alreadyTerminated: true } };
    }
    const done = Promise.withResolvers<void>();
    terminating.set(receiptUri, done.promise);

    try {
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
        destroyed = true;
      }

      if (!destroyed) {
        await saveChange({
          type: "termination-failed", ...contractEventFields(contractKey, contract),
          terminatedAt: new Date().toISOString(), providerId,
        }, ctx.log);
        ctx.log("warn", "submitEvent: vm not deleted -- contract kept for retry", {
          receiptKey: rk, remaining: activeContracts.size,
        });
        return { body: { ok: true } };
      }

      const observedAt = new Date().toISOString();
      await recordTermination(
        contractKey, { ...contract, receiptUri }, "deleted_on_request", observedAt, providerId, ctx.log,
      );
      ctx.log("info", "submitEvent: vm deleted", {
        receiptKey: contractKey, remaining: activeContracts.size,
      });

      const deleteRef = ctx.event.payload as { uri?: string; cid?: string } | undefined;
      void publishTerminated(
        {
          key: contractKey,
          receiptUri,
          receiptCid: contract.receiptCid ?? receiptRef.cid,
          submitEventUrl: contract.submitEventUrl,
        },
        {
          reason: "deleted_on_request",
          observedAt,
          ...(deleteRef?.uri && deleteRef.cid ? { request: strongRef(deleteRef.uri, deleteRef.cid) } : {}),
        },
        ctx.log,
      );
    } finally {
      terminating.delete(receiptUri);
      done.resolve();
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
    restore,
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
        terminatedReceipts: deps.terminatedReceipts,
        saveTombstone: deps.saveTombstone,
        createRepoRecord: deps.createRepoRecord,
        createSignedRepoRecord: deps.createSignedRepoRecord,
        callService: deps.callService,
        resolve: deps.resolve,
        acceptToContract: deps.acceptToContract,
        policyExec: deps.policyExec,
        getVouchedDids: deps.getVouchedDids,
        resolveOperatorDid: deps.resolveOperatorDid,
        evaluator: deps.evaluator,
      });
      return { rfpCallbacks: vm.rfp, onAccept: vm.accept, eventCallbacks: vm.event, restore: vm.restore };
    },
  };
}
