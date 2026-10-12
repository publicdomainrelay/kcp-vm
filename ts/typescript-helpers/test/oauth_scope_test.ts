import { assertEquals } from "@std/assert";
import { dedupeScopes } from "@publicdomainrelay/oauth-scope";

Deno.test("dedupeScopes dedups and preserves order", () => {
  assertEquals(dedupeScopes(["a", "b"], ["b", "c"], []), ["a", "b", "c"]);
});
