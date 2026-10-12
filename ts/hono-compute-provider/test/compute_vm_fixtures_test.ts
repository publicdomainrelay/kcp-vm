import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  ApiValidationError,
  parseComputeVM,
  validateComputeVM,
} from "@publicdomainrelay/compute-provider-kcp-common";

const FIXTURES = new URL("./fixtures/computevm/", import.meta.url);

interface InvalidFixture {
  expectError: string;
  goDecodeRejects: boolean;
  object: unknown;
}

async function fixtures(prefix: string): Promise<{ name: string; value: unknown }[]> {
  const out: { name: string; value: unknown }[] = [];
  for await (const entry of Deno.readDir(FIXTURES)) {
    if (!entry.isFile || !entry.name.startsWith(prefix) || !entry.name.endsWith(".json")) continue;
    out.push({ name: entry.name, value: JSON.parse(await Deno.readTextFile(new URL(entry.name, FIXTURES))) });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

Deno.test("TestComputeVMRoundTripsThroughTheSharedFixtures", async (t) => {
  const valid = await fixtures("valid-");
  assert(valid.length >= 4, `expected the shared valid fixtures, found ${valid.length}`);
  for (const { name, value } of valid) {
    await t.step(name, () => {
      assertEquals(validateComputeVM(value), [], name);
      assertEquals(parseComputeVM(structuredClone(value)), value, name);
    });
  }
  const invalid = await fixtures("invalid-");
  assert(invalid.length >= 7, `expected the shared invalid fixtures, found ${invalid.length}`);
  for (const { name, value } of invalid) {
    await t.step(name, () => {
      const fixture = value as InvalidFixture;
      const issues = validateComputeVM(fixture.object);
      assert(
        issues.some((i) => i.path === fixture.expectError),
        `${name}: expected an issue at ${fixture.expectError}, got ${JSON.stringify(issues)}`,
      );
      const err = assertThrows(() => parseComputeVM(fixture.object), ApiValidationError);
      assert(err.issues.some((i) => i.path === fixture.expectError), name);
    });
  }
});
