// Snapshot + semantics tests for cloud-init-common buildUserData.
//
// Fixtures are byte-stable outputs of the composer (regenerate only when
// intentionally changing the composed YAML):
//   test/fixtures/cloud-init/{iroh,iroh-secrets}.yaml
//
// Run:
//   deno test -A test/cloud_init_snapshot_test.ts

import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  acceptBundleModule,
  buildUserData,
  getUserDataModules,
  LegacyTransportError,
  listUserDataModules,
} from "@publicdomainrelay/cloud-init-common";

const SSH = "ssh-ed25519 AAAA test@example";
const CTX = {
  vmName: "vm-test",
  sshAuthorizedKey: SSH,
};

// Fixtures are the composer's byte-stable output: a change to composed YAML is
// expected to fail here first, and is accepted by rewriting the fixture it
// changed rather than by loosening the comparison.
const UPDATE = Deno.env.get("UPDATE_FIXTURES") === "1";

async function fixture(name: string): Promise<string> {
  return await Deno.readTextFile(new URL(`./fixtures/cloud-init/${name}`, import.meta.url));
}

async function snapshot(name: string, composed: string): Promise<void> {
  if (UPDATE) {
    await Deno.writeTextFile(new URL(`./fixtures/cloud-init/${name}`, import.meta.url), composed);
    return;
  }
  assertEquals(composed, await fixture(name));
}

const SECRETS_CTX = {
  ...CTX,
  secretsUrl: "https://sec-abc.relay.local",
  secretsRoute: "/xrpc/com.publicdomainrelay.temp.compute.secrets.getSecrets",
  secretsAud: "api://ATProto?actx=did:plc:requester123",
  secretsAcceptPath: "/root/secrets/publicdomainrelay.com/market/accept.json",
};

Deno.test("secrets module composes with the transport", async () => {
  await snapshot(
    "iroh-secrets.yaml",
    buildUserData({ ctx: SECRETS_CTX, modules: ["iroh", "secrets"] }),
  );
});

Deno.test("secrets module discovers provider paths at runtime, not build time", () => {
  const y = buildUserData({ ctx: SECRETS_CTX, modules: ["secrets"] });
  // Everything provider-specific comes out of the bidder-injected accept.json,
  // because this cloud-config is built before a bid has even been selected.
  // The bidder wraps bid_config as a strongRef ({uri, cid, value}), so the wif
  // record is under .value; the flat shape stays accepted as a fallback.
  assert(y.includes(".bid_config.value // .bid_config"), "unwraps the strongRef wrapper");
  assert(y.includes(".token_path // empty"), "token path read from accept.json");
  assert(y.includes(".url_path // empty"), "issuer base read from accept.json");
  assert(y.includes(".url_route //"), "exchange route read from accept.json");
  assert(!y.includes("/root/secrets/digitalocean.com"), "no provider path baked in");
});

Deno.test("secrets module echoes the provider-assigned subject", () => {
  const y = buildUserData({ ctx: SECRETS_CTX, modules: ["secrets"] });
  assert(y.includes("cut -d. -f2"), "subject read from the token payload");
  assert(y.includes("jq -r .sub"), "subject taken verbatim from sub");
  assert(!y.includes("actx:{actx}"), "no client-side subject template rendering");
});

Deno.test("secrets module fails loud", () => {
  const y = buildUserData({ ctx: SECRETS_CTX, modules: ["secrets"] });
  assert(y.includes("set -euo pipefail"), "strict shell");
  assert(!y.includes("skipping secrets"), "no best-effort skip");
  assert(y.includes("exit 1"), "non-zero exit on failure");
  assert(!/secrets fetch failed[^\n]*exit 0/.test(y), "failure must not exit 0");
});

Deno.test("secrets module writes secrets 0600 under a 0700 parent", () => {
  const y = buildUserData({ ctx: SECRETS_CTX, modules: ["secrets"] });
  assert(y.includes("install -d -m 0700 -o root -g root"), "parent dir locked down");
  assert(y.includes('chmod 0600 "${SECRET_PATH}"'), "secret file locked down");
  assert(y.includes("umask 077"), "umask before writing");
});

Deno.test("secrets module carries no secret values", () => {
  const y = buildUserData({ ctx: SECRETS_CTX, modules: ["secrets"] });
  assert(!y.includes("secret-value"), "values never enter cloud-init");
  assert(y.includes(SECRETS_CTX.secretsUrl), "only the fetch location is baked in");
});

Deno.test("merge semantics: base + module append, dedupe, override", () => {
  const base = `#cloud-config
packages:
  - curl
write_files:
  - path: /etc/base.conf
    content: base
runcmd:
  - echo base
`;

  const merged = buildUserData({
    ctx: CTX,
    base,
    modules: ["iroh"],
  });
  // base preserved: its packages ride the guard the composer puts at the head
  // of runcmd, and cloud-init's own package list is gone
  assert(merged.includes('"curl"'), "the base's packages are not carried into the composed document");
  assert(merged.includes("/etc/base.conf"));
  assert(merged.includes("echo base"));
  assert(merged.includes("iroh.service"));

  // module write_file path dedupes with base (later wins), base entry kept
  const withOverride = buildUserData({
    ctx: CTX,
    base: `write_files:\n  - path: /etc/systemd/system/iroh.service\n    content: base-unit\n`,
    modules: ["iroh"],
  });
  assertEquals((withOverride.match(/\/etc\/systemd\/system\/iroh\.service/g) ?? []).length, 1, "dedupe by path");
  assert(withOverride.includes("base-unit") === false, "module wins on dup path");

  // overrides beat modules
  const overridden = buildUserData({
    ctx: CTX,
    modules: ["iroh"],
    overrides: { ssh_pwauth: true },
  });
  assert(overridden.includes("ssh_pwauth: true"));
  assert(!overridden.includes("ssh_pwauth: false"));
});

Deno.test("runcmdPrepend ordering + acceptBundleModule", () => {
  const y = buildUserData({
    ctx: CTX,
    base: `runcmd:\n  - echo base\n`,
    modules: [acceptBundleModule("/root/accept.json", { ok: true })],
  });
  const run = y.split("runcmd:")[1];
  const installIdx = run.indexOf("install -d");
  const echoIdx = run.indexOf("echo base");
  assert(installIdx >= 0 && echoIdx >= 0 && installIdx < echoIdx, "prepend before base");
  assert(y.includes("/root/accept.json"));
});

Deno.test("unparseable / empty base -> fresh build", () => {
  const fresh = buildUserData({ ctx: CTX, modules: ["iroh"] });
  assertEquals(buildUserData({ ctx: CTX, base: "", modules: ["iroh"] }), fresh);
  assertEquals(buildUserData({ ctx: CTX, base: "not: [valid", modules: ["iroh"] }), fresh);
});

const IROH_CTX = {
  ...CTX,
  targetPort: 22,
  hostAliases: ["10.0.0.1 relay.internal"],
};

Deno.test("iroh module snapshot matches fixture", async () => {
  await snapshot("iroh.yaml", buildUserData({ ctx: IROH_CTX, modules: ["iroh"] }));
});

Deno.test("iroh module semantics", () => {
  const y = buildUserData({ ctx: IROH_CTX, modules: ["iroh"] });
  assert(y.includes('DUMBPIPE_VERSION="0.39.0"'), "pins the dumbpipe release");
  assert(y.includes("dumbpipe-v${DUMBPIPE_VERSION}-linux-${_arch}.tar.gz"), "release URL from GitHub");
  assert(y.includes("--host 127.0.0.1:22"), "listener forwards to the guest's own sshd");
  assert(y.includes("openssh-server"), "installs the sshd it configures");
  assert(!y.includes("ListenAddress"), "no ListenAddress line");
  // The guest announces itself the way every other transport does: it leaves its
  // address in the rendezvous file the on-network reporter publishes to the
  // market, and says which transport the address belongs to by its scheme.
  assert(
    y.includes(`/run/guest-fqdn`),
    "leaves its address in the file the on-network reporter already publishes",
  );
  assert(
    y.includes(`printf 'iroh://%s`),
    "announces the ticket as an iroh address, so the scheme says which transport it is",
  );
  assert(!y.includes("iroh-report.service"), "no reporter unit of its own");
  assert(!y.includes("iroh-report.path"), "no path unit of its own");
});

Deno.test("iroh listener never waits on sshd and no runcmd step blocks on a unit start", () => {
  const y = buildUserData({ ctx: IROH_CTX, modules: ["iroh"] });
  const unit = y.slice(y.indexOf("Description=iroh dumbpipe listener")).split("\n").map((l) => l.trim());
  assertEquals(unit.find((l) => l.startsWith("After=")), "After=network-online.target");
  assert(!/systemctl enable --now (?!--no-block)/.test(y), "every unit start in runcmd is --no-block");
  assert(
    y.indexOf("systemctl start --no-block iroh.service") < y.indexOf("--no-block ssh"),
    "the listener is queued before sshd",
  );
  assert(y.includes("mv -f /run/guest-fqdn.tmp /run/guest-fqdn"), "the rendezvous file appears whole");
});

Deno.test("the registry holds only iroh, secrets and k3s", () => {
  assertEquals([...listUserDataModules()].sort(), ["iroh", "k3s", "secrets"]);
  assertEquals(getUserDataModules(["iroh"]).length, 1);
  assertThrows(() => getUserDataModules(["nope"]));
  for (const legacy of ["tunnel", "fedproxy-ssh", "fedproxy-web", "wootty"]) {
    assertThrows(() => getUserDataModules([legacy]), LegacyTransportError, "iroh is the only guest transport");
  }
});
