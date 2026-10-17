import type { PdsWrite, PdsWriteBudget, WriteAction } from "@publicdomainrelay/pds-write-budget-abc";

const WRITE_PATH = /\/xrpc\/com\.atproto\.repo\.(createRecord|putRecord|deleteRecord|applyWrites)$/;

const SINGLE_WRITE_ACTION: Readonly<Record<string, WriteAction>> = {
  createRecord: "create",
  putRecord: "update",
  deleteRecord: "delete",
};

function actionOfOp(type: unknown): WriteAction | undefined {
  const tail = String(type).split("#").pop();
  return tail === "create" || tail === "update" || tail === "delete" ? tail : undefined;
}

export function pdsWritesOf(nsid: string, body: unknown): PdsWrite[] {
  const json = body as { collection?: unknown; writes?: unknown } | null;
  const single = SINGLE_WRITE_ACTION[nsid];
  if (single) return typeof json?.collection === "string" ? [{ collection: json.collection, action: single }] : [];
  if (!Array.isArray(json?.writes)) return [];
  const out: PdsWrite[] = [];
  for (const op of json.writes as Array<{ $type?: unknown; collection?: unknown }>) {
    const action = actionOfOp(op.$type);
    if (action && typeof op.collection === "string") out.push({ collection: op.collection, action });
  }
  return out;
}

export function observePdsWrites(budget: PdsWriteBudget, base: typeof fetch = globalThis.fetch): typeof fetch {
  return async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
    const match = method === "POST" ? WRITE_PATH.exec(url.pathname) : null;
    if (!match) return await base(input, init);
    let writes: PdsWrite[] = [];
    try {
      const text = typeof init?.body === "string" ? init.body : input instanceof Request ? await input.clone().text() : "";
      if (text) writes = pdsWritesOf(match[1], JSON.parse(text));
    } catch {
      writes = [];
    }
    const res = await base(input, init);
    budget.observe({ pds: url.origin, status: res.status, headers: res.headers, writes });
    return res;
  };
}

export function installPdsWriteObserver(budget: PdsWriteBudget): void {
  globalThis.fetch = observePdsWrites(budget, globalThis.fetch);
}
