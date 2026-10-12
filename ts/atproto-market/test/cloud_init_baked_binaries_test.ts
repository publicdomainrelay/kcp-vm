import { assert, assertEquals } from "@std/assert";
import { buildUserData, DUMBPIPE_VERSION_DEFAULT } from "@publicdomainrelay/cloud-init-common";

const CTX = {
  vmName: "vm-test",
  sshAuthorizedKey: "ssh-ed25519 AAAA test@example",
  ingressProxyHost: "relay.local:443",
  audHost: "relay.local",
};

const K3S_SETUP_PATH = "/usr/local/bin/setup-k3s.sh";
const IROH_SETUP_PATH = "/usr/local/bin/setup-iroh.sh";

function scriptAt(text: string, path: string): string {
  const at = text.indexOf(`path: ${path}`);
  assert(at > 0, `${path} is not in the document`);
  const lines = text.slice(at).split("\n");
  const indent = lines[1].match(/^\s*/)?.[0] ?? "    ";
  const body: string[] = [];
  for (const line of lines.slice(2)) {
    if (line.trim() !== "" && !line.startsWith(indent)) break;
    body.push(line.slice(indent.length));
  }
  return body.join("\n");
}

Deno.test("a guest whose image carries the pinned k3s never reaches get.k3s.io", () => {
  const script = scriptAt(buildUserData({ ctx: CTX, modules: ["k3s"] }), K3S_SETUP_PATH);
  const skip = script.indexOf("/usr/local/bin/k3s --version");
  const install = script.indexOf("get.k3s.io");
  assert(skip > 0, `the script does not look for the release the node image stages: ${script}`);
  assert(
    script.includes("[ -x /usr/local/bin/k3s ]") && script.includes("grep -qF 'v1.36.4+k3s1'"),
    `the script does not hold the staged binary to the pinned release: ${script}`,
  );
  assert(
    install > skip,
    "the script downloads before it looks at what the image carries, so a market node pays for the " +
      `installation it was built with: ${script}`,
  );
  assert(
    script.slice(skip, install).includes("exit 0"),
    `the staged release does not end the step: ${script}`,
  );
});

Deno.test("a guest whose image carries the pinned dumbpipe never reaches GitHub", () => {
  const script = scriptAt(buildUserData({ ctx: CTX, modules: ["iroh"] }), IROH_SETUP_PATH);
  const skip = script.indexOf("[ -x /usr/local/bin/dumbpipe ]");
  const download = script.indexOf("github.com/n0-computer/dumbpipe");
  assert(skip > 0, `the script does not look for the binary the node image stages: ${script}`);
  assert(
    script.includes("/usr/local/bin/dumbpipe.version"),
    "the script accepts any dumbpipe the image happens to carry, and dumbpipe has no version flag, " +
      `so the version it was built with is in that file and nowhere else: ${script}`,
  );
  assert(
    download > skip,
    "the script downloads before it looks at what the image carries, so a market node pays for the " +
      `download it was built with: ${script}`,
  );
  assert(
    script.slice(skip, download).includes("exit 0"),
    `the staged binary does not end the step: ${script}`,
  );
});

Deno.test("the version the skip check compares against is the version the step installs", () => {
  const y = buildUserData({ ctx: CTX, modules: ["iroh"] });
  assert(
    y.includes(`DUMBPIPE_VERSION="${DUMBPIPE_VERSION_DEFAULT}"`),
    `the step does not pin the release: ${DUMBPIPE_VERSION_DEFAULT}`,
  );
  assertEquals(
    y.includes(`"\${DUMBPIPE_VERSION}"`),
    true,
    "the skip check does not compare the staged binary against the release this step installs, so " +
      "an image built with another release is read as the pinned one",
  );
  const other = buildUserData({ ctx: { ...CTX, dumbpipeVersion: "9.9.9" }, modules: ["iroh"] });
  assert(
    other.includes(`DUMBPIPE_VERSION="9.9.9"`),
    "a caller that pinned another release gets a guest that installs the default",
  );
  assertEquals(
    other.includes(`DUMBPIPE_VERSION="${DUMBPIPE_VERSION_DEFAULT}"`),
    false,
    "the caller's release is ignored",
  );
});
