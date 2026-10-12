import type { CliResult } from "@publicdomainrelay/container-backend-common";
import type { ContainerBackend } from "@publicdomainrelay/container-backend-abc";

function cli(
  args: string[],
  opts?: { inherit?: boolean },
): Promise<CliResult> {
  const cmd = new Deno.Command("docker", {
    args,
    stdout: opts?.inherit ? "inherit" : "piped",
    stderr: opts?.inherit ? "inherit" : "piped",
  });
  return cmd.output().then((out) => ({
    code: out.code,
    stdout: opts?.inherit ? "" : new TextDecoder().decode(out.stdout).trim(),
    stderr: opts?.inherit ? "" : new TextDecoder().decode(out.stderr).trim(),
  }));
}

// ── Backend factory ──────────────────────────────────────────────────────

export function createDockerBackend(): ContainerBackend {
  return {
    type: "docker",
    bin: "docker",

    command: cli,

    async inspectIp(containerName: string): Promise<string> {
      const { code, stdout } = await cli([
        "inspect", "--format",
        "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
        containerName,
      ]);
      if (code !== 0) throw new Error(`docker inspect failed for ${containerName}`);
      return stdout;
    },

    async inspectGateway(containerName: string): Promise<string> {
      const { code, stdout } = await cli([
        "inspect", "--format",
        "{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}",
        containerName,
      ]);
      if (code !== 0) throw new Error(`docker inspect failed for ${containerName}`);
      if (!stdout) throw new Error(`no gateway found in docker inspect for ${containerName}`);
      return stdout;
    },

    async defaultGateway(): Promise<string> {
      const { code, stdout } = await cli([
        "network", "inspect", "bridge", "--format",
        "{{(index .IPAM.Config 0).Gateway}}",
      ]);
      if (code !== 0) throw new Error("docker network inspect failed");
      if (!stdout) throw new Error("no gateway found in docker network inspect");
      return stdout;
    },

    async imageExists(tag: string): Promise<boolean> {
      const { code, stdout } = await cli(["images", "-q", tag]);
      return code === 0 && stdout.length > 0;
    },

    async pullImage(image: string): Promise<void> {
      const { code, stderr } = await cli(["pull", image], { inherit: true });
      if (code !== 0) throw new Error(`docker pull failed for ${image}: ${stderr}`);
    },

    async rm(name: string): Promise<void> {
      await cli(["rm", "-f", name]);
    },

    async kill(name: string): Promise<void> {
      await cli(["kill", name]);
    },

    async exec(containerName: string, args: string[]): Promise<CliResult> {
      return cli(["exec", containerName, ...args]);
    },

    async isRunning(): Promise<boolean> {
      const { code } = await cli(["info"]);
      return code === 0;
    },

    async ensureRunning(): Promise<boolean> {
      if (await this.isRunning()) return true;

      for (const startCmd of [
        ["sudo", "service", "docker", "start"],
        ["sudo", "systemctl", "start", "docker"],
      ]) {
        const bin = startCmd[0];
        const binArgs = startCmd.slice(1);
        try {
          const c = new Deno.Command(bin, {
            args: binArgs,
            stdout: "null",
            stderr: "null",
          });
          const { code } = await c.output();
          if (code === 0) {
            for (let i = 0; i < 10; i++) {
              if (await this.isRunning()) return true;
              await new Promise((r) => setTimeout(r, 500));
            }
          }
        } catch (err) {
          console.error(`docker start command failed: ${bin} ${binArgs.join(" ")}: ${err}`);
        }
      }

      console.error("docker not running — could not start daemon");
      return false;
    },

    logStream(containerName: string): ReadableStream<string> {
      const dockerBin = "docker";
      const dockerArgs = ["logs", "-f", containerName];
      return new ReadableStream({
        start: async (controller) => {
          const child = new Deno.Command(dockerBin, {
            args: dockerArgs,
            stdout: "piped",
          }).spawn();
          const reader = child.stdout.getReader();
          const decoder = new TextDecoder();
          let leftover = "";
          try {
            while (true) {
              const { value, done } = await reader.read();
              if (done) break;
              leftover += decoder.decode(value, { stream: true });
              const lines = leftover.split("\n");
              leftover = lines.pop() ?? "";
              for (const line of lines) {
                if (line.trim()) controller.enqueue(line);
              }
            }
          } finally {
            controller.close();
            try { child.kill("SIGTERM"); } catch { /* already exited */ }
          }
        },
      });
    },
  };
}
