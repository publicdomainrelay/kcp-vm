import type { IdentityVault, VaultRef } from "@publicdomainrelay/market-identity-abc";

export interface FileIdentityVaultOptions {
  path: string;
}

const PRIVATE_MODE = 0o600;

function key(ref: VaultRef): string {
  if (!ref.mount || !ref.path) throw new Error(`file vault: a ref needs a mount and a path, got ${JSON.stringify(ref)}`);
  return `${ref.mount}/${ref.path}`;
}

export interface SessionFileIdentityVaultOptions {
  base: IdentityVault;
  ref: VaultRef;
  path: string;
}

async function writePrivate(path: string, text: string): Promise<void> {
  const dir = path.includes("/") ? path.slice(0, path.lastIndexOf("/")) || "/" : ".";
  await Deno.mkdir(dir, { recursive: true, mode: 0o700 });
  const tmp = `${path}.${crypto.randomUUID()}.tmp`;
  await Deno.writeTextFile(tmp, text, { mode: PRIVATE_MODE, createNew: true });
  await Deno.chmod(tmp, PRIVATE_MODE);
  await Deno.rename(tmp, path);
}

export function createSessionFileIdentityVault(opts: SessionFileIdentityVaultOptions): IdentityVault {
  const target = key(opts.ref);
  const mine = (ref: VaultRef) => key(ref) === target;
  return {
    async read(ref) {
      if (!mine(ref)) return await opts.base.read(ref);
      let text: string;
      try {
        text = await Deno.readTextFile(opts.path);
      } catch (err) {
        if (err instanceof Deno.errors.NotFound) return null;
        throw err;
      }
      if (!text.trim()) return null;
      const parsed = JSON.parse(text);
      return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : null;
    },
    async write(ref, value) {
      if (!mine(ref)) return await opts.base.write(ref, value);
      await writePrivate(opts.path, JSON.stringify(value, null, 2) + "\n");
    },
    async remove(ref) {
      if (!mine(ref)) return await opts.base.remove(ref);
      try {
        await Deno.remove(opts.path);
      } catch (err) {
        if (!(err instanceof Deno.errors.NotFound)) throw err;
      }
    },
    async create(ref, value) {
      if (!mine(ref)) return await opts.base.create(ref, value);
      const held = await this.read(ref);
      if (held) return held;
      await writePrivate(opts.path, JSON.stringify(value, null, 2) + "\n");
      return structuredClone(value);
    },
  };
}

export function createFileIdentityVault(opts: FileIdentityVaultOptions): IdentityVault {
  const path = opts.path;
  let queue: Promise<unknown> = Promise.resolve();

  function serialized<T>(fn: () => Promise<T>): Promise<T> {
    const next = queue.then(fn, fn);
    queue = next.catch(() => {});
    return next;
  }

  async function load(): Promise<Record<string, Record<string, unknown>>> {
    let info: Deno.FileInfo;
    try {
      info = await Deno.stat(path);
    } catch (err) {
      if (err instanceof Deno.errors.NotFound) return {};
      throw err;
    }
    if (info.mode !== null && (info.mode & 0o077) !== 0) {
      throw new Error(`file vault ${path}: mode ${(info.mode & 0o777).toString(8)} lets others read it; it must be 600`);
    }
    const text = await Deno.readTextFile(path);
    if (!text.trim()) return {};
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`file vault ${path}: not a JSON object`);
    return parsed as Record<string, Record<string, unknown>>;
  }

  async function save(data: Record<string, Record<string, unknown>>): Promise<void> {
    const dir = path.includes("/") ? path.slice(0, path.lastIndexOf("/")) || "/" : ".";
    await Deno.mkdir(dir, { recursive: true, mode: 0o700 });
    const tmp = `${path}.${crypto.randomUUID()}.tmp`;
    await Deno.writeTextFile(tmp, JSON.stringify(data, null, 2) + "\n", { mode: PRIVATE_MODE, createNew: true });
    await Deno.chmod(tmp, PRIVATE_MODE);
    await Deno.rename(tmp, path);
  }

  return {
    read(ref) {
      const k = key(ref);
      return serialized(async () => {
        const v = (await load())[k];
        return v && typeof v === "object" ? structuredClone(v) : null;
      });
    },
    write(ref, value) {
      const k = key(ref);
      return serialized(async () => {
        const data = await load();
        data[k] = structuredClone(value);
        await save(data);
      });
    },
    remove(ref) {
      const k = key(ref);
      return serialized(async () => {
        const data = await load();
        if (!(k in data)) return;
        delete data[k];
        await save(data);
      });
    },
    create(ref, value) {
      const k = key(ref);
      return serialized(async () => {
        const data = await load();
        const held = data[k];
        if (held && typeof held === "object") return structuredClone(held);
        data[k] = structuredClone(value);
        await save(data);
        return structuredClone(value);
      });
    },
  };
}
