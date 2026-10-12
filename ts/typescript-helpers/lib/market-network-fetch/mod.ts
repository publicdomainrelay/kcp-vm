import { hostAddressOf, PUBLIC_PLC_DIRECTORY_URL, rewriteMarketUrl, splitHostPort } from "@publicdomainrelay/market-network-common";
import type { MarketNetwork } from "@publicdomainrelay/market-network-common";

export interface MarketNetworkFetchFailure {
  url: string;
  cause: string;
}

export interface MarketNetworkFetchOptions {
  network: MarketNetwork;
  realFetch?: typeof fetch;
  caCertPem?: string;
  hostPorts?: Readonly<Record<string, number>>;
  onFetchError?: (failure: MarketNetworkFetchFailure) => void;
}

export interface MarketNetworkFetch {
  fetch: typeof fetch;
  close(): void;
}

function inputUrl(input: string | URL | Request): string {
  return typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
}

export function createMarketNetworkFetch(opts: MarketNetworkFetchOptions): MarketNetworkFetch {
  const realFetch = opts.realFetch ?? globalThis.fetch;
  const client = opts.caCertPem ? Deno.createHttpClient({ caCerts: [opts.caCertPem] }) : undefined;
  const rewriteOpts = { keepHttps: client !== undefined, hostPorts: opts.hostPorts };

  function send(input: string | URL | Request, init?: RequestInit): Promise<Response> {
    const url = inputUrl(input);
    const rewritten = rewriteMarketUrl(opts.network, url, rewriteOpts);
    if (rewritten === undefined) return realFetch(input, init);
    const original = url.match(/^https:\/\/([^/?#]+)/)?.[1];
    if (original && hostAddressOf(opts.network, splitHostPort(original).hostname)) {
      const headers = new Headers(input instanceof Request ? input.headers : init?.headers);
      headers.set("host", original);
      const named = input instanceof Request ? new Request(rewritten, input) : rewritten;
      return realFetch(named, { ...init, headers });
    }
    const target = input instanceof Request ? new Request(rewritten, input) : rewritten;
    const isPlcDirectory = url.startsWith(`${PUBLIC_PLC_DIRECTORY_URL}/`);
    if (client && !isPlcDirectory) {
      return realFetch(target, { ...init, client } as RequestInit);
    }
    return realFetch(target, init);
  }

  const marketFetch = (async (input: string | URL | Request, init?: RequestInit) => {
    if (!opts.onFetchError) return await send(input, init);
    try {
      return await send(input, init);
    } catch (err) {
      const cause = (err as { cause?: unknown }).cause;
      opts.onFetchError({ url: inputUrl(input), cause: cause === undefined ? String(err) : String(cause) });
      throw err;
    }
  }) as typeof fetch;

  return {
    fetch: marketFetch,
    close: () => client?.close(),
  };
}

export function installMarketNetworkFetch(opts: MarketNetworkFetchOptions): () => void {
  const previous = globalThis.fetch;
  const adapter = createMarketNetworkFetch({ ...opts, realFetch: opts.realFetch ?? previous });
  globalThis.fetch = adapter.fetch;
  return () => {
    globalThis.fetch = previous;
    adapter.close();
  };
}
