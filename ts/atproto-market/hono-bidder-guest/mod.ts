import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { marketNetwork } from "@publicdomainrelay/market-network-common";
import { createMarketBidderGuestApp } from "@publicdomainrelay/hono-factory-market-bidder-guest-app";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_HONO_BIDDER_GUEST", cliArgsEnv, runtimeConfig).resolve();
const o: Record<string, any> = options;

if (!o.denopodNamespace || !o.statusFile) {
  console.error(
    "hono-bidder-guest runs only as the entry of a bidder's guest DenoPod and this process has no DenoPod kcp environment; start a bidder with `socialweb-computer run bidder`",
  );
  Deno.exit(2);
}
const missing = ["vaultKcpServer", "kcpServer", "kcpTokenFile", "handle"].filter((k) => !o[k]);
if (missing.length > 0) {
  console.error(`hono-bidder-guest: ${missing.map((k) => "--" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())).join(", ")} not set`);
  Deno.exit(2);
}

const logger = createLogger({ serviceName: o.serviceName as string });

const app = createMarketBidderGuestApp({
  role: o.role,
  network: marketNetwork({ plcDirectoryUrl: o.plcDirectoryUrl, ingressProxyHost: o.ingressProxyHost, hosts: o.networkHosts }),
  vault: {
    name: o.vaultOpenbaoServer,
    client: o.vaultOpenbaoClient,
    kvMountClient: o.vaultKvMountClient,
    kcp: { server: o.vaultKcpServer, tokenFile: o.vaultKcpTokenFile, caCertFile: o.vaultKcpCaCertFile },
  },
  kcp: { server: o.kcpServer, workspace: o.kcpWorkspace, handle: o.handle, tokenFile: o.kcpTokenFile, caFile: o.kcpCaFile },
  serve: { addr: o.serveAddr, port: o.servePort },
  reports: { portFile: o.portFile, listenersFile: o.listenersFile },
}, { logger });

const shutdown = () => {
  app.shutdown();
  Deno.exit();
};
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

let status;
try {
  status = await app.start();
} catch (err) {
  app.shutdown();
  logger.error("guest_start_failed", { error: err instanceof Error ? err.message : String(err) });
  Deno.exit(1);
}

const statusFile = o.statusFile as string;
await Deno.writeTextFile(`${statusFile}.tmp`, JSON.stringify(status) + "\n");
await Deno.rename(`${statusFile}.tmp`, statusFile);
logger.info("guest_status_written", { path: statusFile, ...status });
