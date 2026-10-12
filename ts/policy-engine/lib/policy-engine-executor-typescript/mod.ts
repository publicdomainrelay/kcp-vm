/**
 * policy-engine-executor-typescript — the TypescriptExecutor policy engine
 * executor.
 *
 * Dispatch key: `computer.socialweb.temp.policy.typescript` (see
 * POLICY_TYPESCRIPT_NSID). The referenced record's `value` is:
 *
 *   { name: string,
 *     manifest: { uri, cid },        // strongRef → com.publicdomainrelay.temp.compute.deno.workerManifest
 *     permissions?: Record<string, unknown>,
 *     createdAt: string }
 *
 * `execute()` resolves `value.manifest` through the host `ctx.resolve`, reads
 * the workerManifest's `bundle` string (the policy code), and runs it in a
 * `deno-worker-sandbox` persistent Deno worker. The worker module embeds the
 * bundle plus a host-RPC shim: every host call the bundle makes
 * (resolve / resolveOperatorDid / getVouchedDids / log) is posted to the host
 * process and fulfilled by the real `PolicyEvalCtx`, so the bundle runs with
 * no ambient permissions by default (deny-all) and reaches the network/trust
 * graph only through the host.
 *
 *   common <- abc <- impl (this package) <- factory <- CLI
 *
 * A second executor kind = a sibling impl package + one EngineRegistry entry,
 * never a branch inside this file.
 */

import type { PolicyEngineExecutor, ScopeInput } from "@publicdomainrelay/policy-engine-abc";
import type {
  PolicyEvalCtx,
  PolicyRecord,
  PolicyResult,
  PolicyViolation,
  StrongRef,
} from "@publicdomainrelay/policy-common";
import { POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-common";
import { scopeDecide, type PolicyRegistry } from "@publicdomainrelay/policy-deno-typescript-shared";
import { createPolicyRegistry } from "@publicdomainrelay/policy-deno-typescript";
import type { PersistentWorker } from "@publicdomainrelay/sandbox-abc";
import type { SandboxPermissions } from "@publicdomainrelay/sandbox-common";
import { createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";

/** NSID of the workerManifest record that holds the bundled policy code. */
const WORKER_MANIFEST_NSID =
  "com.publicdomainrelay.temp.compute.deno.workerManifest";

/** Whole-evaluation budget, ms. The bundle may make several host-RPC
 * round-trips inside this window. Overridable via TypescriptExecutorOptions. */
const DEFAULT_TIMEOUT_MS = 30_000;

/**
 * The bundle contract a workerManifest `bundle` string must satisfy.
 *
 * The bundle is evaluated inside the worker module. It MUST assign
 * `globalThis.__evaluatePolicy` to an async function:
 *
 *   globalThis.__evaluatePolicy = async (input) => {
 *     // ...decide, using host-RPC for anything off-sandbox...
 *     return { allow: boolean, violations: Array<{ msg: string, policyId: string }> };
 *   };
 *
 * `input` is the plain-data policy context (the host `PolicyEvalCtx` minus its
 * host functions, which the shim injects as RPC stubs):
 *
 *   {
 *     policyName: string,
 *     args: PolicyArgs,
 *     perspective: "bidder" | "requester",
 *     selfDid: string,
 *     subjectDid: string,
 *     rootRequesterDid: string,
 *     counterpartyDid: string,
 *     resolve: (ref: { uri: string, cid: string }) => Promise<record>,
 *     resolveOperatorDid: (did: string) => Promise<string | null>,
 *     getVouchedDids: (did: string) => Promise<string[]>,
 *     log: (level: string, msg: string, meta?: Record<string, unknown>) => void,
 *     policyRef?: StrongRef,
 *     demand?: DemandSide,
 *     offer?: OfferSide,
 *   }
 *
 * The four functions are the ONLY way the bundle reaches the host:
 *
 *   resolve(ref)              → host ctx.resolve(ref)           — the record body
 *   resolveOperatorDid(did)   → host ctx.resolveOperatorDid(did) — operator DID or null
 *   getVouchedDids(did)       → host ctx.getVouchedDids(did)    — Set serialized to string[]
 *   log(level, msg, meta?)    → host ctx.log(level, msg, meta)  — structured log line
 *
 * The worker runs with deny-all permissions unless the policy record's
 * `value.permissions`, the manifest's `permissions`, or the caller's explicit
 * `permissions` grant something; every read the policy needs is meant to flow
 * through host-RPC. A well-formed result has boolean `allow` and an array of
 * `{ msg, policyId }` violations. Anything else is a deny with the error text.
 */
const WORKER_SHIM = `
const pending = new Map();
let nextRpcId = 1;

function send(m) { self.postMessage(m); }

function rpc(method, ...args) {
  const id = nextRpcId++;
  const { promise, resolve, reject } = Promise.withResolvers();
  pending.set(id, { resolve, reject });
  send({ type: "rpc", id, method, args });
  return promise;
}

self.onmessage = async (ev) => {
  const message = ev.data;
  if (!message || typeof message !== "object") return;

  if (message.type === "rpc-result") {
    const entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id);
    if (message.ok) entry.resolve(message.value);
    else entry.reject(new Error(message.error ?? "host rpc failed"));
    return;
  }

  if (message.type !== "eval") return;

  const evaluate = globalThis.__evaluatePolicy;
  if (typeof evaluate !== "function") {
    send({ type: "eval-result", id: message.id, error: "bundle did not assign globalThis.__evaluatePolicy" });
    return;
  }

  try {
    const input = message.input ?? {};
    const result = await evaluate({
      policyName: input.policyName,
      args: input.args ?? {},
      perspective: input.perspective,
      selfDid: input.selfDid,
      subjectDid: input.subjectDid,
      rootRequesterDid: input.rootRequesterDid,
      counterpartyDid: input.counterpartyDid,
      resolve: (ref) => rpc("resolve", ref),
      resolveOperatorDid: (did) => rpc("resolveOperatorDid", did),
      getVouchedDids: (did) => rpc("getVouchedDids", did),
      log: (level, msg, meta) => rpc("log", level, msg, meta),
      policyRef: input.policyRef,
      demand: input.demand,
      offer: input.offer,
    });
    send({ type: "eval-result", id: message.id, result });
  } catch (err) {
    send({
      type: "eval-result",
      id: message.id,
      error: "policy threw: " + (err instanceof Error ? err.message : String(err)),
    });
  }
};
`;

/** A deny result carrying `msg` under `policyId`. */
function deny(msg: string, policyId: string): PolicyResult {
  return { allow: false, violations: [{ msg, policyId }] as PolicyViolation[] };
}

/** Encode worker source as a module worker data: URL. */
function toDataUrl(source: string): string {
  const bytes = new TextEncoder().encode(source);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return `data:application/javascript;base64,${btoa(binary)}`;
}

/**
 * Coerce an arbitrary eval-result into a PolicyResult. The bundle's verdict is
 * untrusted data; a non-object, a non-boolean `allow`, or garbage violations
 * all become a deny (never a silent allow).
 */
function toPolicyResult(raw: unknown, policyId: string): PolicyResult {
  if (!raw || typeof raw !== "object") {
    return deny("policy returned a non-object result", policyId);
  }
  const r = raw as { allow?: unknown; violations?: unknown };
  const violations: PolicyViolation[] = [];
  if (Array.isArray(r.violations)) {
    for (const v of r.violations) {
      if (v && typeof v === "object") {
        const vv = v as { msg?: unknown; policyId?: unknown };
        violations.push({
          msg: typeof vv.msg === "string" ? vv.msg : "policy violation",
          policyId: typeof vv.policyId === "string" ? vv.policyId : policyId,
        });
      } else {
        violations.push({ msg: String(v ?? "policy violation"), policyId });
      }
    }
  }
  return { allow: r.allow === true, violations };
}

/**
 * Merge sandbox permission declarations: policy record `value.permissions`,
 * then workerManifest `permissions`, then the caller's explicit override
 * (later sources win per key). Default is deny-all (empty).
 */
function mergePermissions(
  ...sources: Array<unknown | undefined>
): SandboxPermissions {
  const out: Record<string, unknown> = {};
  for (const src of sources) {
    if (src && typeof src === "object") {
      Object.assign(out, src as Record<string, unknown>);
    }
  }
  return out as SandboxPermissions;
}

export interface TypescriptExecutorOptions {
  /** Whole-evaluation budget in ms. Default 30_000. */
  timeoutMs?: number;
  /** Worker spawner override (test seam / transport swap). Defaults to createPersistentDenoWorker. */
  createWorker?: (
    workerUrl: string | URL,
    permissions?: SandboxPermissions,
  ) => PersistentWorker;
  /** First-party policy registry for the scope lane. Defaults to
   *  createPolicyRegistry() — the builtin perspective-split set. */
  registry?: PolicyRegistry;
}

/**
 * TypescriptExecutor — runs a `computer.socialweb.temp.policy.typescript`
 * policy record by loading its workerManifest bundle into a deno-worker-sandbox
 * worker and evaluating it with host-RPC. See the bundle contract above.
 */
export class TypescriptExecutor implements PolicyEngineExecutor {
  readonly kind = POLICY_TYPESCRIPT_NSID;
  readonly #timeoutMs: number;
  readonly #spawn: (
    workerUrl: string | URL,
    permissions?: SandboxPermissions,
  ) => PersistentWorker;
  readonly #registry: PolicyRegistry;

  constructor(opts: TypescriptExecutorOptions = {}) {
    this.#timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.#spawn = opts.createWorker ?? createPersistentDenoWorker;
    this.#registry = opts.registry ?? createPolicyRegistry();
  }

  async execute(input: {
    policyRecord: PolicyRecord;
    ctx: PolicyEvalCtx;
    permissions?: Record<string, unknown>;
  }): Promise<PolicyResult> {
    const { policyRecord, ctx, permissions } = input;
    const value = policyRecord.value;

    // 1. The policy record's value must carry a strongRef to the workerManifest.
    if (!value || typeof value !== "object") {
      return deny("typescript policy record has no value", ctx.policyName);
    }
    const manifestRef = (value as { manifest?: unknown }).manifest as
      | StrongRef
      | undefined;
    if (
      !manifestRef || typeof manifestRef !== "object" ||
      typeof manifestRef.uri !== "string" || typeof manifestRef.cid !== "string"
    ) {
      return deny(
        "typescript policy record is missing value.manifest (strongRef to a workerManifest)",
        ctx.policyName,
      );
    }

    // 2. Resolve the workerManifest record; its `bundle` string is the policy code.
    let manifest: Record<string, unknown>;
    try {
      manifest = await ctx.resolve({
        uri: manifestRef.uri,
        cid: manifestRef.cid,
      });
    } catch (err) {
      return deny(
        `failed to resolve workerManifest ${manifestRef.uri}: ${err}`,
        ctx.policyName,
      );
    }
    if (!manifest || typeof manifest !== "object") {
      return deny(
        `workerManifest ${manifestRef.uri} resolved to a non-object`,
        ctx.policyName,
      );
    }
    const bundle = (manifest as { bundle?: unknown }).bundle;
    if (typeof bundle !== "string" || bundle.length === 0) {
      return deny("workerManifest has no bundle string", ctx.policyName);
    }

    // 3. Deny-all by default; grant only declared + explicitly-passed permissions.
    const sandboxPerms = mergePermissions(
      (value as { permissions?: unknown }).permissions,
      (manifest as { permissions?: unknown }).permissions,
      permissions,
    );

    return await this.#evaluate(bundle, sandboxPerms, ctx);
  }

  /**
   * Trust-only scope lane: resolve the record's named policies against the
   * first-party registry and run each trust policy's decide() over a trust
   * snapshot built from the host resolvers. No worker is spawned — the scope
   * lane never executes the bundle. Returns undefined (escalate to execute())
   * when the record names no registry trust policies or a decide() abstains.
   */
  async scope(input: {
    policyRecord: PolicyRecord;
    scope: ScopeInput;
  }): Promise<PolicyResult | undefined> {
    const { policyRecord, scope } = input;
    const value = policyRecord.value;
    if (!value || typeof value !== "object") return undefined;

    const named = (value as { policies?: unknown }).policies;
    if (!Array.isArray(named) || named.length === 0) return undefined;

    return await scopeDecide({
      registry: this.#registry,
      named: named as Array<{ name?: unknown; args?: unknown }>,
      perspective: scope.perspective,
      selfDid: scope.selfDid,
      counterpartyDid: scope.counterpartyDid,
      resolveOperatorDid: scope.resolveOperatorDid ?? (async () => null),
      getVouchedDids: scope.getVouchedDids ?? (async () => new Set<string>()),
    });
  }

  /**
   * Spawn a worker over the bundle + shim, drive the RPC loop, and settle on
   * the first terminal signal: eval-result, worker error/exit, or timeout.
   */
  async #evaluate(
    bundle: string,
    permissions: SandboxPermissions,
    ctx: PolicyEvalCtx,
  ): Promise<PolicyResult> {
    // The bundle runs in the worker module scope; the shim (appended after) owns
    // self.onmessage, so an eval control can never be captured by the bundle.
    const workerUrl = toDataUrl(`${bundle}\n${WORKER_SHIM}`);

    let worker: PersistentWorker;
    try {
      worker = this.#spawn(workerUrl, permissions);
    } catch (err) {
      return deny(
        `failed to start typescript policy worker: ${err}`,
        ctx.policyName,
      );
    }

    const { promise, resolve } = Promise.withResolvers<PolicyResult>();
    let settled = false;
    const finish = (result: PolicyResult) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      worker.shutdown().catch(() => {});
      resolve(result);
    };

    const timer = setTimeout(() => {
      finish(
        deny(
          `typescript policy worker timed out after ${this.#timeoutMs}ms`,
          ctx.policyName,
        ),
      );
    }, this.#timeoutMs);

    worker.onMessage((raw) => {
      const message = raw as Record<string, unknown> | null;
      if (!message || typeof message !== "object") return;

      switch (message.type) {
        // createPersistentDenoWorker surfaces worker.onerror here.
        case "error": {
          finish(
            deny(
              `typescript policy worker error: ${
                String(message.message ?? "unknown")
              }`,
              ctx.policyName,
            ),
          );
          return;
        }
        // The bundle's verdict (or its throw) — terminal.
        case "eval-result": {
          if (message.error !== undefined) {
            finish(deny(String(message.error), ctx.policyName));
          } else {
            finish(toPolicyResult(message.result, ctx.policyName));
          }
          return;
        }
        // A host-RPC request from the bundle — fulfill via ctx and reply.
        case "rpc": {
          const id = message.id as number;
          const method = message.method as string;
          const args = Array.isArray(message.args)
            ? message.args as unknown[]
            : [];
          this.#handleRpc(method, args, ctx)
            .then((value) =>
              worker.postMessage({ type: "rpc-result", id, ok: true, value })
            )
            .catch((err) =>
              worker.postMessage({
                type: "rpc-result",
                id,
                ok: false,
                error: String(err),
              })
            );
          return;
        }
        default:
          return;
      }
    });

    worker.postMessage({
      type: "eval",
      id: 1,
      input: {
        policyName: ctx.policyName,
        args: ctx.args,
        perspective: ctx.perspective,
        selfDid: ctx.selfDid,
        subjectDid: ctx.subjectDid,
        rootRequesterDid: ctx.rootRequesterDid,
        counterpartyDid: ctx.counterpartyDid,
        policyRef: ctx.policyRef,
        demand: ctx.demand,
        offer: ctx.offer,
      },
    });

    return await promise;
  }

  /** Serve one host-RPC method against the host ctx. Unknown method → error reply. */
  async #handleRpc(
    method: string,
    args: unknown[],
    ctx: PolicyEvalCtx,
  ): Promise<unknown> {
    switch (method) {
      case "resolve": {
        const ref = args[0] as StrongRef | undefined;
        if (
          !ref || typeof ref !== "object" ||
          typeof ref.uri !== "string" || typeof ref.cid !== "string"
        ) {
          throw new Error("resolve called without a { uri, cid } strongRef");
        }
        return await ctx.resolve({ uri: ref.uri, cid: ref.cid });
      }
      case "resolveOperatorDid": {
        const did = args[0];
        if (typeof did !== "string") {
          throw new Error("resolveOperatorDid called without a did string");
        }
        return await ctx.resolveOperatorDid(did);
      }
      case "getVouchedDids": {
        const did = args[0];
        if (typeof did !== "string") {
          throw new Error("getVouchedDids called without a did string");
        }
        const set = await ctx.getVouchedDids(did);
        return [...set];
      }
      case "log": {
        const [level, msg, meta] = args as [
          string,
          string,
          Record<string, unknown> | undefined,
        ];
        ctx.log(
          typeof level === "string" ? level : "info",
          typeof msg === "string" ? msg : String(msg ?? ""),
          meta && typeof meta === "object"
            ? meta as Record<string, unknown>
            : undefined,
        );
        return undefined;
      }
      default:
        throw new Error(`unknown host rpc method: ${method}`);
    }
  }
}

/** Convenience factory; equivalent to `new TypescriptExecutor(opts)`. */
export function createTypescriptExecutor(
  opts: TypescriptExecutorOptions = {},
): TypescriptExecutor {
  return new TypescriptExecutor(opts);
}
