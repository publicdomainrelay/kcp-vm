// Net-FS cache restore, drop-in for actions/cache/restore.
//
// Cache data rides the GITHUB_CACHE command file: the engine seeds it with the
// current cache map (a real temp file in full mode, an in-memory virtual file
// in the net-only worker) and reads it back afterwards. Restore only reads it.
// File writes go through the POLICY_ENGINE_FS_API_URL server when present
// (net-only worker) or the real Deno filesystem otherwise.
//
// Supports the full actions/cache/restore surface: key, path, restore-keys,
// fail-on-cache-miss, lookup-only; outputs cache-hit, cache-primary-key,
// cache-matched-key.

interface CacheFile {
  data: string;
  encoding: "text" | "base64";
}
type CacheEntry = Record<string, CacheFile>;
type Cache = Record<string, CacheEntry>;

const inputKey = Deno.env.get("INPUT_KEY") ?? "";
const inputPath = Deno.env.get("INPUT_PATH") ?? "";
const restoreKeysRaw = Deno.env.get("INPUT_RESTORE_KEYS") ?? "";
const failOnCacheMiss = (Deno.env.get("INPUT_FAIL_ON_CACHE_MISS") ?? "").toLowerCase() === "true";
const lookupOnly = (Deno.env.get("INPUT_LOOKUP_ONLY") ?? "").toLowerCase() === "true";
const workspace = Deno.env.get("GITHUB_WORKSPACE") ?? "";
const cachePath = Deno.env.get("GITHUB_CACHE") ?? "";
const outputPath = Deno.env.get("GITHUB_OUTPUT") ?? "";
const fsApiUrl = (Deno.env.get("POLICY_ENGINE_FS_API_URL") ?? "").replace(/\/+$/, "");

if (!inputKey || !inputPath) {
  console.error("::error::cache/restore: key and path inputs are required");
  Deno.exit(1);
}

const { join } = await import("jsr:@std/path");

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
function targetPath(rel: string): string {
  return fsApiUrl ? rel : join(workspace, rel);
}

async function fsWriteFile(rel: string, bytes: Uint8Array): Promise<void> {
  if (fsApiUrl) {
    const r = await fetch(`${fsApiUrl}/file?path=${encodeURIComponent(rel)}`, {
      method: "PUT",
      body: bytes,
    });
    if (!r.ok) throw new Error(`write ${rel}: ${r.status}`);
    return;
  }
  const full = targetPath(rel);
  const dir = full.substring(0, full.lastIndexOf("/"));
  if (dir) await Deno.mkdir(dir, { recursive: true });
  await Deno.writeFile(full, bytes);
}

function base64ToBytes(data: string): Uint8Array {
  const bin = atob(data);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

const cache = readCache();
const restoreKeys = restoreKeysRaw.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);

let matchedKey = "";
let cacheHit = false;
if (Object.prototype.hasOwnProperty.call(cache, inputKey)) {
  matchedKey = inputKey;
  cacheHit = true;
} else if (!lookupOnly) {
  // restore-keys: first prefix match wins (lookup-only skips restore-keys).
  for (const rk of restoreKeys) {
    const found = Object.keys(cache).find((k) => k.startsWith(rk));
    if (found !== undefined) {
      matchedKey = found;
      break;
    }
  }
}

// Outputs are always written, mirroring actions/cache/restore.
if (outputPath) {
  await Deno.writeTextFile(outputPath, `cache-hit=${cacheHit}\n`, { append: true });
  await Deno.writeTextFile(outputPath, `cache-primary-key=${inputKey}\n`, { append: true });
  await Deno.writeTextFile(outputPath, `cache-matched-key=${matchedKey}\n`, { append: true });
}

if (!matchedKey) {
  if (failOnCacheMiss) {
    console.error(`::error::cache/restore: no cache entry found for key "${inputKey}"`);
    Deno.exit(1);
  }
  console.log(`Cache not found for input keys: ${[inputKey, ...restoreKeys].join(", ")}`);
  Deno.exit(0);
}

if (lookupOnly) {
  console.log(`Cache found (lookup-only): ${matchedKey}`);
  Deno.exit(0);
}

const entry = cache[matchedKey];
const rels = Object.keys(entry).sort();
for (const rel of rels) {
  const f = entry[rel];
  try {
    const bytes = f.encoding === "base64" ? base64ToBytes(f.data) : new TextEncoder().encode(f.data);
    await fsWriteFile(rel, bytes);
  } catch (e) {
    console.warn(
      `::warning::cache/restore: failed to restore ${rel}: ${e instanceof Error ? e.message : e}`,
    );
  }
}
console.log(`Cache restored from key: ${matchedKey} (${rels.length} files)`);
