/**
 * policy-engine-executor-gha-lite — a PolicyEngineExecutor that evaluates a
 * `computer.socialweb.temp.policy.ghalite` record by running its GitHub
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

import type { PolicyEngineExecutor, ScopeInput } from "@publicdomainrelay/policy-engine-abc";
import { SANDBOX_DENIED_POLICY_ID } from "@publicdomainrelay/policy-engine-abc";
import {
  createConcurrencyLimiter,
  DEFAULT_POLICY_CONCURRENCY,
  POLICY_GHA_LITE_NSID,
  splitAtUri,
} from "@publicdomainrelay/policy-common";
import type {
  PolicyEvalCtx,
  PolicyRecord,
  PolicyResult,
} from "@publicdomainrelay/policy-common";
import { parse as parseYaml } from "@std/yaml";
import {
  WorkflowExecutor,
  EXPRESSION_DENIED,
  ExitStatusSuccess,
  StatusComplete,
  type Cache,
  type CacheFile,
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

export type GhaLiteWorkflowRunner = (request: PolicyEngineRequest) => Promise<PolicyEngineStatus>;

export interface GhaLiteExecutorOptions {
  /** Run the workflow somewhere else (the policy engine pod) instead of in this process. */
  workflowRunner?: GhaLiteWorkflowRunner;
  /** Override the sandbox (default: resolved from POLICY_ENGINE_NET_ONLY / POLICY_ENGINE_FS_API). */
  sandbox?: SandboxConfig;
  /** Override the deno binary used for subprocess actions. */
  denoPath?: string;
  /** Cap on one workflow execution (ms) before it is treated as a denial. */
  timeoutMs?: number;
  /** Cap on workflows running at once; each one holds worker isolates. */
  maxConcurrentRuns?: number;
}

/** Evaluates gha-lite policy records by executing their workflow. */
export class GhaLiteExecutor implements PolicyEngineExecutor {
  readonly kind = KIND;

  private readonly timeoutMs: number;
  private readonly sandbox?: SandboxConfig;
  private readonly denoPath?: string;
  private readonly workflowRunner?: GhaLiteWorkflowRunner;
  private readonly limited: ReturnType<typeof createConcurrencyLimiter>;

  constructor(opts: GhaLiteExecutorOptions = {}) {
    this.limited = createConcurrencyLimiter(opts.maxConcurrentRuns ?? DEFAULT_POLICY_CONCURRENCY);
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.sandbox = opts.sandbox;
    this.denoPath = opts.denoPath;
    this.workflowRunner = opts.workflowRunner;
    if (this.workflowRunner) return;
    // Default the bundled-actions dir so a policy workflow's `uses:
    // tangy/policy-*` steps resolve without the caller exporting
    // BUNDLED_ACTIONS_DIR (the atproto-market bidder/requester tests run the
    // engine from source and never set it). A caller-set value wins.
    if (!Deno.env.get("BUNDLED_ACTIONS_DIR")) {
      const candidate = new URL("../policies/gha-lite/bundled-actions/", import.meta.url);
      try {
        Deno.statSync(candidate);
        Deno.env.set("BUNDLED_ACTIONS_DIR", candidate.pathname);
      } catch {
        // packaged / compiled build without the actions dir — leave unset
      }
    }
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

    const trust = await buildTrustInput(ctx, ctx.selfDid, ctx.counterpartyDid);
    return await this.#run(buildRequest(policyRecord, ctx, permissions, workflow, trust));
  }

  /**
   * Trust-only scope lane: run the record's workflow in `mode: scope` with the
   * counterparty carried as an input, so the action can decide from a trust
   * snapshot without workload records. The scope lane never hard-denies a
   * well-formed record — the workflow's scope-mode action falls back to a full
   * evaluate when decide() abstains — so this returns a verdict, not undefined.
   */
  async scope(input: {
    policyRecord: PolicyRecord;
    scope: ScopeInput;
  }): Promise<PolicyResult | undefined> {
    const { policyRecord, scope } = input;

    const workflow = policyRecord.value?.workflow;
    if (typeof workflow !== "string" || workflow.trim().length === 0) {
      return deny("gha-lite record has no workflow");
    }

    const trust = await buildTrustInput(
      {
        resolveOperatorDid: scope.resolveOperatorDid ?? (async () => null),
        getVouchedDids: scope.getVouchedDids ?? (async () => new Set<string>()),
      },
      scope.selfDid,
      scope.counterpartyDid,
    );
    return await this.#run(buildScopeRequest(policyRecord, scope, workflow, trust));
  }

  /** Run a workflow request through the gha-lite engine and map status → verdict. */
  async #run(request: PolicyEngineRequest): Promise<PolicyResult> {
    if (this.workflowRunner || this.sandbox?.netOnly) {
      const refused = execStepsOf(request.workflow);
      if (refused) return deny(refused);
    }
    const run = this.workflowRunner ?? ((req: PolicyEngineRequest) =>
      new WorkflowExecutor({
        ...(this.sandbox !== undefined ? { sandbox: this.sandbox } : {}),
        ...(this.denoPath !== undefined ? { denoPath: this.denoPath } : {}),
      }).executeWorkflow(req));

    try {
      const status = await this.limited(() => withTimeout(run(request), this.timeoutMs));
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
/**
 * The host-provided trust snapshot: operatorOf / vouchedBy / trustedOperators
 * for the two sides, pre-resolved through the host's own resolvers so the
 * workflow action never has to re-read the trust graph over the network (which
 * fails in local test envs with fake DIDs and is slower in prod).
 */
export interface TrustInput {
  operatorOf: Record<string, string>;
  vouchedBy: Record<string, string[]>;
  trustedOperators: string[];
}

/** Cache entry (a map of workspace-relative path → file) holding a host trust
 * snapshot. Seeded into the request cache under `trust/<counterpartyDid>` so
 * the policy action reads it via createCacheStore instead of depending on a
 * workflow input forwarding host data. */
function trustCacheEntry(trust: TrustInput): Record<string, CacheFile> {
  return {
    "operatorOf.json": { data: JSON.stringify(trust.operatorOf), encoding: "text" },
    "vouchedBy.json": { data: JSON.stringify(trust.vouchedBy), encoding: "text" },
    "trustedOperators.json": { data: JSON.stringify(trust.trustedOperators), encoding: "text" },
  };
}

/** The `trust/<counterpartyDid>` cache entry for a request, keyed on the side
 * the snapshot was pre-resolved for. */
function trustCacheKey(counterpartyDid: string): string {
  return `trust/${counterpartyDid}`;
}

/** Pre-resolve the trust snapshot for the two sides through host resolvers. */
async function buildTrustInput(
  resolvers: {
    resolveOperatorDid?: (did: string) => Promise<string | null>;
    getVouchedDids?: (did: string) => Promise<Set<string>>;
  },
  selfDid: string,
  counterpartyDid: string,
): Promise<TrustInput> {
  const resolveOp = resolvers.resolveOperatorDid ?? (async () => null);
  const getVouches = resolvers.getVouchedDids ?? (async () => new Set<string>());

  const [selfOpResolved, coOp] = await Promise.all([resolveOp(selfDid), resolveOp(counterpartyDid)]);
  const selfOp = selfOpResolved ?? selfDid;

  const operatorOf: Record<string, string> = { [selfDid]: selfOp };
  if (coOp) operatorOf[counterpartyDid] = coOp;

  const trustedOperators = [selfDid, selfOp];
  const vouchers = [...new Set(coOp ? [...trustedOperators, coOp] : trustedOperators)];
  const vouchSets = await Promise.all(vouchers.map((op) => getVouches(op)));
  const vouchedBy: Record<string, string[]> = {};
  vouchers.forEach((op, i) => {
    vouchedBy[op] = [...vouchSets[i]];
  });

  return { operatorOf, vouchedBy, trustedOperators };
}

function buildRequest(
  policyRecord: PolicyRecord,
  ctx: PolicyEvalCtx,
  permissions: Record<string, unknown> | undefined,
  workflow: string,
  trust: TrustInput,
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
  // Deliver the host trust snapshot through the request cache (read by the
  // policy action under `trust/<counterpartyDid>`), not a workflow input — the
  // action never depends on the workflow forwarding host data.
  context["cache"] = { [trustCacheKey(ctx.counterpartyDid)]: trustCacheEntry(trust) };

  return { workflow, inputs, context };
}

/**
 * Build the scope-lane request: `mode: scope` + the two DIDs + policy args.
 * No workload records — the action derives everything from the counterparty DID
 * and its own trust snapshot. The optional `permissions` record field still
 * travels so a workflow that references it works in scope mode.
 */
function buildScopeRequest(
  policyRecord: PolicyRecord,
  scope: ScopeInput,
  workflow: string,
  trust: TrustInput,
): PolicyEngineRequest {
  const inputs: Record<string, unknown> = {
    mode: "scope",
    "self-did": scope.selfDid,
    "counterparty-did": scope.counterpartyDid,
    perspective: scope.perspective,
    "policy-args": JSON.stringify(scope.args ?? {}),
  };

  const recordPermissions = policyRecord.value?.permissions;
  if (recordPermissions !== undefined) {
    inputs["permissions"] = toJson(recordPermissions);
  }

  const repoId = repoFromUri(policyRecord.uri) ?? scope.selfDid;
  const context: Record<string, unknown> = {
    config: {
      env: {
        GITHUB_REPOSITORY: repoId,
        GITHUB_ACTOR: scope.selfDid,
      },
    },
  };
  // Trust snapshot rides the request cache (see buildRequest) — the scope lane
  // action reads it under `trust/<counterpartyDid>`.
  context["cache"] = { [trustCacheKey(scope.counterpartyDid)]: trustCacheEntry(trust) };

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
    return verdictOf((detail as PolicyEngineComplete).outputs);
  }
  const message = describeStatus(status);
  return message.startsWith(EXPRESSION_DENIED)
    ? { allow: false, violations: [{ msg: message, policyId: SANDBOX_DENIED_POLICY_ID }] }
    : deny(message);
}

export const NET_ONLY_REFUSED = "this evaluator runs gha-lite policies net-only and refuses";

export function execStepsOf(workflow: unknown): string | undefined {
  let parsed: unknown = workflow;
  if (typeof workflow === "string") {
    try {
      parsed = parseYaml(workflow);
    } catch (err) {
      return `the policy workflow is not YAML: ${errorMessage(err)}`;
    }
  }
  const jobs = (parsed as { jobs?: Record<string, { steps?: Array<Record<string, unknown>> }> } | null)?.jobs ?? {};
  for (const [name, job] of Object.entries(jobs)) {
    for (const step of job?.steps ?? []) {
      if (step && typeof step === "object" && "run" in step) {
        return `${NET_ONLY_REFUSED} a run: step (job ${name})`;
      }
    }
  }
  return undefined;
}

function verdictOf(outputs: Record<string, unknown> | undefined): PolicyResult {
  const jobs = Object.values(outputs ?? {}).filter((o): o is Record<string, unknown> =>
    o !== null && typeof o === "object" && "allow" in (o as Record<string, unknown>)
  );
  if (jobs.length === 0) return { allow: true, violations: [] };
  const violations: PolicyResult["violations"] = [];
  let allow = true;
  for (const o of jobs) {
    if (String(o["allow"]) !== "true") allow = false;
    const raw = o["violations"];
    let list: unknown = raw;
    if (typeof raw === "string" && raw.trim().length > 0) {
      try {
        list = JSON.parse(raw);
      } catch {
        list = [raw];
      }
    }
    if (Array.isArray(list)) {
      for (const v of list) {
        const msg = v && typeof v === "object" && typeof (v as { msg?: unknown }).msg === "string" ? (v as { msg: string }).msg : String(v);
        const policyId = v && typeof v === "object" && typeof (v as { policyId?: unknown }).policyId === "string"
          ? (v as { policyId: string }).policyId
          : POLICY_ID;
        violations.push({ msg, policyId });
      }
    }
  }
  if (!allow && violations.length === 0) violations.push({ msg: "the policy action answered allow=false", policyId: POLICY_ID });
  return { allow, violations: allow ? [] : violations };
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
