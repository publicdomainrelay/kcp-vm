import type {
  ContractEvent,
  ContractStore,
  StoredContract,
  StoredContracts,
  Tombstone,
} from "@publicdomainrelay/market-bidder-abc";
import { DEFAULT_TERMINATED_RECEIPTS_CAPACITY, storedContractOf } from "@publicdomainrelay/market-bidder-abc";

export const DEFAULT_TOMBSTONE_TTL_MS = 30 * 24 * 60 * 60 * 1000;

const STORE_VERSION = 1;
const CONTRACTS = "contracts";
const TOMBSTONES = "tombstones";
const FILE_MODE = 0o600;
const DIR_MODE = 0o700;

export interface FsContractStoreOptions {
  dir: string;
  tombstoneTtlMs?: number;
  tombstoneCapacity?: number;
  now?: () => number;
}

export class ContractStoreCorruptError extends Error {
  constructor(readonly path: string, detail: string) {
    super(
      `contract store file ${path} is unreadable (${detail}). The bidder refuses to start rather than ` +
        `forget the contracts and tombstones it holds: a contract dropped here is a guest that keeps ` +
        `running after its requester's vm.delete is answered "unknown receipt". Inspect or restore the ` +
        `file, or move it aside deliberately, then start the bidder again.`,
    );
    this.name = "ContractStoreCorruptError";
  }
}

function fileNameOf(receiptUri: string): string {
  return `${encodeURIComponent(receiptUri)}.json`;
}

function isStoreFile(name: string): boolean {
  return !name.startsWith(".") && name.endsWith(".json");
}

function isTempFile(name: string): boolean {
  return name.startsWith(".") && name.includes(".tmp-");
}

async function syncDir(dir: string): Promise<void> {
  let handle: Deno.FsFile | undefined;
  try {
    handle = await Deno.open(dir, { read: true });
    await handle.sync();
  } catch {
    return;
  } finally {
    handle?.close();
  }
}

async function writeAtomic(dir: string, name: string, value: unknown): Promise<void> {
  const tmp = `${dir}/.${name}.tmp-${crypto.randomUUID()}`;
  const bytes = new TextEncoder().encode(JSON.stringify(value, null, 2) + "\n");
  const file = await Deno.open(tmp, { write: true, createNew: true, mode: FILE_MODE });
  try {
    let written = 0;
    while (written < bytes.length) written += await file.write(bytes.subarray(written));
    await file.sync();
  } finally {
    file.close();
  }
  try {
    await Deno.chmod(tmp, FILE_MODE);
    await Deno.rename(tmp, `${dir}/${name}`);
  } catch (err) {
    await Deno.remove(tmp).catch(() => {});
    throw err;
  }
  await syncDir(dir);
}

async function removeFile(dir: string, name: string): Promise<boolean> {
  try {
    await Deno.remove(`${dir}/${name}`);
  } catch (err) {
    if (err instanceof Deno.errors.NotFound) return false;
    throw err;
  }
  await syncDir(dir);
  return true;
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch (err) {
    if (err instanceof Deno.errors.NotFound) return false;
    throw err;
  }
}

function isString(v: unknown): v is string {
  return typeof v === "string" && v.length > 0;
}

function optionalString(record: Record<string, unknown>, field: string, path: string): void {
  if (record[field] !== undefined && typeof record[field] !== "string") {
    throw new ContractStoreCorruptError(path, `${field} is not a string`);
  }
}

function parseEnvelope(raw: string, path: string, field: "contract" | "tombstone"): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new ContractStoreCorruptError(path, `not JSON: ${String(err)}`);
  }
  const envelope = parsed as Record<string, unknown> | null;
  if (!envelope || typeof envelope !== "object") throw new ContractStoreCorruptError(path, "not an object");
  if (envelope.version !== STORE_VERSION) {
    throw new ContractStoreCorruptError(path, `version ${String(envelope.version)}, expected ${STORE_VERSION}`);
  }
  const body = envelope[field] as Record<string, unknown> | undefined;
  if (!body || typeof body !== "object") throw new ContractStoreCorruptError(path, `no ${field}`);
  return body;
}

function parseContract(raw: string, path: string): StoredContract {
  const c = parseEnvelope(raw, path, "contract");
  for (const field of ["key", "receiptUri", "receiptCid", "acceptAuthor", "acceptedAt"]) {
    if (!isString(c[field])) throw new ContractStoreCorruptError(path, `missing ${field}`);
  }
  for (const field of ["acceptUri", "acceptCid", "provider", "submitEventUrl"]) optionalString(c, field, path);
  if (c.providerId !== undefined && typeof c.providerId !== "string" && typeof c.providerId !== "number") {
    throw new ContractStoreCorruptError(path, "providerId is neither a string nor a number");
  }
  if (`${fileNameOf(c.receiptUri as string)}` !== path.slice(path.lastIndexOf("/") + 1)) {
    throw new ContractStoreCorruptError(path, `holds receipt ${c.receiptUri}, which belongs in another file`);
  }
  return c as unknown as StoredContract;
}

function parseTombstone(raw: string, path: string): Tombstone {
  const t = parseEnvelope(raw, path, "tombstone");
  for (const field of ["receiptUri", "acceptAuthor", "reason", "at"]) {
    if (!isString(t[field])) throw new ContractStoreCorruptError(path, `missing ${field}`);
  }
  if (!Number.isFinite(Date.parse(t.at as string))) throw new ContractStoreCorruptError(path, "at is not a time");
  for (const field of ["receiptCid", "acceptUri", "acceptCid"]) optionalString(t, field, path);
  return t as unknown as Tombstone;
}

async function storeFiles(dir: string): Promise<string[]> {
  const names: string[] = [];
  for await (const entry of Deno.readDir(dir)) {
    if (!entry.isFile) continue;
    if (isTempFile(entry.name)) {
      await Deno.remove(`${dir}/${entry.name}`).catch(() => {});
      continue;
    }
    if (isStoreFile(entry.name)) names.push(entry.name);
  }
  return names.sort();
}

export function createFsContractStore(opts: FsContractStoreOptions): ContractStore {
  const contractsDir = `${opts.dir}/${CONTRACTS}`;
  const tombstonesDir = `${opts.dir}/${TOMBSTONES}`;
  const ttlMs = opts.tombstoneTtlMs ?? DEFAULT_TOMBSTONE_TTL_MS;
  const capacity = opts.tombstoneCapacity ?? DEFAULT_TERMINATED_RECEIPTS_CAPACITY;
  const now = opts.now ?? Date.now;
  const queues = new Map<string, Promise<void>>();
  let ready: Promise<void> | undefined;

  const ensureDirs = (): Promise<void> => {
    ready ??= (async () => {
      await Deno.mkdir(contractsDir, { recursive: true, mode: DIR_MODE });
      await Deno.mkdir(tombstonesDir, { recursive: true, mode: DIR_MODE });
      await Deno.chmod(opts.dir, DIR_MODE);
      await Deno.chmod(contractsDir, DIR_MODE);
      await Deno.chmod(tombstonesDir, DIR_MODE);
    })();
    return ready;
  };

  const serial = (receiptUri: string, fn: () => Promise<void>): Promise<void> => {
    const prev = queues.get(receiptUri) ?? Promise.resolve();
    const next = prev.catch(() => {}).then(async () => {
      await ensureDirs();
      await fn();
    });
    const tracked = next.finally(() => {
      if (queues.get(receiptUri) === tracked) queues.delete(receiptUri);
    });
    queues.set(receiptUri, tracked);
    return next;
  };

  async function load(): Promise<StoredContracts> {
    await ensureDirs();
    const cutoff = now() - ttlMs;
    const tombstones: Tombstone[] = [];
    for (const name of await storeFiles(tombstonesDir)) {
      const path = `${tombstonesDir}/${name}`;
      const t = parseTombstone(await Deno.readTextFile(path), path);
      if (Date.parse(t.at) < cutoff) {
        await removeFile(tombstonesDir, name);
        continue;
      }
      tombstones.push(t);
    }
    tombstones.sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
    while (tombstones.length > capacity) {
      const oldest = tombstones.shift()!;
      await removeFile(tombstonesDir, fileNameOf(oldest.receiptUri));
    }
    const dead = new Set(tombstones.map((t) => t.receiptUri));

    const contracts: StoredContract[] = [];
    for (const name of await storeFiles(contractsDir)) {
      const path = `${contractsDir}/${name}`;
      const c = parseContract(await Deno.readTextFile(path), path);
      if (dead.has(c.receiptUri)) {
        await removeFile(contractsDir, name);
        continue;
      }
      contracts.push(c);
    }
    contracts.sort((a, b) => a.acceptedAt.localeCompare(b.acceptedAt));
    return { contracts, tombstones };
  }

  function save(event: ContractEvent): Promise<void> {
    const name = fileNameOf(event.receiptUri);
    return serial(event.receiptUri, async () => {
      if (event.type === "terminated") {
        await removeFile(contractsDir, name);
        return;
      }
      if (await exists(`${tombstonesDir}/${name}`)) return;
      const path = `${contractsDir}/${name}`;
      let previous: StoredContract | undefined;
      try {
        previous = parseContract(await Deno.readTextFile(path), path);
      } catch (err) {
        if (!(err instanceof Deno.errors.NotFound)) throw err;
      }
      const next: StoredContract = { ...(previous ?? {}), ...storedContractOf(event) };
      await writeAtomic(contractsDir, name, { version: STORE_VERSION, contract: next });
    });
  }

  function saveTombstone(tombstone: Tombstone): Promise<void> {
    return serial(tombstone.receiptUri, async () => {
      await writeAtomic(tombstonesDir, fileNameOf(tombstone.receiptUri), {
        version: STORE_VERSION,
        tombstone,
      });
    });
  }

  return { load, save, saveTombstone };
}
