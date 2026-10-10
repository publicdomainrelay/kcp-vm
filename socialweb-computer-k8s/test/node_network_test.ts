import { assert, assertEquals, assertStringIncludes, assertThrows } from "@std/assert";
import { parse as parseYaml } from "@std/yaml";
import { buildUserData } from "@publicdomainrelay/cloud-init-common";
import {
  createK3sJoinModule,
  type K3sJoinModuleOptions,
  MESHD_UNIT,
  MESHD_UNIT_DROP_IN,
  NODE_LOCAL_DNS_ADDRESS,
  NODE_LOCAL_DNS_LABEL,
  NODE_NET_SCRIPTS,
  NODE_NET_UNITS,
  NODE_UNITS,
  POD_CLUSTER_CIDR,
  POD_MTU,
  renderDnsLinkScript,
  renderMeshdInstallScript,
  renderNodeDiagnoseScript,
  renderPodCniScript,
  renderPodMasqueradeScript,
} from "@publicdomainrelay/cloud-init-plugin-k3s-join";

const PIN_X86 = "7c563cd6cd10e4113aab6a8dc3fc45e041728376499e6a215b1d51f0dfa2ad64";
const PIN_ARM = "47df130296100de6b08e9008421a6351fa5848264ebb71ec586245daf7f1212a";
const DOOR = "https://door.test";
const MESHD = { downloadBase: DOOR, sha256: { x86_64: PIN_X86, aarch64: PIN_ARM } };

interface CloudConfig {
  write_files: { path: string; permissions?: string; content: string }[];
  runcmd: unknown[];
  packages?: string[];
}

function userData(opts: K3sJoinModuleOptions = { meshd: MESHD }): CloudConfig {
  const text = buildUserData({
    ctx: { vmName: "ci-node1", secretsUrl: "https://secrets.test", secretsRoute: "/xrpc/secrets", secretsAud: "api://x" },
    modules: ["iroh", "k3s", createK3sJoinModule(opts), "secrets"],
  });
  return parseYaml(text.replace(/^#cloud-config\n/, "")) as CloudConfig;
}

function files(config: CloudConfig): Record<string, string> {
  return Object.fromEntries(config.write_files.map((f) => [f.path, f.content]));
}

function values(unit: string, key: string): string[] {
  return unit.split("\n").filter((l) => l.startsWith(`${key}=`)).flatMap((l) => l.slice(key.length + 1).split(/\s+/)).filter(Boolean);
}

const repo = (path: string) => new URL(`../../${path}`, import.meta.url);

Deno.test("the node's user_data adds the pod network units, and the k3s agent never waits on meshd", async () => {
  const config = userData();
  const f = files(config);
  for (const name of Object.values(NODE_NET_UNITS)) assert(f[`/etc/systemd/system/${name}`], `no unit ${name}`);
  for (const script of Object.values(NODE_NET_SCRIPTS)) {
    assertEquals(config.write_files.find((w) => w.path === script)?.permissions, "0700", script);
  }

  assertEquals(
    f[`/etc/systemd/system/${NODE_NET_UNITS.meshd}`],
    await Deno.readTextFile(repo("rust/socialweb-meshd/deploy/socialweb-meshd.service")),
    "the meshd unit is the one rust/socialweb-meshd/deploy ships",
  );
  assertEquals(MESHD_UNIT, f[`/etc/systemd/system/${NODE_NET_UNITS.meshd}`]);
  const dropIn = f[`/etc/systemd/system/${NODE_NET_UNITS.meshd}.d/10-socialweb-node.conf`];
  assertEquals(dropIn, MESHD_UNIT_DROP_IN);
  for (const dep of [NODE_NET_UNITS.meshdInstall, NODE_UNITS.nodeSecrets]) {
    assert(values(dropIn, "After").includes(dep), `meshd is not After ${dep}`);
    assert(values(dropIn, "Wants").includes(dep), `meshd does not want ${dep}`);
  }

  const agent = f[`/etc/systemd/system/${NODE_UNITS.k3sAgent}`];
  for (const key of ["After", "Wants", "Requires", "BindsTo", "Requisite"]) {
    for (const forbidden of [NODE_NET_UNITS.meshd, NODE_NET_UNITS.meshdInstall, NODE_NET_UNITS.cni]) {
      assertEquals(values(agent, key).includes(forbidden), false, `the k3s agent is ${key} ${forbidden}: deadlock`);
    }
  }
  for (const unit of [MESHD_UNIT, dropIn, f[`/etc/systemd/system/${NODE_NET_UNITS.cni}`]]) {
    assertEquals(values(unit, "Before").includes(NODE_UNITS.k3sAgent), false, "a unit that waits for the Node is Before the agent");
  }
  for (const dep of [NODE_NET_UNITS.dnsLink, NODE_NET_UNITS.masquerade]) {
    assert(values(agent, "After").includes(dep), `the agent starts pods before ${dep}`);
  }
  const exec = values(agent, "ExecStart").join(" ");
  assertStringIncludes(exec, `--kubelet-arg=cluster-dns=${NODE_LOCAL_DNS_ADDRESS}`);
  assertStringIncludes(exec, `--node-label=${NODE_LOCAL_DNS_LABEL}=true`);

  const cni = f[`/etc/systemd/system/${NODE_NET_UNITS.cni}`];
  assert(values(cni, "After").includes(NODE_UNITS.nodeSecrets), "the CNI writer needs the node name");
  assertEquals(values(cni, "Restart"), ["on-failure"]);
  for (const name of Object.values(NODE_NET_UNITS)) {
    const unit = f[`/etc/systemd/system/${name}`];
    if (name !== NODE_NET_UNITS.meshd) assertEquals(values(unit, "StartLimitIntervalSec"), ["0"], name);
  }
  assertEquals(values(dropIn, "StartLimitIntervalSec"), ["0"]);

  const install = f[NODE_NET_SCRIPTS.meshdInstall];
  assertStringIncludes(install, PIN_X86);
  assertStringIncludes(install, PIN_ARM);
  assertStringIncludes(install, `${DOOR}/downloads/`);

  const start = config.runcmd.find((c) =>
    typeof c === "string" && c.startsWith("systemctl start --no-block") &&
    c.includes(NODE_UNITS.openbaoLink)
  ) as string;
  for (const name of Object.values(NODE_NET_UNITS)) assertStringIncludes(start, name);
  const guard = config.runcmd.find((c) => typeof c === "string" && c.includes("for p in ")) as string;
  assert(guard?.includes(`"iptables"`), `the masquerade unit needs iptables: ${guard}`);
  assertEquals(f["/etc/sysctl.d/90-socialweb-node.conf"], "net.ipv4.ip_forward = 1\n");
});

Deno.test("without a meshd pin the node still gets its bridge, masquerade and DNS, and no meshd", () => {
  const f = files(userData({}));
  for (const name of [NODE_NET_UNITS.cni, NODE_NET_UNITS.masquerade, NODE_NET_UNITS.dnsLink]) {
    assert(f[`/etc/systemd/system/${name}`], `no unit ${name}`);
  }
  for (const name of [NODE_NET_UNITS.meshd, NODE_NET_UNITS.meshdInstall]) {
    assertEquals(f[`/etc/systemd/system/${name}`], undefined, `${name} without a pin`);
  }
  assertEquals(f[NODE_NET_SCRIPTS.meshdInstall], undefined);
});

Deno.test("a meshd pin that is not a sha256, or a door that is not an http origin, is refused", () => {
  assertThrows(() => createK3sJoinModule({ meshd: { ...MESHD, sha256: { x86_64: "abc", aarch64: PIN_ARM } } }));
  assertThrows(() => createK3sJoinModule({ meshd: { ...MESHD, downloadBase: "https://door.test/'; rm -rf /" } }));
  assertThrows(() => createK3sJoinModule({ meshd: { ...MESHD, downloadBase: "file:///etc" } }));
});

async function shim(dir: string, name: string, body: string) {
  await Deno.writeTextFile(`${dir}/bin/${name}`, `#!/bin/bash\n${body}\n`, { mode: 0o755 });
}

async function runScript(script: string, dir: string, env: Record<string, string> = {}) {
  await Deno.writeTextFile(`${dir}/script`, script, { mode: 0o700 });
  const out = await new Deno.Command("bash", {
    args: [`${dir}/script`],
    env: { PATH: `${dir}/bin:/usr/bin:/bin`, ...env },
    stdout: "piped",
    stderr: "piped",
  }).output();
  return { code: out.code, stderr: new TextDecoder().decode(out.stderr) };
}

async function scratch() {
  const dir = await Deno.makeTempDir();
  await Deno.mkdir(`${dir}/bin`, { recursive: true });
  await shim(dir, "sleep", "exit 0");
  return dir;
}

Deno.test("pod-cni waits for the Node's podCIDR through the kubelet's own kubeconfig and writes the bridge conflist", async () => {
  const dir = await scratch();
  try {
    await Deno.writeTextFile(`${dir}/kubelet.kubeconfig`, "apiVersion: v1\n");
    await shim(
      dir,
      "k3s",
      `echo "$*" >> ${dir}/k3s.log\nn=$(cat ${dir}/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > ${dir}/n\n[ $n -ge 3 ] && printf '10.42.3.0/24'\nexit 0`,
    );
    const script = renderPodCniScript({
      k3sPath: `${dir}/bin/k3s`,
      cniConfDir: `${dir}/net.d`,
      cniBinDir: `${dir}/cni-bin`,
      k3sDataDir: `${dir}/k3s`,
      kubeletKubeconfig: `${dir}/kubelet.kubeconfig`,
      cniWaitAttempts: 5,
    });
    const run = await runScript(script, dir, { NODE_NAME: "ci-node1" });
    assertEquals(run.code, 0, run.stderr);
    const calls = (await Deno.readTextFile(`${dir}/k3s.log`)).trim().split("\n");
    assertEquals(calls.length, 3, "it polled until the podCIDR was allocated");
    assertEquals(calls[0], `kubectl --kubeconfig ${dir}/kubelet.kubeconfig get node ci-node1 -o jsonpath={.spec.podCIDR}`);
    const conf = JSON.parse(await Deno.readTextFile(`${dir}/net.d/10-socialweb-bridge.conflist`));
    assertEquals(conf.plugins.length, 2);
    const [bridge, portmap] = conf.plugins;
    assertEquals(bridge.type, "bridge");
    assertEquals(bridge.isGateway, true);
    assertEquals(bridge.ipMasq, false);
    assertEquals(bridge.mtu, POD_MTU);
    assertEquals(POD_MTU, 1160);
    assertEquals(bridge.ipam.type, "host-local");
    assertEquals(bridge.ipam.ranges, [[{ subnet: "10.42.3.0/24" }]]);
    assertEquals(portmap.type, "portmap");
    assertEquals(portmap.capabilities, { portMappings: true });
    for (const plugin of ["bridge", "host-local", "portmap", "loopback"]) {
      assertEquals(await Deno.readLink(`${dir}/cni-bin/${plugin}`), `${dir}/k3s/data/current/bin/cni`, plugin);
    }
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("pod-cni fails, for systemd to restart it, while the Node has no podCIDR, and refuses one that is not IPv4", async () => {
  const dir = await scratch();
  try {
    await Deno.writeTextFile(`${dir}/kubelet.kubeconfig`, "apiVersion: v1\n");
    const opts = { k3sPath: `${dir}/bin/k3s`, cniConfDir: `${dir}/net.d`, kubeletKubeconfig: `${dir}/kubelet.kubeconfig`, cniWaitAttempts: 2 };
    await shim(dir, "k3s", "exit 0");
    const none = await runScript(renderPodCniScript(opts), dir, { NODE_NAME: "ci-node1" });
    assertEquals(none.code, 1);
    assertStringIncludes(none.stderr, "no podCIDR");
    await shim(dir, "k3s", "printf '10.42.3.0/24; rm -rf /'");
    const bad = await runScript(renderPodCniScript(opts), dir, { NODE_NAME: "ci-node1" });
    assertEquals(bad.code, 1);
    assertStringIncludes(bad.stderr, "not an IPv4 CIDR");
    await Deno.stat(`${dir}/net.d/10-socialweb-bridge.conflist`).then(() => {
      throw new Error("a conflist was written");
    }, () => undefined);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the masquerade unit forwards and SNATs only traffic leaving the cluster CIDR, once", async () => {
  const dir = await scratch();
  try {
    await shim(dir, "sysctl", `echo "$*" >> ${dir}/sysctl.log`);
    await shim(dir, "iptables", `echo "$*" >> ${dir}/iptables.log\ncase "$*" in *" -C "*) [ -f ${dir}/added ] && exit 0; exit 1 ;; *" -A "*) touch ${dir}/added ;; esac`);
    const script = renderPodMasqueradeScript();
    assertEquals((await runScript(script, dir)).code, 0);
    assertEquals((await runScript(script, dir)).code, 0);
    assertStringIncludes(await Deno.readTextFile(`${dir}/sysctl.log`), "net.ipv4.ip_forward=1");
    const lines = (await Deno.readTextFile(`${dir}/iptables.log`)).trim().split("\n");
    const appends = lines.filter((l) => l.includes(" -A "));
    assertEquals(appends.length, 1, lines.join("\n"));
    assertEquals(
      appends[0],
      `-w -t nat -A POSTROUTING -s ${POD_CLUSTER_CIDR} ! -d ${POD_CLUSTER_CIDR} -m comment --comment socialweb-pod-egress -j MASQUERADE`,
    );
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the DNS link puts the per-node DNS address on a dummy interface, once", async () => {
  const dir = await scratch();
  try {
    await shim(dir, "modprobe", "exit 0");
    await shim(
      dir,
      "ip",
      `echo "$*" >> ${dir}/ip.log\ncase "$*" in "link show"*) [ -f ${dir}/link ] && exit 0; exit 1 ;; "link add"*) touch ${dir}/link ;; "-4 addr show"*) [ -f ${dir}/addr ] && echo "    inet ${NODE_LOCAL_DNS_ADDRESS}/32 scope global swdns0"; exit 0 ;; "addr add"*) touch ${dir}/addr ;; esac`,
    );
    assertEquals((await runScript(renderDnsLinkScript(), dir)).code, 0);
    assertEquals((await runScript(renderDnsLinkScript(), dir)).code, 0);
    const log = (await Deno.readTextFile(`${dir}/ip.log`)).trim().split("\n");
    assertEquals(log.filter((l) => l === "link add swdns0 type dummy").length, 1, log.join("\n"));
    assertEquals(log.filter((l) => l === `addr add ${NODE_LOCAL_DNS_ADDRESS}/32 dev swdns0`).length, 1, log.join("\n"));
    assert(log.includes("link set swdns0 up"));
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

async function sha256Hex(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

async function meshdInstall(arch: string, served: Uint8Array, preinstalled?: Uint8Array) {
  const dir = await scratch();
  const goodBytes = new TextEncoder().encode("#!/bin/sh\necho meshd\n");
  const pin = await sha256Hex(goodBytes);
  await Deno.writeFile(`${dir}/served`, served);
  if (preinstalled) await Deno.writeFile(`${dir}/socialweb-meshd`, preinstalled, { mode: 0o755 });
  await shim(dir, "uname", `echo ${arch}`);
  await shim(
    dir,
    "curl",
    `OUT=""; URL=""; while [ $# -gt 0 ]; do case "$1" in -o) OUT="$2"; shift 2 ;; -*) shift ;; *) URL="$1"; shift ;; esac; done\necho "$URL" >> ${dir}/curl.log\ncp ${dir}/served "$OUT"`,
  );
  const script = renderMeshdInstallScript({
    meshd: { downloadBase: DOOR, sha256: { x86_64: pin, aarch64: PIN_ARM } },
    meshdPath: `${dir}/socialweb-meshd`,
  });
  const run = await runScript(script, dir);
  const curl = await Deno.readTextFile(`${dir}/curl.log`).catch(() => "");
  return { dir, run, pin, goodBytes, curl: curl.trim() };
}

Deno.test("meshd-install keeps a node-image binary with the pinned sha256 and downloads nothing", async () => {
  const good = new TextEncoder().encode("#!/bin/sh\necho meshd\n");
  const r = await meshdInstall("x86_64", new Uint8Array(), good);
  try {
    assertEquals(r.run.code, 0, r.run.stderr);
    assertEquals(r.curl, "");
  } finally {
    await Deno.remove(r.dir, { recursive: true });
  }
});

Deno.test("meshd-install downloads its arch's pinned binary from the door by hash and installs it 0755", async () => {
  const good = new TextEncoder().encode("#!/bin/sh\necho meshd\n");
  const r = await meshdInstall("x86_64", good, new TextEncoder().encode("stale"));
  try {
    assertEquals(r.run.code, 0, r.run.stderr);
    assertEquals(r.curl, `${DOOR}/downloads/${r.pin}/socialweb-meshd-x86_64`);
    assertEquals(await Deno.readFile(`${r.dir}/socialweb-meshd`), good);
    assertEquals((await Deno.stat(`${r.dir}/socialweb-meshd`)).mode! & 0o777, 0o755);
  } finally {
    await Deno.remove(r.dir, { recursive: true });
  }
});

Deno.test("meshd-install refuses bytes the pin does not name and leaves nothing installed", async () => {
  const r = await meshdInstall("x86_64", new TextEncoder().encode("tampered"));
  try {
    assertEquals(r.run.code, 1);
    assertStringIncludes(r.run.stderr, "pinned");
    await Deno.stat(`${r.dir}/socialweb-meshd`).then(() => {
      throw new Error("tampered bytes were installed");
    }, () => undefined);
    const left = [...Deno.readDirSync(r.dir)].map((e) => e.name).filter((n) => n.startsWith("socialweb-meshd"));
    assertEquals(left, []);
  } finally {
    await Deno.remove(r.dir, { recursive: true });
  }
});

Deno.test("meshd-install asks the door for the aarch64 build on an arm64 node", async () => {
  const r = await meshdInstall("aarch64", new TextEncoder().encode("arm"));
  try {
    assertEquals(r.curl, `${DOOR}/downloads/${PIN_ARM}/socialweb-meshd-aarch64`);
    assertEquals(r.run.code, 1, "the served bytes are not the aarch64 pin");
  } finally {
    await Deno.remove(r.dir, { recursive: true });
  }
});

Deno.test("the guest, the tenant DaemonSet and the k3s server agree on the DNS address, its label and the cluster CIDR", async () => {
  const docs = (await Deno.readTextFile(repo("internal/tenantnet/manifests/node-local-dns.yaml"))).split(/^---$/m)
    .map((d) => parseYaml(d) as Record<string, any>);
  const ds = docs.find((d) => d.kind === "DaemonSet")!;
  assertEquals(ds.spec.template.spec.nodeSelector, { [NODE_LOCAL_DNS_LABEL]: "true" });
  const corefile = docs.find((d) => d.kind === "ConfigMap")!.data.Corefile as string;
  assertStringIncludes(corefile, `bind ${NODE_LOCAL_DNS_ADDRESS}\n`);
  const tenantnet = await Deno.readTextFile(repo("internal/tenantnet/tenantnet.go"));
  assertStringIncludes(tenantnet, `Address = "${NODE_LOCAL_DNS_ADDRESS}"`);
  assertStringIncludes(tenantnet, `NodeLabel = "${NODE_LOCAL_DNS_LABEL}"`);
  const stack = await Deno.readTextFile(repo("internal/clusterstack/clusterstack.go"));
  assertStringIncludes(stack, `const MarketClusterCIDR = "${POD_CLUSTER_CIDR}"`);
});

Deno.test("node-diagnose prints containerd's CNI load status to the console until the node network is ready", async () => {
  const dir = await scratch();
  try {
    await Deno.mkdir(`${dir}/cni-bin`);
    await Deno.writeTextFile(`${dir}/cni-bin/bridge`, "");
    await Deno.mkdir(`${dir}/net.d`);
    await Deno.writeTextFile(`${dir}/net.d/10-socialweb-bridge.conflist`, "{}");
    const notReady = JSON.stringify({
      status: { conditions: [{ type: "RuntimeReady", status: true }, { type: "NetworkReady", status: false, reason: "NetworkPluginNotReady", message: "cni plugin not initialized" }] },
      config: { cni: { confDir: `${dir}/net.d`, binDirs: [`${dir}/cni-bin`] } },
      lastCNILoadStatus: "cni config load failed: failed to find plugin \"portmap\" in path",
    });
    const ready = JSON.stringify({ status: { conditions: [{ type: "NetworkReady", status: true }] }, config: { cni: {} } });
    await Deno.writeTextFile(`${dir}/not-ready.json`, notReady);
    await Deno.writeTextFile(`${dir}/ready.json`, ready);
    await shim(
      dir,
      "k3s",
      `echo "$*" >> ${dir}/k3s.log\nn=$(cat ${dir}/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > ${dir}/n\nif [ $n -ge 3 ]; then cat ${dir}/ready.json; else cat ${dir}/not-ready.json; fi`,
    );
    const run = await runScript(renderNodeDiagnoseScript({ k3sPath: `${dir}/bin/k3s`, diagnoseAttempts: 5 }), dir);
    assertEquals(run.code, 0, run.stderr);
    assertEquals((await Deno.readTextFile(`${dir}/k3s.log`)).trim().split("\n")[0], "crictl info");
    assertStringIncludes(run.stderr, "failed to find plugin \"portmap\" in path");
    assertStringIncludes(run.stderr, `confDir=${dir}/net.d`);
    assertStringIncludes(run.stderr, "10-socialweb-bridge.conflist");
    assertStringIncludes(run.stderr, `binDirs=${dir}/cni-bin`);
    assertStringIncludes(run.stderr, "bridge");
    assertStringIncludes(run.stderr, "NetworkReady");
    const config = userData();
    const unit = files(config)[`/etc/systemd/system/${NODE_NET_UNITS.diagnose}`];
    assert(unit, "no node-diagnose unit in the user_data");
    assertEquals(values(unit, "ExecStart"), [NODE_NET_SCRIPTS.diagnose]);
    assertEquals(values(unit, "StandardError"), ["journal+console"]);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the bridge conflist and its plugins go where containerd reads them when k3s runs no flannel", () => {
  const script = renderPodCniScript({});
  assertStringIncludes(script, "/etc/cni/net.d/10-socialweb-bridge.conflist");
  assertStringIncludes(script, "install -d -m 0755 /etc/cni/net.d /opt/cni/bin");
  assertEquals(script.includes("/var/lib/rancher/k3s/agent/etc/cni/net.d"), false,
    "measured on a prod market node 2026-10-07T23:11Z: with --flannel-backend=none k3s writes no cni section into " +
      "containerd's config, so containerd reads /etc/cni/net.d and /opt/cni/bin (lastCNILoadStatus: no network config " +
      "found in /etc/cni/net.d) and a conflist under k3s's agent dir is never loaded");
});

Deno.test("node-diagnose says NetworkReady=false, not unknown, for a runtime that reports false", async () => {
  const dir = await scratch();
  try {
    await Deno.writeTextFile(`${dir}/info.json`, JSON.stringify({
      status: { conditions: [{ type: "NetworkReady", status: false, reason: "NetworkPluginNotReady", message: "x" }] },
      config: { cni: { confDir: "/x", binDirs: ["/y"] } },
    }));
    await shim(dir, "k3s", `cat ${dir}/info.json`);
    const run = await runScript(renderNodeDiagnoseScript({ k3sPath: `${dir}/bin/k3s`, diagnoseAttempts: 1 }), dir);
    assertStringIncludes(run.stderr, "NetworkReady=false");
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});
