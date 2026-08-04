/**
 * policy-engine-executor-gha-lite — a PolicyEngineExecutor that evaluates a
 * `computer.socialweb.temp.policy.gha-lite` record by running its GitHub
 * Actions workflow through the existing gha-lite engine and reading the
 * workflow's terminal status as the policy verdict.
 *
 *   record value: { name, workflow [GHA YAML], permissions?, createdAt }
 *
 * The workflow IS the policy. The gha-lite engine runs it (jobs, steps, uses,
 * ${{ }} expressions) and returns a complete status whose exit_status is
 * success when every job succeeded and failure otherwise. A successful run
 * allows; any failure / error / timeout / non-complete status denies.
 */

import type { PolicyEngineExecutor } from "@publicdomainrelay/policy-engine-abc";
import {
  POLICY_GHA_LITE_NSID,
  splitAtUri,
} from "@publicdomainrelay/policy-common";
import type {
  PolicyEvalCtx,
  PolicyRecord,
  PolicyResult,
} from "@publicdomainrelay/policy-common";
import {
  WorkflowExecutor,
  ExitStatusSuccess,
  StatusComplete,
  type PolicyEngineComplete,
  type PolicyEngineRequest,
  type PolicyEngineStatus,
  type SandboxConfig,
} from "@publicdomainrelay/policy-engine-server-gha-lite";

/** Record $type this executor dispatches for. */
const KIND = POLICY_GHA_LITE_NSID;

/** Default cap on a single workflow execution before it is a denial. */
const DEFAULT_TIMEOUT_MS = 30_000;

/** Violation policyId for every denial from this executor. */
const POLICY_ID = "gha-lite";

export interface GhaLiteExecutorOptions {
  /** Override the sandbox (default: resolved from POLICY_ENGINE_NET_ONLY / POLICY_ENGINE_FS_API). */
  sandbox?: SandboxConfig;
  /** Override the deno binary used for subprocess actions. */
  denoPath?: string;
  /** Cap on one workflow execution (ms) before it is treated as a denial. */
  timeoutMs?: number;
}

/** Evaluates gha-lite policy records by executing their workflow. */
export class GhaLiteExecutor implements PolicyEngineExecutor {
  readonly kind = KIND;

  private readonly timeoutMs: number;
  private readonly sandbox?: SandboxConfig;
  private readonly denoPath?: string;

  constructor(opts: GhaLiteExecutorOptions = {}) {
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.sandbox = opts.sandbox;
    this.denoPath = opts.denoPath;
  }

  async execute(input: {
    policyRecord: PolicyRecord;
    ctx: PolicyEvalCtx;
    permissions?: Record<string, unknown>;
  }): Promise<PolicyResult> {
    const { policyRecord, ctx, permissions } = input;

    const workflow = policyRecord.value?.workflow;
    if (typeof workflow !== "string" || workflow.trim().length === 0) {
      return deny("gha-lite record has no workflow");
    }

    const request = buildRequest(policyRecord, ctx, permissions, workflow);
    const executor = new WorkflowExecutor({
      ...(this.sandbox !== undefined ? { sandbox: this.sandbox } : {}),
      ...(this.denoPath !== undefined ? { denoPath: this.denoPath } : {}),
    });

    try {
      const status = await withTimeout(executor.executeWorkflow(request), this.timeoutMs);
      return statusToResult(status);
    } catch (err) {
      return deny(errorMessage(err));
    }
  }
}

// ── request construction ─────────────────────────────────────────────────────

/**
 * Build the PolicyEngineRequest the gha-lite engine expects: the workflow YAML
 * plus the inputs/context the policy needs. The gha-lite policy actions read
 * `self-did` (required) and the market records (`rfp`/`bid`/`accept`, each
 * optional, firehose shape {uri,cid,value?}) as JSON strings, plus `policy-args`.
 */
function buildRequest(
  policyRecord: PolicyRecord,
  ctx: PolicyEvalCtx,
  permissions: Record<string, unknown> | undefined,
  workflow: string,
): PolicyEngineRequest {
  const inputs: Record<string, unknown> = {
    "self-did": ctx.selfDid,
    "subject-did": ctx.subjectDid,
    "root-requester-did": ctx.rootRequesterDid,
    "counterparty-did": ctx.counterpartyDid,
    perspective: ctx.perspective,
    "policy-args": JSON.stringify(ctx.args ?? {}),
  };

  // The record's permissions field, when the workflow references it.
  const recordPermissions = policyRecord.value?.permissions;
  if (recordPermissions !== undefined) {
    inputs["permissions"] = toJson(recordPermissions);
  }

  // Records present at the caller's stage, in the firehose shape the policies
  // expect. subjectDid/rootRequesterDid/demand/offer are derived from these by
  // the policy actions (see lib/policies/gha-lite/README.md).
  const rfp = demandToRecord(ctx.demand);
  if (rfp) inputs["rfp"] = JSON.stringify(rfp);
  const bid = offerToRecord(ctx.offer);
  if (bid) inputs["bid"] = JSON.stringify(bid);

  const repoId = repoFromUri(policyRecord.uri) ?? ctx.selfDid;
  const context: Record<string, unknown> = {
    config: {
      env: {
        GITHUB_REPOSITORY: repoId,
        GITHUB_ACTOR: ctx.selfDid,
      },
    },
  };
  if (permissions !== undefined) {
    // The engine ignores unknown context keys today; carrying permissions here
    // keeps them available to a workflow author without changing the engine.
    context["permissions"] = permissions;
  }

  return { workflow, inputs, context };
}

/** Firehose-shape record {uri,cid,value?} from the demand side, if present. */
function demandToRecord(demand: PolicyEvalCtx["demand"]): Record<string, unknown> | undefined {
  if (!demand?.rfpRef) return undefined;
  return {
    uri: demand.rfpRef.uri,
    cid: demand.rfpRef.cid,
    ...(demand.payload ? { value: demand.payload } : {}),
  };
}

/** Firehose-shape record {uri,cid,value?} from the offer side, if present. */
function offerToRecord(offer: PolicyEvalCtx["offer"]): Record<string, unknown> | undefined {
  if (!offer?.bidRef) return undefined;
  return {
    uri: offer.bidRef.uri,
    cid: offer.bidRef.cid,
    ...(offer.payload ? { value: offer.payload } : {}),
  };
}

/** The atproto repo (DID) an at:// URI names, if the URI parses. */
function repoFromUri(uri: string): string | undefined {
  if (typeof uri !== "string" || !uri.startsWith("at://")) return undefined;
  try {
    return splitAtUri(uri).repo || undefined;
  } catch {
    return undefined;
  }
}

function toJson(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

// ── status → verdict mapping ─────────────────────────────────────────────────

/**
 * Interpret the workflow's terminal status as a policy verdict.
 *
 * A complete status whose exit_status is "success" means every job succeeded —
 * the policy allows. Any failure, error, non-complete status, timeout, or
 * thrown exception denies.
 */
function statusToResult(status: PolicyEngineStatus): PolicyResult {
  const detail = status.detail;
  if (
    status.status === StatusComplete &&
    detail !== null &&
    typeof detail === "object" &&
    (detail as PolicyEngineComplete).exit_status === ExitStatusSuccess
  ) {
    return { allow: true, violations: [] };
  }
  return deny(describeStatus(status));
}

/** Human-readable denial message for a non-allowing status. */
function describeStatus(status: PolicyEngineStatus): string {
  const detail = status.detail;
  if (Array.isArray(detail)) {
    // input_validation_error carries [{msg, loc, type, ...}].
    const parts = detail.map((x) => {
      if (x !== null && typeof x === "object" && typeof (x as { msg?: unknown }).msg === "string") {
        return (x as { msg: string }).msg;
      }
      return JSON.stringify(x);
    });
    return parts.length > 0 ? parts.join("; ") : `policy engine status: ${status.status}`;
  }
  if (detail !== null && typeof detail === "object") {
    const d = detail as Record<string, unknown>;
    if (typeof d["exit_status"] === "string") {
      const annotations = d["annotations"] as Record<string, unknown> | undefined;
      const errors = Array.isArray(annotations?.["error"]) ? annotations["error"] as unknown[] : [];
      if (errors.length > 0) return String(errors[0]);
      return `policy workflow exited with ${String(d["exit_status"])}`;
    }
  }
  return `policy engine status: ${status.status}`;
}

function deny(msg: string): PolicyResult {
  return { allow: false, violations: [{ msg, policyId: POLICY_ID }] };
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** Race a promise against a timeout; the workflow continues in the background. */
function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    promise,
    new Promise<never>((_resolve, reject) => {
      timer = setTimeout(() => reject(new Error(`policy workflow timed out after ${ms}ms`)), ms);
    }),
  ]).finally(() => {
    if (timer !== undefined) clearTimeout(timer);
  });
}
