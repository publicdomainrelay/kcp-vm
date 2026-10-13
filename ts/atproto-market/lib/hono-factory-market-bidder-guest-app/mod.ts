import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import type { ServeHandle } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import type { MarketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import { createOpenBaoServerResolver } from "@publicdomainrelay/openbao-server-kcp";
import { vaultGuestKv, vaultOidcJwkStore, vaultRelayKeypair } from "@publicdomainrelay/market-bidder-vault-keys";
import { createComputeProviderKcpGuestFactory } from "@publicdomainrelay/hono-factory-compute-provider-kcp";

export interface MarketBidderGuestVaultSpec {
  name: string;
  client: string;
  kcp: { server: string; workspace?: string; token?: string; tokenFile?: string; caCertFile?: string };
}

export interface MarketBidderGuestKcpSpec {
  server: string;
  workspace: string;
  handle: string;
  tokenFile?: string;
  caFile?: string;
}

export interface MarketBidderGuestAppSpec {
  role: string;
  network: MarketNetwork;
  vault: MarketBidderGuestVaultSpec;
  kcp: MarketBidderGuestKcpSpec;
  serve: { addr?: string; port?: number };
  reports?: { portFile?: string; listenersFile?: string };
}

export interface MarketBidderGuestAppStatus {
  role: string;
  issuerUrl: string;
  ingressRef: string;
  servePort: number;
}

export interface MarketBidderGuestApp {
  start(): Promise<MarketBidderGuestAppStatus>;
  shutdown(): void;
}

export function createMarketBidderGuestApp(
  spec: MarketBidderGuestAppSpec,
  ports: { logger: StructuredLoggerInterface },
): MarketBidderGuestApp {
  const { logger } = ports;
  let serve: ServeHandle | undefined;

  async function start(): Promise<MarketBidderGuestAppStatus> {
    installMarketNetworkFetch({ network: spec.network });
    const ref = spec.vault;
    const resolve = createOpenBaoServerResolver({
      server: ref.kcp.server,
      workspace: ref.kcp.workspace,
      tokenFile: ref.kcp.tokenFile,
      token: ref.kcp.token,
      caCert: ref.kcp.caCertFile ? await Deno.readTextFile(ref.kcp.caCertFile) : undefined,
      name: ref.name,
      client: ref.client,
      onWaiting: (reason) => logger.info("guest_vault_waiting", { openbaoserver: ref.name, reason }),
    });
    const resolved = await resolve();
    logger.info("guest_vault_resolved", { openbaoserver: resolved.name, address: resolved.address, kvMount: resolved.kvMount, role: resolved.certLogin.role });
    const vault = createOpenBaoIdentityVault({
      endpoint: async () => {
        const r = await resolve();
        return { address: r.address, caCert: r.caCert, clientCert: r.clientCert, clientKey: r.clientKey, certLogin: r.certLogin };
      },
    });
    const mount = resolved.kvMount;
    const relayKeypair = await vaultRelayKeypair(vault, mount, spec.role, logger);
    const relay = createIngress({
      logger,
      ingressProxyHost: spec.network.ingressProxyHost,
      signer: relayKeypair,
      keypair: relayKeypair,
      label: `guest-${spec.role}`,
    });
    const guest = createComputeProviderKcpGuestFactory({
      logger,
      credentials: { server: spec.kcp.server, workspace: spec.kcp.workspace, tokenFile: spec.kcp.tokenFile, caFile: spec.kcp.caFile },
      handle: spec.kcp.handle,
      guestKv: vaultGuestKv(vault, mount),
      getIssuerUrl: () => relay.ingressUrl,
      oidcJwkStore: vaultOidcJwkStore(vault, mount, spec.role, logger),
      plcDirectoryUrl: spec.network.plcDirectoryUrl,
    });
    serve = createServe({
      logger,
      tcp: { addr: spec.serve.addr || "127.0.0.1", port: spec.serve.port ?? 0 },
      portFile: spec.reports?.portFile,
      listenersFile: spec.reports?.listenersFile,
      relays: [relay],
    });
    serve.app.route("/", guest.createApp());
    await serve.beginServe();
    const status = { role: spec.role, issuerUrl: relay.ingressUrl, ingressRef: relay.ingressRef, servePort: serve.tcpPort };
    logger.info("guest_endpoints_ready", status);
    return status;
  }

  function shutdown(): void {
    serve?.shutdown();
  }

  return { start, shutdown };
}
