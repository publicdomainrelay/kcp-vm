import type { PinnedDownload, PinnedDownloads } from "@publicdomainrelay/socialweb-computer-k8s-abc";

export const SIDECAR_SUFFIX = ".sha256";

const DOWNLOAD_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const SHA256SUM_LINE = /^([0-9a-f]{64}) [ *](\S+)$/;

async function sha256Hex(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

async function fileNames(dir: string): Promise<string[]> {
  try {
    const out: string[] = [];
    for await (const entry of Deno.readDir(dir)) if (entry.isFile) out.push(entry.name);
    return out.sort();
  } catch (err) {
    if (err instanceof Deno.errors.NotFound) return [];
    throw err;
  }
}

export async function loadPinnedDownloads(dir: string): Promise<PinnedDownloads> {
  const held: { meta: PinnedDownload; bytes: Uint8Array<ArrayBuffer> }[] = [];
  for (const sidecar of (await fileNames(dir)).filter((n) => n.endsWith(SIDECAR_SUFFIX))) {
    const name = sidecar.slice(0, -SIDECAR_SUFFIX.length);
    if (!DOWNLOAD_NAME.test(name)) throw new Error(`${dir}/${sidecar} pins ${JSON.stringify(name)}, which is not a download name`);
    const line = (await Deno.readTextFile(`${dir}/${sidecar}`)).trim();
    const match = SHA256SUM_LINE.exec(line);
    if (!match) throw new Error(`${dir}/${sidecar} is not one sha256sum line`);
    if (match[2] !== name) throw new Error(`${dir}/${sidecar} names ${match[2]}, not ${name}`);
    const bytes = await Deno.readFile(`${dir}/${name}`);
    const got = await sha256Hex(bytes);
    if (got !== match[1]) throw new Error(`${dir}/${name} has sha256 ${got}, but ${sidecar} pins ${match[1]}`);
    held.push({ meta: { name, sha256: got, size: bytes.length }, bytes });
  }
  return {
    list: () => held.map((h) => ({ ...h.meta })),
    get(sha256, name) {
      return held.find((h) => h.meta.sha256 === sha256 && h.meta.name === name)?.bytes ?? null;
    },
  };
}
