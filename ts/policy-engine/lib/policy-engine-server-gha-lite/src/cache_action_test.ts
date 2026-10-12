// Action-level tests for the bundled net-FS cache save/restore actions.
//
// The actions are driven directly through runActionInWorker (which overrides
// GITHUB_CACHE / GITHUB_OUTPUT with in-memory sentinels) while file I/O hits a
// real FS API server rooted at a temp directory, mirroring the net-only worker
// sandbox. Cache is seeded via the `cache` param and read back from `res.cache`.

import { assertEquals, assertStringIncludes } from "@std/assert";
import { join } from "@std/path";
import { runActionInWorker } from "./action_worker.ts";
import { startFsApiServer } from "./fs_api.ts";

const saveIndex = new URL("../bundled-actions/actions/cache/save/index.ts", import.meta.url);
const restoreIndex = new URL("../bundled-actions/actions/cache/restore/index.ts", import.meta.url);

async function runAction(
  source: URL,
  env: Record<string, string>,
  cache: unknown,
): Promise<{ code: number; output: string; cache: string }> {
  const src = await Deno.readTextFile(source);
  return runActionInWorker({
    source: src,
    env,
    allowNet: true,
    cache: JSON.stringify(cache),
  });
}

Deno.test("cache/save: stores text files under the key", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    await fetch(`${srv.url}/mkdir?path=dist`, { method: "POST" });
    await fetch(`${srv.url}/file?path=dist/app.txt`, {
      method: "PUT",
      body: new TextEncoder().encode("hello world"),
    });

    const res = await runAction(
      saveIndex,
      { INPUT_KEY: "build-1", INPUT_PATH: "dist", POLICY_ENGINE_FS_API_URL: srv.url, GITHUB_WORKSPACE: root },
      {},
    );
    assertEquals(res.code, 0);
    assertEquals(JSON.parse(res.cache), {
      "build-1": { "dist/app.txt": { data: "hello world", encoding: "text" } },
    });
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/save: base64-encodes binary files", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    const bytes = new Uint8Array([0, 1, 2, 255]);
    await fetch(`${srv.url}/file?path=bin.dat`, { method: "PUT", body: bytes });

    const res = await runAction(
      saveIndex,
      { INPUT_KEY: "k", INPUT_PATH: "bin.dat", POLICY_ENGINE_FS_API_URL: srv.url, GITHUB_WORKSPACE: root },
      {},
    );
    assertEquals(res.code, 0);
    const entry = JSON.parse(res.cache)["k"]["bin.dat"] as { data: string; encoding: string };
    assertEquals(entry.encoding, "base64");
    const decoded = Uint8Array.from(atob(entry.data), (c) => c.charCodeAt(0));
    assertEquals(decoded, bytes);
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/save: expands glob patterns", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    await fetch(`${srv.url}/mkdir?path=dist/sub`, { method: "POST" });
    await fetch(`${srv.url}/file?path=dist/a.txt`, {
      method: "PUT",
      body: new TextEncoder().encode("a"),
    });
    await fetch(`${srv.url}/file?path=dist/sub/b.txt`, {
      method: "PUT",
      body: new TextEncoder().encode("b"),
    });

    const res = await runAction(
      saveIndex,
      { INPUT_KEY: "k", INPUT_PATH: "dist/**/*.txt", POLICY_ENGINE_FS_API_URL: srv.url, GITHUB_WORKSPACE: root },
      {},
    );
    assertEquals(res.code, 0);
    const entry = JSON.parse(res.cache)["k"] as Record<string, unknown>;
    assertEquals(Object.keys(entry).sort(), ["dist/a.txt", "dist/sub/b.txt"]);
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/restore: exact key hit writes files and sets cache-hit=true", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    const cache = { v1: { "dist/app.txt": { data: "hi", encoding: "text" as const } } };
    const res = await runAction(
      restoreIndex,
      { INPUT_KEY: "v1", INPUT_PATH: "dist", POLICY_ENGINE_FS_API_URL: srv.url, GITHUB_WORKSPACE: root },
      cache,
    );
    assertEquals(res.code, 0);
    assertStringIncludes(res.output, "cache-hit=true");
    assertStringIncludes(res.output, "cache-primary-key=v1");
    assertStringIncludes(res.output, "cache-matched-key=v1");
    assertEquals(await Deno.readTextFile(join(root, "dist/app.txt")), "hi");
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/restore: restore-keys prefix match sets cache-hit=false", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    const cache = { "build-abc": { "dist/app.txt": { data: "stale", encoding: "text" as const } } };
    const res = await runAction(
      restoreIndex,
      {
        INPUT_KEY: "build-xyz",
        INPUT_PATH: "dist",
        INPUT_RESTORE_KEYS: "build-",
        POLICY_ENGINE_FS_API_URL: srv.url,
        GITHUB_WORKSPACE: root,
      },
      cache,
    );
    assertEquals(res.code, 0);
    assertStringIncludes(res.output, "cache-hit=false");
    assertStringIncludes(res.output, "cache-matched-key=build-abc");
    assertEquals(await Deno.readTextFile(join(root, "dist/app.txt")), "stale");
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/restore: fail-on-cache-miss exits 1 on a miss", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    const res = await runAction(
      restoreIndex,
      {
        INPUT_KEY: "nope",
        INPUT_PATH: "dist",
        INPUT_FAIL_ON_CACHE_MISS: "true",
        POLICY_ENGINE_FS_API_URL: srv.url,
        GITHUB_WORKSPACE: root,
      },
      {},
    );
    assertEquals(res.code, 1);
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("cache/restore: lookup-only checks existence without writing files", async () => {
  const root = await Deno.makeTempDir({ prefix: "pe-cache-" });
  const srv = await startFsApiServer(root);
  try {
    const cache = { v1: { "dist/app.txt": { data: "hi", encoding: "text" as const } } };
    const res = await runAction(
      restoreIndex,
      {
        INPUT_KEY: "v1",
        INPUT_PATH: "dist",
        INPUT_LOOKUP_ONLY: "true",
        POLICY_ENGINE_FS_API_URL: srv.url,
        GITHUB_WORKSPACE: root,
      },
      cache,
    );
    assertEquals(res.code, 0);
    assertStringIncludes(res.output, "cache-hit=true");
    // Nothing written under the FS API root.
    let count = 0;
    for await (const _ of Deno.readDir(root)) count++;
    assertEquals(count, 0);
  } finally {
    await srv.close();
    await Deno.remove(root, { recursive: true });
  }
});
