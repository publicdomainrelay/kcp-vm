import { assert, assertEquals, assertStringIncludes } from "@std/assert";

const ROOT = new URL("../", import.meta.url).pathname;
const DEPLOY = `${ROOT}scripts/deploy.sh`;
const TS = "20261012T000000Z";
const TARGET = "root@deploy-test.invalid";
const BACKUP = `/root/qr-backup/${TS}`;
const STAGE = `/tmp/qr-stage-${TS}`;
const ROUTE = "/xrpc/com.fedfork.atprotoOauthQR";

interface DryRun {
  code: number;
  connected: boolean;
  lines: string[];
  remote: string[];
  uploads: string[];
  restoreScript: string[];
  stderr: string;
}

async function dryRun(...args: string[]): Promise<DryRun> {
  const fake = await Deno.makeTempDir();
  const marker = `${fake}/connected`;
  for (const tool of ["ssh", "scp", "rsync", "sftp"]) {
    await Deno.writeTextFile(`${fake}/${tool}`, `#!/bin/sh\ntouch ${marker}\nexit 97\n`, { mode: 0o755 });
  }
  const out = await new Deno.Command("bash", {
    args: [DEPLOY, "--dry-run", ...args],
    cwd: ROOT,
    env: {
      PATH: `${fake}:${Deno.env.get("PATH")}`,
      SSH_TARGET: TARGET,
      DEPLOY_TS: TS,
    },
    stdout: "piped",
    stderr: "piped",
  }).output();
  const connected = await Deno.stat(marker).then(() => true, () => false);
  await Deno.remove(fake, { recursive: true });
  const lines = new TextDecoder().decode(out.stdout).split("\n");
  const pick = (prefix: string) => lines.filter((l) => l.startsWith(prefix)).map((l) => l.slice(prefix.length));
  return {
    code: out.code,
    connected,
    lines,
    remote: pick("remote: "),
    uploads: pick("upload: "),
    restoreScript: pick("restore.sh: "),
    stderr: new TextDecoder().decode(out.stderr),
  };
}

async function metadataSha(): Promise<string> {
  const bytes = await Deno.readFile(`${ROOT}dist/oauth-client-metadata.json`);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

function staticSteps(sha: string): string[] {
  return [
    `caddy validate --config ${STAGE}/Caddyfile --adapter caddyfile`,
    "rm -rf /var/www/qr.fedfork.com.new /var/www/qr.fedfork.com.old",
    "mkdir -p /var/www/qr.fedfork.com.new",
    `cp -a ${STAGE}/www/. /var/www/qr.fedfork.com.new/`,
    "chown -R caddy:caddy /var/www/qr.fedfork.com.new",
    "if [ -d /var/www/qr.fedfork.com ]; then mv /var/www/qr.fedfork.com /var/www/qr.fedfork.com.old; fi",
    "mv /var/www/qr.fedfork.com.new /var/www/qr.fedfork.com",
    "rm -rf /var/www/qr.fedfork.com.old",
    `install -m 0644 ${STAGE}/Caddyfile /etc/caddy/Caddyfile`,
    "systemctl reload caddy",
    `got=$(curl -fsS --resolve qr.fedfork.com:443:127.0.0.1 https://qr.fedfork.com/oauth-client-metadata.json | sha256sum | cut -d' ' -f1 || true); if [ "$got" != ${sha} ]; then echo "static check failed: oauth-client-metadata.json sha256 $got, want ${sha}" >&2; exit 1; fi`,
    `rm -rf ${STAGE}`,
  ];
}

const TRAP =
  `trap 'rc=$?; if [ $rc -ne 0 ]; then echo "deploy failed (rc=$rc), restoring from ${BACKUP}" >&2; bash ${BACKUP}/restore.sh || true; fi' EXIT`;

function fullSteps(sha: string): string[] {
  return [
    "set -euo pipefail",
    `mkdir -p ${BACKUP}`,
    `if [ -d /var/www/qr.fedfork.com ]; then cp -a /var/www/qr.fedfork.com ${BACKUP}/www; fi`,
    `if [ -f /etc/caddy/Caddyfile ]; then cp -a /etc/caddy/Caddyfile ${BACKUP}/Caddyfile; fi`,
    `mkdir -p ${BACKUP}/backend`,
    `if [ -d /opt/qr-fedfork ]; then find /opt/qr-fedfork -maxdepth 1 -type f ! -name 'kv.db*' -exec cp -a {} ${BACKUP}/backend/ \\;; fi`,
    `if [ -f /etc/systemd/system/qr-fedfork-backend.service ]; then cp -a /etc/systemd/system/qr-fedfork-backend.service ${BACKUP}/qr-fedfork-backend.service; fi`,
    `install -m 0700 ${STAGE}/restore.sh ${BACKUP}/restore.sh`,
    TRAP,
    `if [ -f /etc/systemd/system/qr-fedfork-backend.service ]; then diff -u /etc/systemd/system/qr-fedfork-backend.service ${STAGE}/qr-fedfork-backend.service || true; fi`,
    "mkdir -p /opt/qr-fedfork",
    `install -m 0755 ${STAGE}/qr-fedfork-backend /opt/qr-fedfork/qr-fedfork-backend.new`,
    "mv -f /opt/qr-fedfork/qr-fedfork-backend.new /opt/qr-fedfork/qr-fedfork-backend",
    `install -m 0644 ${STAGE}/qr-fedfork-backend.service /etc/systemd/system/qr-fedfork-backend.service`,
    "systemctl daemon-reload",
    "systemctl enable qr-fedfork-backend.service",
    "systemctl restart qr-fedfork-backend.service",
    `code=000; for i in $(seq 1 30); do code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:5557${ROUTE} || true); if [ "$code" = 401 ]; then break; fi; sleep 1; done; if [ "$code" != 401 ]; then echo "backend unhealthy: GET http://127.0.0.1:5557${ROUTE} answered $code, want 401" >&2; exit 1; fi`,
    ...staticSteps(sha),
    `echo deployed mode=full backup=${BACKUP}`,
  ];
}

function staticOnlySteps(sha: string): string[] {
  return [
    "set -euo pipefail",
    `mkdir -p ${BACKUP}`,
    `if [ -d /var/www/qr.fedfork.com ]; then cp -a /var/www/qr.fedfork.com ${BACKUP}/www; fi`,
    `if [ -f /etc/caddy/Caddyfile ]; then cp -a /etc/caddy/Caddyfile ${BACKUP}/Caddyfile; fi`,
    `install -m 0700 ${STAGE}/restore.sh ${BACKUP}/restore.sh`,
    TRAP,
    ...staticSteps(sha),
    `echo deployed mode=static backup=${BACKUP}`,
  ];
}

const upload = (rel: string) => `${rel} -> ${TARGET}:${STAGE}/${rel}`;

Deno.test("deploy --dry-run builds, connects to nothing and lists every upload and remote command", async () => {
  const run = await dryRun();
  assertEquals(run.code, 0, run.stderr);
  assertEquals(run.connected, false, "dry-run invoked ssh/scp");
  assertEquals(run.remote, fullSteps(await metadataSha()));
  for (
    const rel of [
      "Caddyfile",
      "qr-fedfork-backend",
      "qr-fedfork-backend.service",
      "remote.sh",
      "restore.sh",
      "www/index.html",
      "www/oauth-client-metadata.json",
      "www/dka-app.js",
      "www/styles.css",
    ]
  ) assert(run.uploads.includes(upload(rel)), `missing upload ${rel}`);
  assert(run.lines.includes(`backup: ${BACKUP}`));
  assert(run.lines.includes(`restore: ssh ${TARGET} bash ${BACKUP}/restore.sh`));
  assert(run.lines.includes("unit: ExecStart=/opt/qr-fedfork/qr-fedfork-backend"));
  assert(run.lines.some((l) => l.startsWith("local smoke: backend started from an empty dir")));
  assertStringIncludes(run.restoreScript.join("\n"), `cp -a ${BACKUP}/backend/. /opt/qr-fedfork/`);
});

Deno.test("deploy --static-only --dry-run names no backend file or unit", async () => {
  const run = await dryRun("--static-only");
  assertEquals(run.code, 0, run.stderr);
  assertEquals(run.connected, false, "dry-run invoked ssh/scp");
  assertEquals(run.remote, staticOnlySteps(await metadataSha()));
  const touched = [...run.remote, ...run.uploads, ...run.restoreScript].join("\n");
  for (const forbidden of ["qr-fedfork-backend", "/opt/qr-fedfork", "daemon-reload", "/etc/systemd"]) {
    assertEquals(touched.includes(forbidden), false, `static-only mentions ${forbidden}`);
  }
  assert(run.uploads.includes(upload("www/oauth-client-metadata.json")));
  assert(run.uploads.includes(upload("Caddyfile")));
});

Deno.test("deploy never turns on xtrace and never copies the session store into a backup", async () => {
  const script = await Deno.readTextFile(DEPLOY);
  assertEquals(/set -[a-z]*x|bash -[a-z]*x/.test(script), false);
  const run = await dryRun("--static-only");
  const full = await dryRun();
  for (const cmd of [...run.remote, ...full.remote]) {
    if (cmd.includes("/opt/qr-fedfork") && cmd.includes(BACKUP)) assertStringIncludes(cmd, "! -name 'kv.db*'");
  }
});

Deno.test("the compiled backend starts on port 0 from an empty dir with no sibling repos and answers its route offline", async () => {
  const build = await new Deno.Command("deno", {
    args: ["task", "compile:backend"],
    cwd: ROOT,
    stdout: "piped",
    stderr: "piped",
  }).output();
  assertEquals(build.code, 0, new TextDecoder().decode(build.stderr));
  const dir = await Deno.makeTempDir();
  try {
    await Deno.copyFile(`${ROOT}dist-backend/qr-fedfork-backend`, `${dir}/qr-fedfork-backend`);
    await Deno.chmod(`${dir}/qr-fedfork-backend`, 0o755);
    const child = new Deno.Command(`${dir}/qr-fedfork-backend`, {
      args: ["--port", "0", "--port-file", "./port"],
      cwd: dir,
      clearEnv: true,
      env: { HOME: dir, PATH: "/usr/bin:/bin" },
      stdout: "null",
      stderr: "null",
    }).spawn();
    try {
      let port = "";
      for (let i = 0; i < 100 && !port; i++) {
        port = await Deno.readTextFile(`${dir}/port`).then((s) => s.trim(), () => "");
        if (!port) await new Promise((r) => setTimeout(r, 100));
      }
      assert(port, "backend never wrote its port file");
      assert(Number(port) > 0);
      const get = await fetch(`http://127.0.0.1:${port}${ROUTE}`);
      assertEquals(get.status, 401);
      assertEquals((await get.json()).error, "Unauthorized");
      const post = await fetch(`http://127.0.0.1:${port}${ROUTE}`, { method: "POST", body: "not json" });
      assertEquals(post.status, 400);
      assertEquals((await post.json()).error, "InvalidRequest");
    } finally {
      child.kill("SIGTERM");
      await child.status;
    }
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});
