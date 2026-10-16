import { assertEquals, assertRejects } from "@std/assert";
import { createPinnedDownloadsFactory } from "@publicdomainrelay/hono-factory-socialweb-computer-k8s";
import { loadPinnedDownloads } from "@publicdomainrelay/socialweb-computer-k8s-downloads-fs";
import { meshdPinOf } from "@publicdomainrelay/socialweb-computer-k8s-node-market";

async function sha256Hex(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

async function shipped(files: Record<string, Uint8Array<ArrayBuffer>>, sidecar: (name: string, sha: string) => string | null) {
  const dir = await Deno.makeTempDir();
  for (const [name, bytes] of Object.entries(files)) {
    await Deno.writeFile(`${dir}/${name}`, bytes);
    const side = sidecar(name, await sha256Hex(bytes));
    if (side !== null) await Deno.writeTextFile(`${dir}/${name}.sha256`, side);
  }
  return dir;
}

const X86 = new Uint8Array(Array.from({ length: 70_000 }, (_, i) => (i * 7) % 256));
const ARM = new TextEncoder().encode("aarch64 meshd");
const HELPER = new TextEncoder().encode("credential helper, no sidecar");

Deno.test("the door serves a pinned download's exact bytes at /downloads/<sha256>/<name>", async () => {
  const dir = await shipped(
    { "socialweb-meshd-x86_64": X86, "socialweb-meshd-aarch64": ARM, "socialweb-fixture-linux-amd64": HELPER },
    (name, sha) => name.startsWith("socialweb-meshd") ? `${sha}  ${name}\n` : null,
  );
  try {
    const downloads = await loadPinnedDownloads(dir);
    const pin = await sha256Hex(X86);
    assertEquals(downloads.list().map((d) => d.name), ["socialweb-meshd-aarch64", "socialweb-meshd-x86_64"]);
    const app = createPinnedDownloadsFactory({ downloads }).createApp();

    const res = await app.request(`/downloads/${pin}/socialweb-meshd-x86_64`);
    assertEquals(res.status, 200);
    const body = new Uint8Array(await res.arrayBuffer());
    assertEquals(body, X86);
    assertEquals(await sha256Hex(body), pin);
    assertEquals(res.headers.get("content-type"), "application/octet-stream");
    assertEquals(res.headers.get("content-length"), String(X86.length));
    assertEquals(res.headers.get("etag"), `"${pin}"`);
    assertEquals(res.headers.get("cache-control"), "public, max-age=31536000, immutable");

    assertEquals((await app.request(`/downloads/${pin}/socialweb-meshd-aarch64`)).status, 404);
    assertEquals((await app.request(`/downloads/${"0".repeat(64)}/socialweb-meshd-x86_64`)).status, 404);
    assertEquals((await app.request(`/downloads/${await sha256Hex(HELPER)}/socialweb-fixture-linux-amd64`)).status, 404);
    assertEquals((await app.request(`/downloads/${pin.toUpperCase()}/socialweb-meshd-x86_64`)).status, 404);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("a pinned download whose bytes are not its sidecar's sha256 refuses the door's start", async () => {
  const dir = await shipped({ "socialweb-meshd-x86_64": X86 }, (name) => `${"a".repeat(64)}  ${name}\n`);
  try {
    await assertRejects(() => loadPinnedDownloads(dir), Error, "socialweb-meshd-x86_64");
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("a sidecar that names another file refuses the door's start", async () => {
  const dir = await shipped({ "socialweb-meshd-x86_64": X86 }, (_name, sha) => `${sha}  socialweb-meshd-aarch64\n`);
  try {
    await assertRejects(() => loadPinnedDownloads(dir), Error, "names");
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("a door with no downloads directory serves nothing and offers no meshd", async () => {
  const downloads = await loadPinnedDownloads(`${await Deno.makeTempDir()}/missing`);
  assertEquals(downloads.list(), []);
  assertEquals(meshdPinOf(downloads.list(), "https://door.test"), null);
});

Deno.test("the meshd pin is the shipped pair's sha256 at the door's public origin, and needs both arches", async () => {
  const both = [
    { name: "socialweb-meshd-x86_64", sha256: "1".repeat(64), size: 1 },
    { name: "socialweb-meshd-aarch64", sha256: "2".repeat(64), size: 1 },
    { name: "socialweb-fixture-linux-amd64", sha256: "3".repeat(64), size: 1 },
  ];
  assertEquals(meshdPinOf(both, "https://socialweb.computer"), {
    downloadBase: "https://socialweb.computer",
    sha256: { x86_64: "1".repeat(64), aarch64: "2".repeat(64) },
  });
  assertEquals(meshdPinOf(both.slice(0, 1), "https://socialweb.computer"), null);
  assertEquals(meshdPinOf(both, undefined), null);
});
