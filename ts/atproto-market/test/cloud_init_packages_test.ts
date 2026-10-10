import { assert, assertEquals } from "@std/assert";
import { buildUserData, packagesGuardCommand } from "@publicdomainrelay/cloud-init-common";

const CTX = {
  vmName: "vm-test",
  sshAuthorizedKey: "ssh-ed25519 AAAA test@example",
  ingressProxyHost: "relay.local:443",
  audHost: "relay.local",
  secretsUrl: "https://secrets.test",
  secretsRoute: "/xrpc/secrets",
  secretsAud: "api://x",
};

function composed(modules: string[]): string {
  return buildUserData({ ctx: CTX, modules });
}

function runcmdEntries(text: string): string[] {
  const lines = text.split("\n");
  const start = lines.findIndex((l) => l === "runcmd:");
  return lines.slice(start + 1).filter((l) => l.startsWith("  - ")).map((l) => l.slice(4));
}

function firstIndexOf(entries: string[], needle: string): number {
  return entries.findIndex((e) => e.includes(needle));
}

Deno.test("the composed document asks cloud-init for no apt run of its own", () => {
  const y = composed(["iroh", "k3s", "secrets"]);
  assertEquals(
    /^packages:/m.test(y),
    false,
    "the document still carries cloud-init's own packages list, and the module that reads it runs " +
      "apt-get update on every boot whatever the image already holds",
  );
  assert(
    /^package_update: false$/m.test(y),
    "cloud-init's package module updates the index before it looks at what is missing",
  );
});

Deno.test("the packages the modules asked for become one guarded step at the head of runcmd", () => {
  const entries = runcmdEntries(composed(["iroh", "k3s", "secrets"]));
  const guard = entries[0];
  assert(guard?.startsWith("sh -c "), `the first runcmd entry is not the guard: ${guard}`);
  for (const name of ["openssh-server", "jq", "curl"]) {
    assert(guard.includes(`"${name}"`), `the guard does not name ${name}: ${guard}`);
  }
  assert(
    guard.includes("dpkg -s"),
    "the guard does not ask dpkg whether a package is already installed, so it apts on every boot",
  );
  assertEquals(
    entries.filter((e) => e.includes("apt-get install")).length,
    1,
    "more than one runcmd entry installs packages, so which of them ran is not readable from the document",
  );
});

Deno.test("a guest nothing was baked for still installs what it is missing", () => {
  const guard = packagesGuardCommand(["jq", "curl"]);
  assert(
    guard.includes("apt-get update") && guard.includes("apt-get install -y --no-install-recommends"),
    `the guard cannot install anything on a guest whose image carries nothing: ${guard}`,
  );
  assert(
    guard.includes(`missing="$missing $p"`) && guard.trimEnd().endsWith("$missing; }'"),
    `the guard installs the whole list rather than the ones dpkg reported missing: ${guard}`,
  );
  assertEquals(
    guard.split("'").length % 2,
    1,
    `the guard carries an unbalanced quote, so the shell it lands in sees a different command: ${guard}`,
  );
});

Deno.test("a package name that is not one is refused rather than put in a shell command", () => {
  let message = "";
  try {
    packagesGuardCommand(["jq; touch /root/pwned"]);
  } catch (err) {
    message = String(err);
  }
  assert(
    message.includes("jq; touch /root/pwned"),
    `a name that is not a package name was not refused: ${JSON.stringify(message)}`,
  );
});

Deno.test("the guard runs before the steps that need the packages it installs", () => {
  const entries = runcmdEntries(composed(["iroh", "k3s", "secrets"]));
  const guard = firstIndexOf(entries, "dpkg -s");
  assert(guard === 0, `the guard is not the first runcmd entry: ${entries.join(" | ")}`);
  for (const needle of ["setup-secrets.service", "setup-iroh.sh", "setup-k3s.sh"]) {
    const at = firstIndexOf(entries, needle);
    assert(at > guard, `the guard runs after ${needle}, which needs the packages it installs`);
  }
});

Deno.test("the secrets unit is started at the head of runcmd rather than after every other step", () => {
  const entries = runcmdEntries(composed(["iroh", "k3s", "secrets"]));
  const start = firstIndexOf(entries, "start --no-block setup-secrets.service");
  assert(start >= 0, `nothing starts setup-secrets: ${entries.join(" | ")}`);
  for (const needle of ["setup-iroh.sh", "setup-k3s.sh"]) {
    assert(
      start < firstIndexOf(entries, needle),
      `setup-secrets waits behind ${needle}, and the network it needs is up long before that step ` +
        `finishes: ${entries.join(" | ")}`,
    );
  }
});
