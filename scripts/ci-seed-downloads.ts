const usage = "usage: deno run -A scripts/ci-seed-downloads.ts <binaries base URL, e.g. .../main/<commit>/binaries>";
const base = Deno.args[0];
if (!base || !base.startsWith("https://")) {
  console.error(usage);
  Deno.exit(2);
}

const asset = "socialweb-computer-k8s-downloads";
const root = new URL("..", import.meta.url).pathname;
const versionsText = await Deno.readTextFile(`${root}internal/versions/versions.json`);
const entry = JSON.parse(versionsText).assets[asset];
const arch = Deno.build.arch === "aarch64" ? "arm64" : "amd64";
const plat = `linux/${arch}`;
const pin = entry.platforms[plat];
const home = Deno.env.get("HOME");
const tmp = Deno.env.get("TMPDIR") ?? `${home}/.tmp`;
const blobs = `${home}/.tmp/socialweb-computer-build/dev/assets/linux_${arch}`;

const say = (s: string) => console.error(`ci-seed-downloads: ${s}`);
const hex = (b: ArrayBuffer) => Array.from(new Uint8Array(b), (x) => x.toString(16).padStart(2, "0")).join("");
const sha = async (b: Uint8Array<ArrayBuffer> | string) =>
  hex(await crypto.subtle.digest("SHA-256", typeof b === "string" ? new TextEncoder().encode(b) : b));
const sortKeys = (v: unknown): unknown =>
  Array.isArray(v)
    ? v.map(sortKeys)
    : v && typeof v === "object"
    ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, sortKeys((v as Record<string, unknown>)[k])]))
    : v;

const unpack = async (blob: Uint8Array, dir: string) => {
  const p = new Deno.Command("sh", { args: ["-c", `zstd -dcq | tar -x -C '${dir}'`], stdin: "piped", stderr: "null" })
    .spawn();
  const w = p.stdin.getWriter();
  await w.write(blob).catch(() => {});
  await w.close().catch(() => {});
  return (await p.status).success;
};

const treeDigest = async (dir: string) => {
  const names: string[] = [];
  for await (const e of Deno.readDir(dir)) {
    if (!e.isFile) return "";
    names.push(e.name);
  }
  let s = "";
  for (const n of names.sort()) {
    const st = await Deno.stat(`${dir}/${n}`);
    s += `f ${(st.mode! & 0o111) ? "x" : "-"} ${await sha(await Deno.readFile(`${dir}/${n}`))} ${n}\n`;
  }
  return sha(s);
};

const digestOf = async (blob: Uint8Array) => {
  const dir = await Deno.makeTempDir({ dir: tmp, prefix: "seed-" });
  try {
    return (await unpack(blob, dir)) ? await treeDigest(dir) : "";
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
};

const key = (await sha(JSON.stringify(sortKeys(entry)) + "\n")).slice(0, 16);
const target = `${blobs}/${asset}-${key}.tar.zst`;
const current = await Deno.readFile(target).catch(() => undefined);
if (current && await digestOf(current) === pin.treeSha256) {
  say(`${target} unpacks to the pinned tree ${pin.treeSha256}; kept`);
  Deno.exit(0);
}

const name = `socialweb-computer-linux-${arch}`;
const sums = await fetch(`${base}/SHA256SUMS`, { signal: AbortSignal.timeout(30_000) });
if (!sums.ok) throw new Error(`${base}/SHA256SUMS: ${sums.status}`);
const want = (await sums.text()).split("\n").find((l) => l.trim().endsWith(` ${name}`))?.split(/\s+/)[0];
if (!want) throw new Error(`${base}/SHA256SUMS lists no ${name}`);
const res = await fetch(`${base}/${name}`, { signal: AbortSignal.timeout(300_000) });
if (!res.ok) throw new Error(`${base}/${name}: ${res.status}`);
const bin = new Uint8Array(await res.arrayBuffer());
if (await sha(bin) !== want) throw new Error(`${name} does not match its SHA256SUMS line`);

for (let i = 0; i + pin.zstdBytes <= bin.length; i++) {
  if (bin[i] !== 0x28 || bin[i + 1] !== 0xb5 || bin[i + 2] !== 0x2f || bin[i + 3] !== 0xfd) continue;
  const cand = bin.subarray(i, i + pin.zstdBytes);
  if (await digestOf(cand) !== pin.treeSha256) continue;
  await Deno.mkdir(blobs, { recursive: true });
  await Deno.writeFile(`${target}.new`, cand);
  await Deno.rename(`${target}.new`, target);
  say(`${target}: the pinned blob (tree ${pin.treeSha256}) from ${base}/${name}`);
  Deno.exit(0);
}
throw new Error(`${base}/${name} embeds no ${asset} blob with the pinned tree ${pin.treeSha256}`);
