import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createApp } from "@publicdomainrelay/policy-engine-server-gha-lite";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

const { options } = await new Command(
  "CONFIG_PATH_HONO_POLICY_ENGINE",
  cliArgsEnv,
  null,
).resolve();

const logger = createLogger({ serviceName: "hono-policy-engine" });
const bundledActionsDir = options.bundledActionsDir as string | undefined;
const { app } = createApp({
  netOnly: true,
  fsApi: false,
  ...(bundledActionsDir ? { bundledActionsDir } : {}),
});

const serve = createServe({
  logger,
  tcp: {
    addr: options.hostname as string,
    port: options.port as number,
    certFile: options.tlsCertFile as string | undefined,
    keyFile: options.tlsKeyFile as string | undefined,
  },
  portFile: options.portFile as string | undefined,
  listenersFile: options.listenersFile as string | undefined,
});
serve.app.route("/", app as never);

function shutdown() {
  serve.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await serve.beginServe();
