import { marketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import type { MarketNetworkFetchFailure } from "@publicdomainrelay/market-network-fetch";

export function installFetchInterceptor(opts: {
  realFetch: typeof globalThis.fetch;
  plcDirectoryUrl: string;
  dispPort: number;
  caCertPem?: string;
  hostPorts?: Readonly<Record<string, number>>;
  onFetchError?: (failure: MarketNetworkFetchFailure) => void;
}): () => void {
  return installMarketNetworkFetch({
    network: marketNetwork({
      plcDirectoryUrl: opts.plcDirectoryUrl,
      ingressProxyHost: opts.dispPort ? `localhost:${opts.dispPort}` : "localhost",
    }),
    realFetch: opts.realFetch,
    caCertPem: opts.caCertPem,
    hostPorts: opts.hostPorts,
    onFetchError: opts.onFetchError,
  });
}
