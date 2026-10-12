import type {
  Bundler,
  BundleRequest,
  BundleResponse,
  BundleTarRequest,
  BundleTarResponse,
} from "@publicdomainrelay/sandbox-abc";

async function tmpDir(): Promise<string> {
  return await Deno.makeTempDir({ prefix: "sandbox-bundle-" });
}

async function writeText(path: string, content: string): Promise<void> {
  await Deno.writeTextFile(path, content);
}

async function writeBytes(path: string, data: Uint8Array): Promise<void> {
  await Deno.writeFile(path, data);
}

async function readText(path: string): Promise<string> {
  return await Deno.readTextFile(path);
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch {
    return false;
  }
}

async function run(
  cwd: string,
  exe: string,
  args: string[],
): Promise<{ stdout: string; stderr: string; ok: boolean }> {
  const cmd = new Deno.Command(exe, {
    args,
    cwd,
    stdout: "piped",
    stderr: "piped",
  });

  const output = await cmd.output();
  const stdout = new TextDecoder().decode(output.stdout);
  const stderr = new TextDecoder().decode(output.stderr);

  return { stdout, stderr, ok: output.success };
}

async function checkSource(cwd: string): Promise<{ stdout: string; stderr: string; ok: boolean }> {
  return await run(cwd, "deno", ["check", "--quiet", "."]);
}

function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

async function extractTar(
  tarPath: string,
  destDir: string,
): Promise<{ ok: boolean; stderr: string }> {
  const result = await run(destDir, "tar", ["xf", tarPath]);
  return { ok: result.ok, stderr: result.stderr };
}

async function findEntrypoint(dir: string): Promise<string> {
  const denoJsonPath = `${dir}/deno.json`;
  const denoJson = JSON.parse(await readText(denoJsonPath));
  if (denoJson.exports && typeof denoJson.exports === "string") {
    const expPath = denoJson.exports.replace(/^\.\//, "");
    if (await exists(`${dir}/${expPath}`)) return expPath;
  }
  if (await exists(`${dir}/main.ts`)) return "main.ts";
  if (await exists(`${dir}/mod.ts`)) return "mod.ts";
  if (await exists(`${dir}/index.ts`)) return "index.ts";
  return "main.ts";
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
          return { bundleJs: "", stdout: result.stdout, stderr: result.stderr };
        }

        return { bundleJs: request.source, stdout: result.stdout, stderr: result.stderr };
      } finally {
        try {
          await Deno.remove(dir, { recursive: true });
        } catch { }
      }
    },

    async bundleTar(request: BundleTarRequest): Promise<BundleTarResponse> {
      const dir = await tmpDir();
      const tarPath = `${dir}/archive.tar`;
      try {
        let tarBytes: Uint8Array;
        try {
          tarBytes = base64ToBytes(request.tarBase64);
        } catch {
          return { bundleJs: "", stdout: "", stderr: "invalid base64 encoding" };
        }
        await writeBytes(tarPath, tarBytes);

        const extractResult = await extractTar(tarPath, dir);
        if (!extractResult.ok) {
          return {
            bundleJs: "",
            stdout: "",
            stderr: `tar extract failed: ${extractResult.stderr}`,
          };
        }

        if (!(await exists(`${dir}/deno.json`))) {
          return { bundleJs: "", stdout: "", stderr: "deno.json not found at archive root" };
        }

        const entrypoint = await findEntrypoint(dir);

        const checkResult = await checkSource(dir);

        if (!checkResult.ok) {
          return { bundleJs: "", stdout: checkResult.stdout, stderr: checkResult.stderr };
        }

        const bundleJs = await readText(`${dir}/${entrypoint}`);

        return { bundleJs, stdout: checkResult.stdout, stderr: checkResult.stderr };
      } finally {
        try {
          await Deno.remove(dir, { recursive: true });
        } catch { }
      }
    },
  };
}
