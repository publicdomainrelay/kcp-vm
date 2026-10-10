import { assert, assertEquals, assertStringIncludes } from "@std/assert";
import { parse as parseYaml } from "@std/yaml";
import { buildUserData } from "@publicdomainrelay/cloud-init-common";
import {
  createK3sJoinModule,
  DEFAULT_DNS_GATEWAY,
  NODE_SCRIPTS,
  NODE_UNIT_ORDER,
  NODE_UNITS,
  renderApiserverLinkScript,
  renderNodeSecretsScript,
  renderOpenBaoLinkScript,
  renderResolver,
  RESOLV_CONF_PATH,
  SECRETS_FETCH_UNIT,
} from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import { OPENBAO_LINK_LOCAL_ADDR } from "@publicdomainrelay/socialweb-computer-k8s-common";

const CA = "-----BEGIN CERTIFICATE-----\nSERVERCA\n-----END CERTIFICATE-----\n";
const OPENBAO_CA = "-----BEGIN CERTIFICATE-----\nOPENBAOCA\n-----END CERTIFICATE-----\n";

interface CloudConfig {
  write_files: { path: string; permissions?: string; content: string }[];
  runcmd: unknown[];
}

function nodeUserData(): CloudConfig {
  const text = buildUserData({
    ctx: {
      vmName: "ci-node1",
      secretsUrl: "https://secrets.test",
      secretsRoute: "/xrpc/secrets",
      secretsAud: "api://ATProto?actx=did:plc:owner",
    },
    modules: ["iroh", "k3s", createK3sJoinModule(), "secrets"],
  });
  return parseYaml(text.replace(/^#cloud-config\n/, "")) as CloudConfig;
}

function unitSection(unit: string, key: string): string[] {
  return unit.split("\n").filter((l) => l.startsWith(`${key}=`)).map((l) => l.slice(key.length + 1));
}

function runcmdIndex(config: CloudConfig, needle: string): number {
  return config.runcmd.findIndex((c) => typeof c === "string" && c.includes(needle));
}

Deno.test("the node's user_data installs four units that join it through OpenBao and the apiserver link, in order", () => {
  const config = nodeUserData();
  const files = Object.fromEntries(config.write_files.map((f) => [f.path, f.content]));
  const units = NODE_UNIT_ORDER.map((name) => files[`/etc/systemd/system/${name}`]);
  for (const [i, unit] of units.entries()) assert(unit, `no unit file for ${NODE_UNIT_ORDER[i]}`);
  const [openbao, secrets, apiserver, agent] = units;

  assertEquals(unitSection(openbao, "After")[0].split(" ").includes(SECRETS_FETCH_UNIT), true);
  assertEquals(unitSection(openbao, "ExecStart"), [NODE_SCRIPTS.openbaoLink]);
  const secretsAfter = unitSection(secrets, "After")[0].split(" ");
  assert(secretsAfter.includes(SECRETS_FETCH_UNIT) && secretsAfter.includes(NODE_UNITS.openbaoLink), secretsAfter.join(" "));
  assertEquals(unitSection(secrets, "Type"), ["oneshot"]);
  assert(unitSection(apiserver, "After")[0].split(" ").includes(NODE_UNITS.nodeSecrets));
  const agentAfter = unitSection(agent, "After")[0].split(" ");
  assert(agentAfter.includes(NODE_UNITS.apiserverLink) && agentAfter.includes(NODE_UNITS.nodeSecrets), agentAfter.join(" "));

  for (const unit of [openbao, apiserver, agent]) assertEquals(unitSection(unit, "Restart"), ["always"]);
  assertEquals(unitSection(secrets, "Restart"), ["on-failure"]);
  for (const unit of units) assertEquals(unitSection(unit, "StartLimitIntervalSec"), ["0"]);

  const agentExec = unitSection(agent, "ExecStart")[0];
  assertStringIncludes(agentExec, "/usr/local/bin/k3s agent --server https://${ADVERTISE_ADDRESS}:${APISERVER_PORT}");
  assertStringIncludes(agentExec, "--token-file /etc/socialweb/node/token");
  assertStringIncludes(agentExec, "--node-name ${NODE_NAME}");
  assertStringIncludes(agentExec, "--data-dir /var/lib/rancher/k3s");

  const start = runcmdIndex(config, `systemctl start --no-block ${NODE_UNIT_ORDER.join(" ")}`);
  assert(start >= 0, `no runcmd starts the node units: ${JSON.stringify(config.runcmd)}`);
  assert(runcmdIndex(config, "/usr/local/bin/setup-iroh.sh") >= 0, "the iroh transport module installs dumbpipe");
  assert(runcmdIndex(config, "/usr/local/bin/setup-k3s.sh") >= 0, "the k3s module installs k3s");
  assert(runcmdIndex(config, "/usr/local/bin/setup-iroh.sh") < start, "dumbpipe is installed before the units start");
  assert(runcmdIndex(config, "/usr/local/bin/setup-k3s.sh") < start, "k3s is installed before the units start");
});

Deno.test("the node's user_data carries no secret value and no ssh join path", () => {
  const config = nodeUserData();
  const text = JSON.stringify(config);
  for (const forbidden of ["BEGIN CERTIFICATE", "GatewayPorts", "socialweb-k3s-join", "client_token\":\"", "nodePassword\":\""]) {
    assertEquals(text.includes(forbidden), false, `user_data carries ${forbidden}`);
  }
  assertEquals(/K10[0-9a-f]{64}::/.test(text), false, "user_data carries a pinned join token");
  assertEquals(/ticket[^\n]*[a-z0-9]{40,}/.test(text), false, "user_data carries a ticket");
  assertEquals(config.write_files.some((f) => f.path.startsWith("/etc/ssh/sshd_config.d/20-")), false);
  for (const script of Object.values(NODE_SCRIPTS)) {
    assertEquals(config.write_files.find((f) => f.path === script)?.permissions, "0700");
  }
});

Deno.test("the module gives the guest the resolver the provider's network forwards through", () => {
  const patch = createK3sJoinModule()({});
  const files = Object.fromEntries((patch.write_files ?? []).map((f) => [f.path, f.content]));
  assertEquals(files[RESOLV_CONF_PATH], renderResolver([DEFAULT_DNS_GATEWAY]));
  assertEquals(Boolean(patch.bootcmd?.length), true, "the resolver is written before cloud-init's own stages");
  assertEquals(renderResolver(["10.0.0.1"]), "nameserver 10.0.0.1\noptions timeout:2 attempts:3\n");
});

function b64url(text: string): string {
  return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

const FAKE_CURL = `#!/bin/bash
URL=""; DATA=""; CACERT=""; HEADERS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -d) DATA="$2"; shift 2 ;;
    -H) HEADERS+=("$2"); shift 2 ;;
    --cacert) CACERT="$2"; shift 2 ;;
    -*) shift ;;
    *) URL="$1"; shift ;;
  esac
done
printf '%s\\t%s\\t%s\\t%s\\n' "$URL" "$CACERT" "$DATA" "\${HEADERS[*]}" >> "$CURL_LOG"
case "$URL" in
  https://issuer.test/v1/oidc/issue)
    [ "$(printf '%s' "$DATA" | jq -r .aud)" = "$WANT_AUD" ] || exit 22
    [ "$(printf '%s' "$DATA" | jq -r .sub)" = "$WANT_SUB" ] || exit 22
    echo '{"token":"exchanged.jwt.sig"}' ;;
  https://127.0.0.1:18200/v1/auth/jwt-0123456789ab/login)
    [ "$CACERT" = "$WANT_CA" ] || exit 60
    [ "$(printf '%s' "$DATA" | jq -r .role)" = "ci-node1" ] || exit 22
    [ "$(printf '%s' "$DATA" | jq -r .jwt)" = "exchanged.jwt.sig" ] || exit 22
    echo '{"auth":{"client_token":"s.bao"}}' ;;
  https://127.0.0.1:18200/v1/socialweb-nodes/data/ci-node1)
    [ "$CACERT" = "$WANT_CA" ] || exit 60
    case " \${HEADERS[*]} " in *"X-Vault-Token: s.bao"*) ;; *) exit 22 ;; esac
    cat "$KV_RESPONSE" ;;
  *) exit 22 ;;
esac
`;

async function nodeSecretsRun(kv: Record<string, unknown>) {
  const dir = await Deno.makeTempDir();
  const node = `${dir}/etc/socialweb/node`;
  await Deno.mkdir(`${dir}/bin`, { recursive: true });
  await Deno.mkdir(node, { recursive: true });
  await Deno.writeTextFile(`${dir}/bin/curl`, FAKE_CURL, { mode: 0o755 });
  const sub = "actx:a1:plc:owner:role:ci-node1";
  await Deno.writeTextFile(`${dir}/wid`, `${b64url('{"alg":"ES256"}')}.${b64url(JSON.stringify({ sub, iss: "https://issuer.test" }))}.sig`);
  await Deno.writeTextFile(`${dir}/issuer`, "https://issuer.test");
  await Deno.writeTextFile(
    `${dir}/accept.json`,
    JSON.stringify({ bid_config: { value: { token_path: `${dir}/wid`, url_path: `${dir}/issuer` } } }),
  );
  const audience = "socialweb.computer/node/abcd1234-ci/ci-node1";
  await Deno.writeTextFile(
    `${node}/claim.json`,
    JSON.stringify({
      node: "ci-node1",
      cluster: "abcd1234-ci",
      jwtMount: "jwt-0123456789ab",
      role: "ci-node1",
      audience,
      kvMount: "socialweb-nodes",
      kvPath: "ci-node1",
    }),
  );
  await Deno.writeTextFile(`${node}/openbao-ca.pem`, OPENBAO_CA);
  await Deno.writeTextFile(`${dir}/kv.json`, JSON.stringify({ data: { data: kv } }));
  const script = renderNodeSecretsScript({
    nodeDir: node,
    acceptPath: `${dir}/accept.json`,
    k3sNodePasswordPath: `${dir}/rancher/node/password`,
    doneStamp: `${dir}/done`,
  });
  await Deno.writeTextFile(`${dir}/node-secrets`, script, { mode: 0o700 });
  const out = await new Deno.Command("bash", {
    args: [`${dir}/node-secrets`],
    env: {
      PATH: `${dir}/bin:/usr/bin:/bin`,
      CURL_LOG: `${dir}/curl.log`,
      WANT_AUD: audience,
      WANT_SUB: sub,
      WANT_CA: `${node}/openbao-ca.pem`,
      KV_RESPONSE: `${dir}/kv.json`,
    },
    stdout: "piped",
    stderr: "piped",
  }).output();
  return { dir, node, code: out.code, stderr: new TextDecoder().decode(out.stderr) };
}

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

const KV = {
  token: "k10secret",
  nodePassword: "k10password",
  serverCA: CA,
  apiserverTicket: "endpointabc123",
  advertiseAddress: "100.127.255.254",
  apiserverPort: 23456,
};

Deno.test("node-secrets exchanges the workload token for the claim's audience, logs in to OpenBao and writes the join files 0600", async () => {
  const run = await nodeSecretsRun(KV);
  try {
    assertEquals(run.code, 0, run.stderr);
    const read = (p: string) => Deno.readTextFile(p);
    assertEquals(await read(`${run.node}/token`), `K10${await sha256Hex(CA)}::server:k10secret`);
    assertEquals(await read(`${run.node}/server-ca.pem`), CA);
    assertEquals(await read(`${run.node}/node-password`), "k10password");
    assertEquals(await read(`${run.dir}/rancher/node/password`), "k10password");
    assertEquals(
      await read(`${run.node}/apiserver.env`),
      "NODE_NAME=ci-node1\nADVERTISE_ADDRESS=100.127.255.254\nAPISERVER_PORT=23456\nAPISERVER_TICKET=endpointabc123\n",
    );
    assertEquals(
      await read(`${run.node}/meshd.env`),
      "SOCIALWEB_MESHD_NODE_NAME=ci-node1\nSOCIALWEB_MESHD_KUBECONFIG=/var/lib/rancher/k3s/agent/kubelet.kubeconfig\n",
    );
    for (const f of ["token", "server-ca.pem", "node-password", "apiserver.env", "meshd.env"]) {
      assertEquals((await Deno.stat(`${run.node}/${f}`)).mode! & 0o777, 0o600, f);
    }
    assertEquals((await Deno.stat(`${run.dir}/rancher/node/password`)).mode! & 0o777, 0o600);
    const calls = (await read(`${run.dir}/curl.log`)).trim().split("\n").map((l) => l.split("\t")[0]);
    assertEquals(calls, [
      "https://issuer.test/v1/oidc/issue",
      "https://127.0.0.1:18200/v1/auth/jwt-0123456789ab/login",
      "https://127.0.0.1:18200/v1/socialweb-nodes/data/ci-node1",
    ]);
    await Deno.stat(`${run.dir}/done`);
  } finally {
    await Deno.remove(run.dir, { recursive: true });
  }
});

Deno.test("node-secrets refuses a KV entry that would put an unsafe value on a unit's command line", async () => {
  const run = await nodeSecretsRun({ ...KV, apiserverTicket: "abc; rm -rf /" });
  try {
    assertEquals(run.code, 1);
    assertStringIncludes(run.stderr, "apiserverTicket is not a ticket");
    await Deno.stat(`${run.node}/apiserver.env`).then(() => {
      throw new Error("the env file was written");
    }, () => undefined);
  } finally {
    await Deno.remove(run.dir, { recursive: true });
  }
});

Deno.test("node-secrets fails, and is restarted by systemd, when the KV entry lacks a field", async () => {
  const { serverCA: _ca, ...partial } = KV;
  const run = await nodeSecretsRun(partial);
  try {
    assertEquals(run.code, 1);
    assertStringIncludes(run.stderr, "has no serverCA");
  } finally {
    await Deno.remove(run.dir, { recursive: true });
  }
});

async function shimmedLink(script: string, env: Record<string, string>) {
  const dir = await Deno.makeTempDir();
  await Deno.mkdir(`${dir}/bin`, { recursive: true });
  await Deno.writeTextFile(`${dir}/bin/ip`, `#!/bin/bash\necho "$*" >> ${dir}/ip.log\n`, { mode: 0o755 });
  await Deno.writeTextFile(`${dir}/dumbpipe`, `#!/bin/bash\necho "$*" > ${dir}/dumbpipe.args\n`, { mode: 0o755 });
  await Deno.writeTextFile(`${dir}/ticket`, "openbaoticket\n");
  const body = script.replaceAll("/usr/local/bin/dumbpipe", `${dir}/dumbpipe`).replaceAll(
    "/etc/socialweb/node/openbao-ticket",
    `${dir}/ticket`,
  );
  await Deno.writeTextFile(`${dir}/link`, body, { mode: 0o700 });
  const out = await new Deno.Command("bash", {
    args: [`${dir}/link`],
    env: { PATH: `${dir}/bin:/usr/bin:/bin`, ...env },
    stdout: "piped",
    stderr: "piped",
  }).output();
  return { dir, code: out.code, stderr: new TextDecoder().decode(out.stderr) };
}

Deno.test("the OpenBao link dials the delivered ticket and listens where node-secrets looks", async () => {
  const run = await shimmedLink(renderOpenBaoLinkScript(), {});
  try {
    assertEquals(run.code, 0, run.stderr);
    assertEquals(
      (await Deno.readTextFile(`${run.dir}/dumbpipe.args`)).trim(),
      `connect-tcp --addr ${OPENBAO_LINK_LOCAL_ADDR} openbaoticket`,
    );
  } finally {
    await Deno.remove(run.dir, { recursive: true });
  }
});

Deno.test("the apiserver link puts the advertise address on lo and dials the apiserver ticket there", async () => {
  const run = await shimmedLink(renderApiserverLinkScript(), {
    ADVERTISE_ADDRESS: "100.127.255.254",
    APISERVER_PORT: "23456",
    APISERVER_TICKET: "apiticket",
  });
  try {
    assertEquals(run.code, 0, run.stderr);
    assertStringIncludes(await Deno.readTextFile(`${run.dir}/ip.log`), "addr add 100.127.255.254/32 dev lo");
    assertEquals(
      (await Deno.readTextFile(`${run.dir}/dumbpipe.args`)).trim(),
      "connect-tcp --addr 100.127.255.254:23456 apiticket",
    );
  } finally {
    await Deno.remove(run.dir, { recursive: true });
  }
});

Deno.test("every join and network oneshot unit writes to the guest console, so a failure reads without a shell on the guest", () => {
  const config = nodeUserData();
  const files = Object.fromEntries(config.write_files.map((f) => [f.path, f.content]));
  const quiet = [NODE_UNITS.k3sAgent];
  const units = Object.keys(files)
    .filter((path) => /^\/etc\/systemd\/system\/socialweb-[^/]+\.service$/.test(path))
    .filter((path) => !quiet.some((name) => path.endsWith(`/${name}`)));
  assert(units.length >= 6, units.join(" "));
  for (const path of units) {
    assertEquals(unitSection(files[path], "StandardOutput"), ["journal+console"], path);
    assertEquals(unitSection(files[path], "StandardError"), ["journal+console"], path);
  }
  const agent = files[`/etc/systemd/system/${NODE_UNITS.k3sAgent}`];
  assertEquals(unitSection(agent, "StandardOutput"), [], "the k3s agent's kubelet log would drown the console");
});
