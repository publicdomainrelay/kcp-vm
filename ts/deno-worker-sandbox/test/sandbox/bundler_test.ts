import { assertEquals } from "@std/assert";
import { createDenoBundler } from "@publicdomainrelay/sandbox-deno";

const VALID_DENO_JSON = JSON.stringify({
  imports: {},
  fmt: { lineWidth: 100 },
});

const VALID_SOURCE = 'console.log("hello from bundle");';

Deno.test("Bundler produces bundle.js from valid input", async () => {
  const bundler = createDenoBundler();

  const result = await bundler.bundle({
    denoJson: VALID_DENO_JSON,
    source: VALID_SOURCE,
  });

  assertEquals(result.bundleJs.length > 0, true);
  assertEquals(result.bundleJs.includes("hello from bundle"), true);
});

Deno.test("Bundler returns stderr on invalid source", async () => {
  const bundler = createDenoBundler();

  const result = await bundler.bundle({
    denoJson: VALID_DENO_JSON,
    source: "}{ invalid syntax",
  });

  assertEquals(result.bundleJs, "");
  assertEquals(result.stderr.length > 0, true);
});
