import { Command } from "@publicdomainrelay/cli-args-env";
import { createBidder } from "@publicdomainrelay/hono-factory-compute-bidder";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig = null;
try {
  const mod = await import("./config.json", { with: { type: "json" } });
  runtimeConfig = mod.default;
} catch { /* optional */ }

const { options } = await new Command(
  "CONFIG_PATH_HONO_BIDDER",
  cliArgsEnv,
  runtimeConfig,
).resolve();

const mode = options.computeProvider as string | undefined;
const computeProvider = mode
  ? {
      mode: mode as "local" | "digitalocean",
      token: options.computeProviderToken as string | undefined,
      baseUrl: options.computeProviderBaseUrl as string | undefined,
    }
  : undefined;

const bidder = await createBidder({
  port: options.port as number,
  privateKeyHex: options.privateKeyHex as string | undefined,
  plcDirectoryUrl: options.plcDirectoryUrl as string | undefined,
  dispatcherHost: options.dispatcherHost as string | undefined,
  label: options.label as string | undefined,
  computeProvider,
  registryEndpoint: options.registryEndpoint as string | undefined,
  heartbeatIntervalMs: options.heartbeatIntervalMs as number | undefined,
});

await bidder.ready;
console.log(`Bidder DID: ${bidder.did}`);
console.log(`Relay subdomain: ${bidder.relaySubdomain}`);
console.log(`Bidder proxyRef: ${bidder.proxyRef}`);

Deno.addSignalListener("SIGINT", () => {
  console.log("Shutting down...");
  bidder.stop();
  Deno.exit();
});
Deno.addSignalListener("SIGTERM", () => {
  console.log("Shutting down...");
  bidder.stop();
  Deno.exit();
});
