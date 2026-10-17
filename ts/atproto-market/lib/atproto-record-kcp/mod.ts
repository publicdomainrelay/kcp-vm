import type { KcpClient } from "@publicdomainrelay/kcp-client-abc";
import type { KcpResource } from "@publicdomainrelay/kcp-client-common";
import type { Did, RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import {
  API_RESOURCES,
  ATPROTO_RECORD_RESOURCE,
  type AtprotoRecord,
  type AtprotoRecordStatus,
  FINALIZER_ATPROTO_RECORD,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export const CONDITION_REFERENCES_RESOLVED = "ReferencesResolved";

export interface ValueFrom {
  kind?: string;
  name: string;
  fieldPath: string;
  apiVersion?: string;
  resource?: string;
  workspace?: string;
  namespace?: string;
}

export interface AtprotoRecordReconcilerOpts {
  kcp: KcpClient;
  workspace: string;
  repoName: string;
  did: string;
  repo: RepoApi;
  logger: StructuredLoggerInterface;
  intervalMs?: number;
}

export interface AtprotoRecordReconciler {
  wake(): void;
  stop(): void;
}

const DENO_POD_RESOURCE: KcpResource = { group: "deno.computer", version: "v1alpha1", plural: "denopods", kind: "DenoPod" };

type Spec = NonNullable<AtprotoRecord["spec"]>;
type Held = AtprotoRecord & { spec: Spec };

function canonical(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
  if (v && typeof v === "object") {
    return `{${Object.keys(v as Record<string, unknown>).sort().map((k) => `${JSON.stringify(k)}:${canonical((v as Record<string, unknown>)[k])}`).join(",")}}`;
  }
  return JSON.stringify(v);
}

export function valueFromOf(v: unknown): ValueFrom | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const keys = Object.keys(v);
  if (keys.length !== 1 || keys[0] !== "valueFrom") return null;
  const r = (v as { valueFrom: ValueFrom }).valueFrom;
  return r && (typeof r.kind === "string" || typeof r.resource === "string") && typeof r.name === "string" && typeof r.fieldPath === "string" ? r : null;
}

export function fieldAt(obj: unknown, path: string): unknown {
  let cur: unknown = obj;
  for (const part of path.replace(/\[(\d+)\]/g, ".$1").split(".").filter(Boolean)) {
    if (cur === null || cur === undefined || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[part];
  }
  return cur;
}

function resourceOf(r: ValueFrom): KcpResource {
  const known = [...API_RESOURCES, DENO_POD_RESOURCE].find((a) => r.resource ? a.plural === r.resource : a.kind === r.kind);
  if (known && !r.apiVersion) return known;
  if (r.resource && r.apiVersion) {
    const [group, version] = r.apiVersion.includes("/") ? r.apiVersion.split("/") : ["", r.apiVersion];
    return { group, version, plural: r.resource, kind: r.kind };
  }
  throw new Error(`valueFrom ${r.resource ?? r.kind} is not a socialweb.computer resource or denopods; name its apiVersion and resource`);
}

export function startAtprotoRecordReconciler(opts: AtprotoRecordReconcilerOpts): AtprotoRecordReconciler {
  const { kcp, logger, repo, repoName } = opts;
  const did = opts.did as Did;
  const target = { resource: ATPROTO_RECORD_RESOURCE, workspace: opts.workspace };
  const ac = new AbortController();
  let wake: (() => void) | undefined;

  async function setStatus(o: Held, next: AtprotoRecordStatus, cond: { status: "True" | "False"; reason: string; message: string }): Promise<void> {
    const prior = (o.status?.conditions ?? []).find((c) => c.type === CONDITION_REFERENCES_RESOLVED);
    const changed = !prior || prior.status !== cond.status || prior.reason !== cond.reason || prior.message !== cond.message;
    const condition = changed
      ? { type: CONDITION_REFERENCES_RESOLVED, ...cond, observedGeneration: o.metadata?.generation, lastTransitionTime: new Date().toISOString() }
      : prior;
    const status = { ...(o.status ?? {}), ...next, conditions: [condition, ...(o.status?.conditions ?? []).filter((c) => c.type !== CONDITION_REFERENCES_RESOLVED)] };
    if (canonical(o.status ?? {}) === canonical(status)) return;
    await kcp.patchStatus(target, o.metadata!.name!, (current) => ({ ...((current as AtprotoRecord)?.status ?? {}), ...status }));
  }

  async function resolveValue(v: unknown, missing: string[]): Promise<unknown> {
    const ref = valueFromOf(v);
    if (ref) {
      const resource = resourceOf(ref);
      const workspace = ref.workspace ?? opts.workspace;
      const what = `${resource.plural} ${ref.name} ${ref.fieldPath} in ${workspace}`;
      const t = { resource, workspace, namespace: ref.namespace ?? (resource.plural === DENO_POD_RESOURCE.plural ? "default" : undefined) };
      let obj: unknown;
      try {
        obj = await kcp.get(t, ref.name);
      } catch (err) {
        missing.push(`${what}: ${err instanceof Error ? err.message : String(err)}`);
        return undefined;
      }
      const value = obj ? fieldAt(obj, ref.fieldPath) : undefined;
      if (value === undefined || value === null || value === "") {
        missing.push(`${what}${obj ? " is empty" : " does not exist"}`);
        return undefined;
      }
      return value;
    }
    if (Array.isArray(v)) return await Promise.all(v.map((x) => resolveValue(x, missing)));
    if (v && typeof v === "object") {
      const out: Record<string, unknown> = {};
      for (const [k, x] of Object.entries(v)) out[k] = await resolveValue(x, missing);
      return out;
    }
    return v;
  }

  async function reconcile(o: Held): Promise<void> {
    const name = o.metadata!.name!;
    const finalizers = o.metadata?.finalizers ?? [];
    const { collection, rkey } = o.spec;
    if (o.metadata?.deletionTimestamp) {
      if (!finalizers.includes(FINALIZER_ATPROTO_RECORD)) return;
      if (await repo.getRecord(did, collection, rkey)) await repo.applyWrites(did, [{ action: "delete", collection, rkey }]);
      await kcp.patch(target, name, { type: "merge", body: { metadata: { finalizers: finalizers.filter((f) => f !== FINALIZER_ATPROTO_RECORD) } } });
      logger.info("atproto_record_deleted", { name, collection, rkey });
      return;
    }
    if (!finalizers.includes(FINALIZER_ATPROTO_RECORD)) {
      await kcp.patch(target, name, { type: "merge", body: { metadata: { finalizers: [...finalizers, FINALIZER_ATPROTO_RECORD] } } });
    }
    const missing: string[] = [];
    const resolved = await resolveValue(o.spec.record ?? {}, missing) as Record<string, unknown>;
    if (missing.length) {
      return await setStatus(o, { phase: "Pending", observedGeneration: o.metadata?.generation }, {
        status: "False",
        reason: "MissingReference",
        message: `waiting for ${missing.join("; ")}`,
      });
    }
    const record = { $type: collection, ...resolved };
    const held = await repo.getRecord(did, collection, rkey);
    if (!held || canonical(held.value) !== canonical(record)) {
      await repo.applyWrites(did, [{ action: held ? "update" : "create", collection, rkey, record }]);
      logger.info("atproto_record_applied", { name, collection, rkey, action: held ? "update" : "create" });
    }
    const now = await repo.getRecord(did, collection, rkey);
    await setStatus(o, {
      phase: now ? "Applied" : "Failed",
      observedGeneration: o.metadata?.generation,
      did,
      uri: now?.uri ?? "",
      cid: String(now?.cid ?? ""),
      message: now ? "" : "the record is not in the repo after the write",
    }, { status: "True", reason: "Resolved", message: "" });
  }

  async function pass(): Promise<void> {
    const list = await kcp.list(target);
    for (const raw of list.items) {
      const o = raw as Held;
      if (o.spec?.repo !== repoName) continue;
      try {
        await reconcile(o);
      } catch (err) {
        logger.warn("atproto_record_reconcile_failed", { name: o.metadata?.name, error: String(err) });
        await setStatus(o, { phase: "Failed", observedGeneration: o.metadata?.generation, message: String(err) }, {
          status: "False",
          reason: "Error",
          message: String(err),
        }).catch(() => {});
      }
    }
  }

  (async () => {
    while (!ac.signal.aborted) {
      await pass().catch((err) => logger.warn("atproto_record_list_failed", { error: String(err) }));
      await new Promise<void>((r) => {
        wake = r;
        // ponytail: a 5 s list poll of the workspace's AtprotoRecords and their references; a kcp watch when the poll costs too much
        const t = setTimeout(r, opts.intervalMs ?? 5_000);
        Deno.unrefTimer(t);
      });
    }
  })();

  return {
    wake: () => wake?.(),
    stop: () => {
      ac.abort();
      wake?.();
    },
  };
}
