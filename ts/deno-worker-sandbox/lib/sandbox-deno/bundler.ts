import type { Bundler, BundleRequest, BundleResponse } from "@publicdomainrelay/sandbox-abc";

async function tmpDir(): Promise<string> {
  const dir = await Deno.makeTempDir({ prefix: "sandbox-bundle-" });
  return dir;
}

async function writeText(path: string, content: string): Promise<void> {
  await Deno.writeTextFile(path, content);
}

async function checkSource(cwd: string): Promise<{ stdout: string; stderr: string; ok: boolean }> {
  const cmd = new Deno.Command("deno", {
    args: ["check", "--quiet", "main.ts"],
    cwd,
    stdout: "piped",
    stderr: "piped",
  });

  const output = await cmd.output();
  const stdout = new TextDecoder().decode(output.stdout);
  const stderr = new TextDecoder().decode(output.stderr);

  return { stdout, stderr, ok: output.success };
}

export function createDenoBundler(): Bundler {
  return {
    async bundle(request: BundleRequest): Promise<BundleResponse> {
      const dir = await tmpDir();
      try {
        await writeText(`${dir}/deno.json`, request.denoJson);
        if (request.denoLock) {
          await writeText(`${dir}/deno.lock`, request.denoLock);
        }
        await writeText(`${dir}/main.ts`, request.source);

        const result = await checkSource(dir);

        if (!result.ok) {
          return {
            bundleJs: "",
            stdout: result.stdout,
            stderr: result.stderr,
          };
        }

        return {
          bundleJs: request.source,
          stdout: result.stdout,
          stderr: result.stderr,
        };
      } finally {
        try {
          await Deno.remove(dir, { recursive: true });
        } catch { /* best-effort cleanup */ }
      }
    },
  };
}
