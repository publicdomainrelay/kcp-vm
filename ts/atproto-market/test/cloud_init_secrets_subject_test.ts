import { assertEquals } from "@std/assert";
import { getUserDataModules } from "@publicdomainrelay/cloud-init-common";

function b64url(text: string): string {
  return btoa(text).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function tokenWithSubject(sub: string): string {
  return `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify({ sub }))}.sig`;
}

function subjectWithPayloadLengthMod4(want: number): { sub: string; token: string } {
  for (let n = 0; n < 64; n++) {
    const sub = `actx:a:plc:p:role:${"x".repeat(n)}`;
    const token = tokenWithSubject(sub);
    if (token.split(".")[1].length % 4 === want) return { sub, token };
  }
  throw new Error(`no subject gives a payload length of ${want} mod 4`);
}

async function runSetupSecrets(token: string): Promise<{ code: number; stderr: string; written: string; exchangedSub: string }> {
  const dir = await Deno.makeTempDir({ prefix: "setup-secrets-" });
  try {
    const [mod] = getUserDataModules(["secrets"]);
    const files = mod({
      secretsUrl: "https://secrets.test",
      secretsRoute: "/xrpc/get",
      secretsAud: "api://test",
      secretsAcceptPath: `${dir}/accept.json`,
    } as never).write_files as Array<{ path: string; content: string }>;
    const script = files.find((f) => f.path === "/usr/local/bin/setup-secrets.sh")!.content
      .replaceAll("/var/lib/setup-secrets.done", `${dir}/done`)
      .replaceAll(" -o root -g root", "")
      .replaceAll('chown root:root "${SECRET_PATH}"', "true");
    await Deno.writeTextFile(`${dir}/setup-secrets.sh`, script);
    await Deno.writeTextFile(`${dir}/token`, token);
    await Deno.writeTextFile(`${dir}/base_url`, "https://issuer.test");
    await Deno.writeTextFile(
      `${dir}/accept.json`,
      JSON.stringify({ bid_config: { value: { token_path: `${dir}/token`, url_path: `${dir}/base_url` } } }),
    );
    await Deno.mkdir(`${dir}/bin`);
    await Deno.writeTextFile(
      `${dir}/bin/curl`,
      `#!/bin/bash
for a in "$@"; do
  case "$a" in
    -d) next=body ;;
    *) if [ "\${next:-}" = body ]; then printf '%s' "$a" > "${dir}/exchange.json"; next=; fi ;;
  esac
  last="$a"
done
case "$last" in
  https://issuer.test/*) echo '{"token":"exchanged"}' ;;
  https://secrets.test/*) printf '[{"path":"%s","value":"v1"}]' "${dir}/out/secret" ;;
  *) exit 22 ;;
esac
`,
    );
    await Deno.chmod(`${dir}/bin/curl`, 0o755);
    const out = await new Deno.Command("bash", {
      args: [`${dir}/setup-secrets.sh`],
      env: { PATH: `${dir}/bin:${Deno.env.get("PATH") ?? ""}` },
      stdout: "piped",
      stderr: "piped",
    }).output();
    const written = await Deno.readTextFile(`${dir}/out/secret`).catch(() => "");
    const exchangedSub = await Deno.readTextFile(`${dir}/exchange.json`).then((t) => JSON.parse(t).sub).catch(() => "");
    return { code: out.code, stderr: new TextDecoder().decode(out.stderr), written, exchangedSub };
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
}

for (const mod4 of [0, 2, 3]) {
  Deno.test(`setup-secrets reads the subject of a token whose payload length is ${mod4} mod 4 and fetches the bundle`, async () => {
    const { sub, token } = subjectWithPayloadLengthMod4(mod4);
    const r = await runSetupSecrets(token);
    assertEquals(r.code, 0, r.stderr);
    assertEquals(r.exchangedSub, sub);
    assertEquals(r.written, "v1");
  });
}
