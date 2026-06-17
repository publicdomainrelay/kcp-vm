import { assertEquals, assertStringIncludes } from "@std/assert";
import { HTTPError } from "@publicdomainrelay/http-error";
import { EventBus } from "@publicdomainrelay/event-bus";
import { toCamelCase, coerce, cliffyFlag } from "@publicdomainrelay/cli-args-env";
import { rawStructuredLogger, noopLogger, noopLoggerInterface } from "@publicdomainrelay/logger";
import type { Logger } from "@publicdomainrelay/logger";

// --- HTTPError ---

Deno.test("HTTPError constructor sets status and detail", () => {
  const err = new HTTPError(404, "not found");
  assertEquals(err.status, 404);
  assertEquals(err.detail, "not found");
  assertEquals(err.message, "not found");
});

Deno.test("HTTPError is instanceof Error", () => {
  const err = new HTTPError(500, "boom");
  assertEquals(err instanceof Error, true);
  assertEquals(err instanceof HTTPError, true);
});

Deno.test("HTTPError toJSON returns correct shape", () => {
  const err = new HTTPError(403, "forbidden");
  assertEquals(err.toJSON(), { error: "http_error", code: 403, detail: "forbidden" });
});

// --- EventBus ---

Deno.test("EventBus subscribe and publish", () => {
  const bus = new EventBus<string>();
  const received: string[] = [];
  bus.subscribe((msg) => received.push(msg));
  bus.publish("hello");
  assertEquals(received, ["hello"]);
});

Deno.test("EventBus multiple subscribers all receive", () => {
  const bus = new EventBus<number>();
  const a: number[] = [];
  const b: number[] = [];
  bus.subscribe((n) => a.push(n));
  bus.subscribe((n) => b.push(n));
  bus.publish(42);
  assertEquals(a, [42]);
  assertEquals(b, [42]);
});

Deno.test("EventBus unsubscribe removes listener", () => {
  const bus = new EventBus<string>();
  const received: string[] = [];
  const unsub = bus.subscribe((msg) => received.push(msg));
  unsub();
  bus.publish("hello");
  assertEquals(received, []);
});

Deno.test("EventBus publish with no subscribers does not throw", () => {
  const bus = new EventBus<string>();
  bus.publish("nobody");
});

// --- toCamelCase ---

Deno.test("toCamelCase converts kebab to camelCase", () => {
  assertEquals(toCamelCase("my-flag"), "myFlag");
  assertEquals(toCamelCase("serve-path"), "servePath");
  assertEquals(toCamelCase("multi-word-flag-name"), "multiWordFlagName");
});

Deno.test("toCamelCase passes through when no hyphens", () => {
  assertEquals(toCamelCase("simple"), "simple");
  assertEquals(toCamelCase(""), "");
});

// --- coerce ---

Deno.test("coerce boolean true values", () => {
  assertEquals(coerce("true", "boolean"), true);
  assertEquals(coerce("1", "boolean"), true);
});

Deno.test("coerce boolean false values", () => {
  assertEquals(coerce("false", "boolean"), false);
  assertEquals(coerce("0", "boolean"), false);
  assertEquals(coerce("", "boolean"), false);
});

Deno.test("coerce number", () => {
  assertEquals(coerce("8080", "number"), 8080);
  assertEquals(coerce("0", "number"), 0);
  assertEquals(coerce("-1", "number"), -1);
});

Deno.test("coerce string passthrough", () => {
  assertEquals(coerce("hello", "string"), "hello");
});

Deno.test("coerce unknown type passthrough", () => {
  assertEquals(coerce("val", "unknown"), "val");
});

// --- cliffyFlag ---

Deno.test("cliffyFlag boolean", () => {
  assertEquals(cliffyFlag("verbose", "boolean"), "--verbose");
});

Deno.test("cliffyFlag number", () => {
  assertEquals(cliffyFlag("port", "number"), "--port <port:number>");
});

Deno.test("cliffyFlag string", () => {
  assertEquals(cliffyFlag("host", "string"), "--host <host:string>");
});

// --- Logger ---

Deno.test("rawStructuredLogger: debug suppressed at minLevel info", () => {
  const lines: string[] = [];
  const log: Logger = rawStructuredLogger("test", "info");
  const origLog = console.log;
  const origError = console.error;
  console.log = (s: string) => lines.push(s);
  console.error = () => {};
  try {
    log("debug", "should not appear");
    log("info", "should appear");
  } finally {
    console.log = origLog;
    console.error = origError;
  }
  assertEquals(lines.length, 1);
  assertStringIncludes(lines[0], "should appear");
});

Deno.test("rawStructuredLogger: warn and error go to stderr", () => {
  const errors: string[] = [];
  const origError = console.error;
  console.error = (s: string) => errors.push(s);
  const origLog = console.log;
  console.log = () => {};
  try {
    const log: Logger = rawStructuredLogger("test", "debug");
    log("warn", "warning message");
    log("error", "error message");
  } finally {
    console.error = origError;
    console.log = origLog;
  }
  assertEquals(errors.length, 2);
  assertStringIncludes(errors[0], "warning message");
  assertStringIncludes(errors[1], "error message");
});

Deno.test("rawStructuredLogger JSON output has required fields", () => {
  const lines: string[] = [];
  const origLog = console.log;
  console.log = (s: string) => lines.push(s);
  try {
    const log: Logger = rawStructuredLogger("myapp", "debug");
    log("info", "test message", { extra: "data" });
  } finally {
    console.log = origLog;
  }
  const parsed = JSON.parse(lines[0]);
  assertEquals(typeof parsed.ts, "string");
  assertEquals(parsed.level, "info");
  assertEquals(parsed.message, "test message");
  assertEquals(parsed.prefix, "myapp");
  assertEquals(parsed.extra, "data");
});

Deno.test("noopLogger does not throw", () => {
  noopLogger("error", "boom");
  noopLogger("info", "msg", { key: "val" });
});

Deno.test("noopLoggerInterface does not throw", () => {
  noopLoggerInterface.info("msg");
  noopLoggerInterface.warn("msg");
  noopLoggerInterface.error("msg");
  noopLoggerInterface.debug("msg");
});
