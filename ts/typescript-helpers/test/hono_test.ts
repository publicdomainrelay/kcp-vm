import { assertEquals, assertStringIncludes } from "@std/assert";
import { Hono } from "@hono/hono";
import { httpStatusOf, registerErrorMiddleware } from "@publicdomainrelay/hono-error-middleware";
import { HTTPError } from "@publicdomainrelay/http-error";
import { createStaticFilesApp } from "@publicdomainrelay/hono-factory-static-files-fs";
import { EventBus } from "@publicdomainrelay/event-bus";
import type { StaticFileEvent } from "@publicdomainrelay/hono-factory-static-files-fs";
import { noopLoggerInterface } from "@publicdomainrelay/logger";
import { Command } from "@publicdomainrelay/cli-args-env";
import type { CliArgsEnv } from "@publicdomainrelay/cli-args-env";

// --- httpStatusOf ---

Deno.test("httpStatusOf returns status for valid range", () => {
  assertEquals(httpStatusOf({ status: 404 }), 404);
  assertEquals(httpStatusOf({ status: 500 }), 500);
  assertEquals(httpStatusOf({ status: 400 }), 400);
  assertEquals(httpStatusOf({ status: 599 }), 599);
});

Deno.test("httpStatusOf returns undefined for status outside range", () => {
  assertEquals(httpStatusOf({ status: 200 }), undefined);
  assertEquals(httpStatusOf({ status: 399 }), undefined);
  assertEquals(httpStatusOf({ status: 600 }), undefined);
});

Deno.test("httpStatusOf returns undefined for non-objects", () => {
  assertEquals(httpStatusOf(null), undefined);
  assertEquals(httpStatusOf("string"), undefined);
  assertEquals(httpStatusOf(42), undefined);
  assertEquals(httpStatusOf(undefined), undefined);
  assertEquals(httpStatusOf({}), undefined);
});

// --- registerErrorMiddleware ---

Deno.test("registerErrorMiddleware: HTTPError returns JSON with status", async () => {
  const app = new Hono();
  registerErrorMiddleware(app, noopLoggerInterface);
  app.get("/boom", () => {
    throw new HTTPError(403, "forbidden");
  });
  const res = await app.request("/boom");
  assertEquals(res.status, 403);
  const body = await res.json();
  assertEquals(body, { error: "http_error", code: 403, detail: "forbidden" });
});

Deno.test("registerErrorMiddleware: plain Error returns 500", async () => {
  const app = new Hono();
  registerErrorMiddleware(app, noopLoggerInterface);
  app.get("/boom", () => {
    throw new Error("something broke");
  });
  const res = await app.request("/boom");
  assertEquals(res.status, 500);
  const body = await res.json();
  assertEquals(body.error, "internal");
  assertEquals(body.detail, "something broke");
});

Deno.test("registerErrorMiddleware: Error with status property returns http_error", async () => {
  const app = new Hono();
  registerErrorMiddleware(app, noopLoggerInterface);
  app.get("/boom", () => {
    const err = new Error("rate limited") as Error & { status: number };
    err.status = 429;
    throw err;
  });
  const res = await app.request("/boom");
  assertEquals(res.status, 429);
  const body = await res.json();
  assertEquals(body, { error: "http_error", code: 429, detail: "rate limited" });
});

// --- createStaticFilesApp ---

Deno.test("createStaticFilesApp serves existing file", async () => {
  const tmp = Deno.makeTempDirSync({ prefix: "static-test-" });
  Deno.writeTextFileSync(`${tmp}/hello.txt`, "hello world");
  const bus = new EventBus<StaticFileEvent>();
  try {
    const app = createStaticFilesApp(tmp, noopLoggerInterface, bus);
    const res = await app.request("/hello.txt");
    assertEquals(res.status, 200);
    const text = await res.text();
    assertEquals(text, "hello world");
  } finally {
    Deno.removeSync(tmp, { recursive: true });
  }
});

Deno.test("createStaticFilesApp returns 404 for missing file", async () => {
  const tmp = Deno.makeTempDirSync({ prefix: "static-test-" });
  const bus = new EventBus<StaticFileEvent>();
  try {
    const app = createStaticFilesApp(tmp, noopLoggerInterface, bus);
    const res = await app.request("/nonexistent.txt");
    assertEquals(res.status, 404);
    const body = await res.json();
    assertEquals(body.error, "http_error");
    assertEquals(body.code, 404);
  } finally {
    Deno.removeSync(tmp, { recursive: true });
  }
});

Deno.test("createStaticFilesApp publishes events on 404", async () => {
  const tmp = Deno.makeTempDirSync({ prefix: "static-test-" });
  const bus = new EventBus<StaticFileEvent>();
  const events: StaticFileEvent[] = [];
  bus.subscribe((e) => events.push(e));
  try {
    const app = createStaticFilesApp(tmp, noopLoggerInterface, bus);
    await app.request("/nonexistent.txt");
    const notFoundEvents = events.filter((e) => e.type === "file-not-found");
    assertEquals(notFoundEvents.length, 1);
    assertEquals(notFoundEvents[0], { type: "file-not-found", path: "/nonexistent.txt" });
  } finally {
    Deno.removeSync(tmp, { recursive: true });
  }
});

// --- Command.resolve priority chain ---

const TEST_ARGS_ENV: CliArgsEnv = {
  name: "test-cli",
  options: {
    "port": { type: "number", env: "TEST_PORT", default: 4000 },
    "host": { type: "string", env: "TEST_HOST", default: "localhost" },
    "verbose": { type: "boolean" },
  },
};

Deno.test("Command.resolve uses default when nothing set", async () => {
  const cmd = new Command("TEST_CONFIG_PATH_NONE", TEST_ARGS_ENV, null, []);
  await cmd.resolve();
  assertEquals(cmd.options.port, 4000);
  assertEquals(cmd.options.host, "localhost");
});

Deno.test("Command.resolve CLI flag overrides default", async () => {
  const cmd = new Command("TEST_CONFIG_PATH_CLI", TEST_ARGS_ENV, null, ["--port", "9000"]);
  await cmd.resolve();
  assertEquals(cmd.options.port, 9000);
});

Deno.test("Command.resolve env var overrides default", async () => {
  Deno.env.set("TEST_PORT", "3000");
  try {
    const cmd = new Command("TEST_CONFIG_PATH_ENV", TEST_ARGS_ENV, null, []);
    await cmd.resolve();
    assertEquals(cmd.options.port, 3000);
  } finally {
    Deno.env.delete("TEST_PORT");
  }
});

Deno.test("Command.resolve moduleConfig overrides default", async () => {
  const cmd = new Command("TEST_CONFIG_PATH_MOD", TEST_ARGS_ENV, { port: 5000 }, []);
  await cmd.resolve();
  assertEquals(cmd.options.port, 5000);
});

Deno.test("Command.resolve CLI flag beats env var", async () => {
  Deno.env.set("TEST_PORT", "3000");
  try {
    const cmd = new Command("TEST_CONFIG_PATH_BEAT", TEST_ARGS_ENV, null, ["--port", "9000"]);
    await cmd.resolve();
    assertEquals(cmd.options.port, 9000);
  } finally {
    Deno.env.delete("TEST_PORT");
  }
});
