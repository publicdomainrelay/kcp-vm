import type { Context, Hono } from "@hono/hono";
import { encode as cborEncode } from "@ipld/dag-cbor";
import type { LogEntry, Operation, PlcOp, TombstoneOp } from "@publicdomainrelay/did-plc";
import type { PlcStore } from "./storage/plc-store.ts";
import { resolveDidDocument } from "./did-resolution.ts";
import {
  validateOperationStructure,
  verifyOperationSignature,
  validatePrevChain,
  validateRotationKeyAuth,
  computeOperationCid,
} from "./validation.ts";

export interface HandlerDeps {
  store: PlcStore;
  version: string;
  verifySig?: (did: string, data: Uint8Array, sig: Uint8Array) => Promise<boolean>;
}

function didParam(c: Context): string {
  const did = c.req.param("did");
  if (!did) throw new Error("Missing DID parameter");
  return did;
}

function json(c: Context, body: unknown, status = 200): Response {
  return c.json(body, status as Parameters<typeof c.json>[1]);
}

export function mountHandlers(app: Hono, deps: HandlerDeps): void {
  app.get("/health", (c) => handleHealth(c, deps));
  app.get("/export", (c) => handleExport(c, deps));
  app.get("/:did", (c) => handleResolveDid(c, deps));
  app.post("/:did", (c) => handleCreateOp(c, deps));
  app.get("/:did/log", (c) => handleGetLog(c, deps));
  app.get("/:did/log/audit", (c) => handleGetAuditLog(c, deps));
}

async function handleHealth(c: Context, deps: HandlerDeps): Promise<Response> {
  return json(c, { version: deps.version });
}

async function handleResolveDid(c: Context, deps: HandlerDeps): Promise<Response> {
  const did = didParam(c);
  const ops = await deps.store.getCurrentOps(did);

  if (ops.length === 0) {
    return json(c, { message: `DID not registered: ${did}` }, 404);
  }

  const lastOp = ops[ops.length - 1].operation;
  if (lastOp.type === "plc_tombstone") {
    return json(c, { message: `DID not available: ${did}` }, 410);
  }

  const doc = resolveDidDocument(did, ops);
  if (!doc) {
    return json(c, { message: `DID not available: ${did}` }, 410);
  }

  return json(c, doc);
}

async function handleGetLog(c: Context, deps: HandlerDeps): Promise<Response> {
  const did = didParam(c);
  const ops = await deps.store.getCurrentOps(did);

  if (ops.length === 0) {
    return json(c, { message: `DID not registered: ${did}` }, 404);
  }

  return json(c, ops.map((e) => e.operation));
}

async function handleGetAuditLog(c: Context, deps: HandlerDeps): Promise<Response> {
  const did = didParam(c);
  const ops = await deps.store.getAuditLog(did);

  if (ops.length === 0) {
    return json(c, { message: `DID not registered: ${did}` }, 404);
  }

  return json(c, ops);
}

async function handleCreateOp(c: Context, deps: HandlerDeps): Promise<Response> {
  const did = didParam(c);

  let body: unknown;
  try {
    body = await c.req.json();
  } catch {
    return json(c, { message: "Invalid JSON body" }, 400);
  }

  const structErr = validateOperationStructure(body);
  if (structErr) {
    return json(c, { message: structErr }, 400);
  }

  const op = body as Operation;
  const existingOps = await deps.store.getAuditLog(did);
  const currentOps = await deps.store.getCurrentOps(did);

  if (op.type === "plc_operation" || op.type === "plc_tombstone") {
    const prevErr = validatePrevChain(op.prev, existingOps);
    if (prevErr) {
      return json(c, { message: prevErr }, 400);
    }

    if (deps.verifySig) {
      if (op.prev !== null && currentOps.length > 0) {
        const prevOp = currentOps[currentOps.length - 1].operation;
        if (prevOp.type === "plc_operation") {
          let validSig = false;
          for (const key of prevOp.rotationKeys) {
            if (await verifyOperationSignature(op, key, deps.verifySig)) {
              validSig = true;
              break;
            }
          }
          if (!validSig) {
            return json(c, { message: "Invalid Signature" }, 400);
          }
        }
      }
    }
  }

  let cid: string;
  try {
    const signedBytes = cborEncode(op);
    cid = await computeOperationCid(signedBytes);
  } catch {
    return json(c, { message: "Failed to compute operation CID" }, 400);
  }

  if (op.type === "plc_operation" && op.prev !== null && currentOps.length > 0) {
    const prevIdx = currentOps.findIndex((e) => e.cid === op.prev);
    if (prevIdx >= 0 && prevIdx < currentOps.length - 1) {
      const toNullify = currentOps.slice(prevIdx + 1).map((e) => e.cid);
      await deps.store.nullifyOps(did, toNullify);
    }
  }

  const entry: LogEntry = {
    did,
    operation: op,
    cid,
    nullified: false,
    createdAt: new Date().toISOString(),
  };

  await deps.store.insertOp(entry);

  return json(c, entry, 200);
}

async function handleExport(c: Context, deps: HandlerDeps): Promise<Response> {
  const afterStr = c.req.query("after");
  const countStr = c.req.query("count");

  let after: Date | undefined;
  if (afterStr) {
    after = new Date(afterStr);
    if (isNaN(after.getTime())) {
      return json(c, { message: "Invalid Query Parameter: after" }, 400);
    }
  }

  let count: number | undefined;
  if (countStr) {
    count = parseInt(countStr, 10);
    if (isNaN(count) || count < 0) {
      return json(c, { message: "Invalid Query Parameter: count" }, 400);
    }
    count = Math.min(count, 1000);
  }

  const entries = await deps.store.exportLogs(after, count);
  return json(c, entries);
}
