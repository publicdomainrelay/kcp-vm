// Net-FS cache save, drop-in for actions/cache/save.
//
// Cache data rides the GITHUB_CACHE command file: the engine seeds it with the
// current cache map (a real temp file in full mode, an in-memory virtual file
// in the net-only worker) and reads it back afterwards. Save reads the map,
// stores the files under INPUT_KEY (overwriting an existing entry), and writes
// the full updated map back.
// File reads go through the POLICY_ENGINE_FS_API_URL server when present
// (net-only worker) or the real Deno filesystem otherwise. Binary files (NUL
// byte or invalid UTF-8) are base64-encoded; everything else is stored as text.

interface CacheFile {
  data: string;
  encoding: "text" | "base64";
}
type CacheEntry = Record<string, CacheFile>;
type Cache = Record<string, CacheEntry>;

const inputKey = Deno.env.get("INPUT_KEY") ?? "";
const inputPath = Deno.env.get("INPUT_PATH") ?? "";
const workspace = Deno.env.get("GITHUB_WORKSPACE") ?? "";
const cachePath = Deno.env.get("GITHUB_CACHE") ?? "";
const fsApiUrl = (Deno.env.get("POLICY_ENGINE_FS_API_URL") ?? "").replace(/\/+$/, "");

if (!inputKey || !inputPath) {
  console.error("::error::cache/save: key and path inputs are required");
  Deno.exit(1);
}

const { join } = await import("jsr:@std/path");
const { globToRegExp } = await import("jsr:@std/path/posix");

function readCache(): Cache {
  if (!cachePath) return {};
  try {
    const parsed = JSON.parse(Deno.readTextFileSync(cachePath));
    return parsed && typeof parsed === "object" ? (parsed as Cache) : {};
  } catch {
    return {};
  }
}

// Workspace-relative path in net-only (FS API root = workspace), absolute in full mode.
function fullPath(rel: string): string {
  return fsApiUrl ? rel : join(workspace, rel);
}

async function fsType(rel: string): Promise<"file" | "dir" | "none"> {
  if (fsApiUrl) {
    const r = await fetch(`${fsApiUrl}/file?path=${encodeURIComponent(rel)}`);
    if (r.status === 200) return "file";
    if (r.status === 404) return "none";
    return "dir"; // reading a directory as a file fails with 500
  }
  try {
    const s = await Deno.stat(fullPath(rel));
    return s.isDirectory ? "dir" : "file";
  } catch {
    return "none";
  }
}

async function fsList(rel: string): Promise<{ name: string; type: "file" | "dir" }[]> {
  if (fsApiUrl) {
    const r = await fetch(`${fsApiUrl}/ls?path=${encodeURIComponent(rel)}`);
    if (!r.ok) return [];
    return await r.json();
  }
  try {
    const out: { name: string; type: "file" | "dir" }[] = [];
    for await (const e of Deno.readDir(fullPath(rel))) {
      out.push({ name: e.name, type: e.isDirectory ? "dir" : "file" });
    }
    return out;
  } catch {
    return [];
  }
}

/** Recursively list all files under rel, returning workspace-relative paths. */
async function listRecursive(rel: string): Promise<string[]> {
  const t = await fsType(rel);
  if (t === "file") return [rel];
  if (t === "none") return [];
  const out: string[] = [];
  for (const e of await fsList(rel)) {
    const child = rel === "." ? e.name : `${rel}/${e.name}`;
    if (e.type === "dir") out.push(...await listRecursive(child));
    else out.push(child);
  }
  return out;
}

async function fsReadBytes(rel: string): Promise<Uint8Array> {
  if (fsApiUrl) {
    const r = await fetch(`${fsApiUrl}/file?path=${encodeURIComponent(rel)}`);
    if (!r.ok) throw new Error(`read ${rel}: ${r.status}`);
    return new Uint8Array(await r.arrayBuffer());
  }
  return await Deno.readFile(fullPath(rel));
}

function bytesToBase64(bytes: Uint8Array): string {
  let bin = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    bin += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(bin);
}

function encodeFile(bytes: Uint8Array): CacheFile {
  if (bytes.includes(0)) return { encoding: "base64", data: bytesToBase64(bytes) };
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    return { encoding: "text", data: new TextDecoder().decode(bytes) };
  } catch {
    return { encoding: "base64", data: bytesToBase64(bytes) };
  }
}

function hasGlob(p: string): boolean {
  return /[*?[\]]/.test(p);
}

/** Expand a single path line (file, dir, or glob) into matching file paths. */
async function expandPattern(p: string): Promise<string[]> {
  const norm = p.replace(/\/+$/, "") || ".";
  if (!hasGlob(norm)) {
    const t = await fsType(norm);
    if (t === "dir") return listRecursive(norm);
    if (t === "file") return [norm];
    return [];
  }
  const m = norm.search(/[*?[\]]/);
  const base = norm.slice(0, m).replace(/\/+$/, "") || ".";
  const re = globToRegExp(norm, { extended: true, globstar: true });
  const all = await listRecursive(base);
  return all.filter((c) => re.test(c));
}

const pathLines = inputPath.split("\n").map((s) => s.trim()).filter(Boolean);
const matched = new Set<string>();
for (const pl of pathLines) {
  for (const f of await expandPattern(pl)) matched.add(f);
}
const files = [...matched].sort();

const cache = readCache();
if (files.length === 0) {
  console.log("No file paths found in INPUT_PATH, nothing to cache.");
  Deno.exit(0);
}

const entry: CacheEntry = {};
for (const rel of files) {
  try {
    entry[rel] = encodeFile(await fsReadBytes(rel));
  } catch (e) {
    console.warn(
      `::warning::cache/save: skipping ${rel}: ${e instanceof Error ? e.message : e}`,
    );
  }
}
if (Object.keys(entry).length === 0) {
  console.log("No file paths found in INPUT_PATH, nothing to cache.");
  Deno.exit(0);
}

// Overwrite semantics: replace any existing entry for this key.
cache[inputKey] = entry;
if (cachePath) Deno.writeTextFileSync(cachePath, JSON.stringify(cache));
console.log(`Cache saved with key: ${inputKey} (${Object.keys(entry).length} files)`);
