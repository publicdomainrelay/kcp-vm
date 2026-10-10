import { assert, assertEquals } from "@std/assert";
import { buildUserData, DUMBPIPE_VERSION_DEFAULT } from "@publicdomainrelay/cloud-init-common";
import { createK3sJoinModule } from "@publicdomainrelay/cloud-init-plugin-k3s-join";

const MANIFEST = new URL("../../deploy/bidder/preinstall-market.json", import.meta.url);

interface MarketPreinstall {
  packages: string[];
  files: { path: string; mode?: string; content?: string; source?: string }[];
  runs: string[];
}

const CTX = {
  vmName: "market-node-1",
  sshAuthorizedKey: "ssh-ed25519 AAAA the door",
  ingressProxyHost: "door.localhost:2583",
  audHost: "door.localhost",
  secretsUrl: "https://secrets.localhost",
  secretsRoute: "/xrpc/secrets",
  secretsAud: "api://ATProto?actx=did:plc:door",
};

export function marketNodeUserData(): string {
  return buildUserData({
    ctx: CTX,
    modules: ["iroh", "k3s", createK3sJoinModule(), "secrets"],
  });
}

function marketPreinstall(): Promise<MarketPreinstall> {
  return Deno.readTextFile(MANIFEST).then((raw) => JSON.parse(raw) as MarketPreinstall);
}

function packagesInGuard(userData: string): string[] {
  const guard = userData.split("\n").find((l) => l.includes("for p in "));
  assert(guard, "the composed document carries no guarded package step");
  return [...guard.matchAll(/"([a-z0-9][a-z0-9+.-]*)"/g)].map((m) => m[1]);
}

Deno.test("the preinstall manifest covers every package the door's user_data installs", async () => {
  const manifest = await marketPreinstall();
  const wanted = packagesInGuard(marketNodeUserData());
  assert(wanted.length > 0, "the composed document asks for no packages, so this asserts nothing");
  const missing = wanted.filter((name) => !manifest.packages.includes(name));
  assertEquals(
    missing,
    [],
    `the guest's own user_data still installs ${JSON.stringify(missing)} at boot, and the image ` +
      `it boots from was built from ${JSON.stringify(manifest.packages)}: every boot of a market ` +
      `node reaches the network for a package the image could have carried`,
  );
});

Deno.test("the manifest carries the release the iroh module's skip check compares against", async () => {
  const manifest = await marketPreinstall();
  const run = manifest.runs.find((r) => r.includes("dumbpipe"));
  assert(run, `the manifest installs no dumbpipe: ${JSON.stringify(manifest.runs)}`);
  assert(
    run.includes(`/v${DUMBPIPE_VERSION_DEFAULT}/dumbpipe-v${DUMBPIPE_VERSION_DEFAULT}-linux-`),
    `the manifest installs a release the module does not pin (${DUMBPIPE_VERSION_DEFAULT}), so a ` +
      `market node would refuse the binary it was built with and download one instead: ${run}`,
  );
  assert(
    run.includes(`> /usr/local/bin/dumbpipe.version`) && run.includes(DUMBPIPE_VERSION_DEFAULT),
    `the guest's skip check reads /usr/local/bin/dumbpipe.version, and this run does not write the ` +
      `release it installed: ${run}`,
  );
  assert(
    /sha256sum -c -/.test(run) && /_sha=[0-9a-f]{64}/.test(run),
    `the manifest fetches a release archive without holding it to a digest: ${run}`,
  );
});

Deno.test("the manifest bakes the same dumbpipe the guest's skip check looks for", async () => {
  const manifest = await marketPreinstall();
  const userData = marketNodeUserData();
  assertEquals(
    userData.includes(`dumbpipe.version`) && userData.includes(`"\${DUMBPIPE_VERSION}"`),
    true,
    "the iroh step does not compare the staged binary against the release it would install",
  );
  assertEquals(
    manifest.files.some((f) => f.path === "/usr/local/bin/dumbpipe"),
    false,
    "the manifest writes dumbpipe as a file, and its bytes are not in the manifest: the run that " +
      "unpacks the pinned archive is what installs it",
  );
});

Deno.test("the manifest uses a path the guest's own cloud-init reads", async () => {
  const manifest = await marketPreinstall();
  for (const file of manifest.files) {
    assert(file.path.startsWith("/"), `${file.path} is not absolute inside the guest`);
    assert(file.source === undefined, `${file.path} is read from the build host, which this test cannot check`);
  }
  assert(
    manifest.runs.length > 0 && manifest.packages.length > 0,
    "the manifest is empty, and the builder refuses that",
  );
});
