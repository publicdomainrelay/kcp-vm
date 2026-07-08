import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createPlcDirectoryFactory, MemoryPlcStore } from "@publicdomainrelay/hono-factory-did-plc-directory";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command(
  "CONFIG_PATH_HONO_PLC",
  cliArgsEnv,
  runtimeConfig,
).resolve();

const logger = createLogger({ serviceName: "hono-plc" });
const store = new MemoryPlcStore();
const factory = createPlcDirectoryFactory({ store });

const port = options.port as number;
const hostname = (options.hostname as string) || "127.0.0.1";

const serve = createServe({
  logger,
  tcp: { addr: hostname, port },
});
serve.app.route("/", factory.app as never);

function shutdown() {
  serve.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await serve.beginServe();
