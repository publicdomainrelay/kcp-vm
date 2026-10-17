import type { KcpClient } from "@publicdomainrelay/kcp-client-abc";
import type { Did, RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import {
  ATPROTO_RECORD_RESOURCE,
  type AtprotoRecord,
  type AtprotoRecordStatus,
  FINALIZER_ATPROTO_RECORD,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

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
  declare(name: string, collection: string, rkey: string, record: Record<string, unknown>, labels?: Record<string, string>): Promise<void>;
  waitApplied(name: string, timeoutMs: number): Promise<AtprotoRecordStatus>;
  stop(): void;
}

function canonical(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
  if (v && typeof v === "object") {
    return `{${Object.keys(v as Record<string, unknown>).sort().map((k) => `${JSON.stringify(k)}:${canonical((v as Record<string, unknown>)[k])}`).join(",")}}`;
  }
  return JSON.stringify(v);
}

export function startAtprotoRecordReconciler(opts: AtprotoRecordReconcilerOpts): AtprotoRecordReconciler {
  const { kcp, logger, repo, repoName } = opts;
  const did = opts.did as Did;
  const target = { resource: ATPROTO_RECORD_RESOURCE, workspace: opts.workspace };
  const ac = new AbortController();
  let wake: (() => void) | undefined;

  async function setStatus(o: AtprotoRecord, next: AtprotoRecordStatus): Promise<void> {
    const cur = o.status ?? {};
    if (canonical(cur) === canonical({ ...cur, ...next })) return;
    await kcp.patchStatus(target, o.metadata!.name!, (current) => ({ ...((current as AtprotoRecord)?.status ?? {}), ...next }));
  }

  async function reconcile(o: AtprotoRecord & { spec: NonNullable<AtprotoRecord["spec"]> }): Promise<void> {
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
    let record: Record<string, unknown>;
    try {
      record = { $type: collection, ...JSON.parse(o.spec.record) };
    } catch (err) {
      return await setStatus(o, { phase: "Failed", observedGeneration: o.metadata?.generation, message: `spec.record is not JSON: ${err}` });
    }
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
    });
  }

  async function pass(): Promise<void> {
    const list = await kcp.list(target);
    for (const raw of list.items) {
      const o = raw as AtprotoRecord & { spec: NonNullable<AtprotoRecord["spec"]> };
      if (o.spec?.repo !== repoName) continue;
      try {
        await reconcile(o);
      } catch (err) {
        logger.warn("atproto_record_reconcile_failed", { name: o.metadata?.name, error: String(err) });
        await setStatus(o, { phase: "Failed", observedGeneration: o.metadata?.generation, message: String(err) }).catch(() => {});
      }
    }
  }

  (async () => {
    while (!ac.signal.aborted) {
      await pass().catch((err) => logger.warn("atproto_record_list_failed", { error: String(err) }));
      await new Promise<void>((r) => {
        wake = r;
        const t = setTimeout(r, opts.intervalMs ?? 5_000);
        Deno.unrefTimer(t);
      });
    }
  })();

  return {
    async declare(name, collection, rkey, record, labels) {
      await kcp.createIfAbsent(target, {
        apiVersion: `${ATPROTO_RECORD_RESOURCE.group}/${ATPROTO_RECORD_RESOURCE.version}`,
        kind: ATPROTO_RECORD_RESOURCE.kind,
        metadata: { name, ...(labels ? { labels } : {}) },
        spec: { repo: repoName, collection, rkey, record: JSON.stringify(record) },
      } as never);
      wake?.();
    },
    async waitApplied(name, timeoutMs) {
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline) {
        const o = await kcp.get(target, name) as AtprotoRecord | null;
        if (o?.status?.phase === "Applied" && o.status.observedGeneration === o.metadata?.generation) return o.status;
        if (o?.status?.phase === "Failed") throw new Error(`AtprotoRecord ${name}: ${o.status.message}`);
        wake?.();
        await new Promise((r) => setTimeout(r, 250));
      }
      throw new Error(`AtprotoRecord ${name} was not applied within ${timeoutMs} ms`);
    },
    stop: () => {
      ac.abort();
      wake?.();
    },
  };
}
