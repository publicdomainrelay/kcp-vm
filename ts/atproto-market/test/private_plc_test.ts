import { assertEquals } from "@std/assert";
import { isPrivatePlc } from "@publicdomainrelay/atproto-helpers";

Deno.test("a PLC directory other than plc.directory is private, so plc.directory lookups are sent to it", () => {
  assertEquals(isPrivatePlc("https://plc.directory"), false);
  assertEquals(isPrivatePlc("https://plc.directory/"), false);
  assertEquals(isPrivatePlc("https://127.0.0.1:2587"), true);
  assertEquals(isPrivatePlc("http://localhost:2582"), true);
  assertEquals(isPrivatePlc("https://plc.default.global.svc.kcp.local"), true);
  assertEquals(isPrivatePlc("not a url"), false);
});
