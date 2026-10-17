import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createPlcDirectoryFactory, FilePlcStore, MemoryPlcStore } from "@publicdomainrelay/hono-factory-did-plc-directory";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command(
  "CONFIG_PATH_HONO_PLC",
  cliArgsEnv,
  runtimeConfig,
).resolve();

const logger = createLogger({ serviceName: "hono-plc" });
const statePath = options.statePath as string | undefined;
const store = statePath ? new FilePlcStore(statePath) : new MemoryPlcStore();
const factory = createPlcDirectoryFactory({ store });

const port = options.port as number;
const hostname = (options.hostname as string) || "127.0.0.1";

const serve = createServe({
  logger,
  tcp: {
    addr: hostname,
    port,
    certFile: options.tlsCertFile as string | undefined,
    keyFile: options.tlsKeyFile as string | undefined,
  },
  portFile: options.portFile as string | undefined,
  listenersFile: options.listenersFile as string | undefined,
});
serve.app.route("/", factory.app as never);

function shutdown() {
  serve.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await serve.beginServe();
