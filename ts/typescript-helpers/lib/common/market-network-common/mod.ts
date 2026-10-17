export const PUBLIC_PLC_DIRECTORY_URL = "https://plc.directory";
export const PUBLIC_INGRESS_PROXY_HOST = "xrpc.fedproxy.com";
export const PUBLIC_QR_ORIGIN = "https://qr.fedfork.com";

export interface MarketNetwork {
  readonly plcDirectoryUrl: string;
  readonly ingressProxyHost: string;
  readonly relayUrls: readonly string[];
  readonly firehoseUrls: readonly string[];
  readonly qrOrigin: string;
  readonly guestTlsUrl?: string;
  readonly caBundleRef?: string;
  readonly hosts?: Readonly<Record<string, string>>;
}

export interface MarketNetworkInput {
  plcDirectoryUrl?: string;
  ingressProxyHost?: string;
  relayUrls?: string | readonly string[];
  firehoseUrls?: string | readonly string[];
  qrOrigin?: string;
  guestTlsUrl?: string;
  caBundleRef?: string;
  hosts?: string | Readonly<Record<string, string>>;
}

function hostMap(v: string | Readonly<Record<string, string>> | undefined): Record<string, string> | undefined {
  if (!v) return undefined;
  const out: Record<string, string> = {};
  if (typeof v === "string") {
    for (const pair of urlList(v)) {
      const i = pair.indexOf("=");
      if (i > 0) out[pair.slice(0, i).trim()] = pair.slice(i + 1).trim();
    }
  } else {
    Object.assign(out, v);
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

export function hostAddressOf(network: MarketNetwork, hostname: string): string | undefined {
  return network.hosts?.[hostname];
}

export interface RewriteOptions {
  keepHttps?: boolean;
  hostPorts?: Readonly<Record<string, number>>;
}

function urlList(v: string | readonly string[] | undefined): string[] {
  const parts = typeof v === "string" ? v.split(",") : [...(v ?? [])];
  return parts.map((s) => s.trim()).filter(Boolean);
}

function stripTrailingSlash(url: string): string {
  return url.replace(/\/+$/, "");
}

export function marketNetwork(input: MarketNetworkInput = {}): MarketNetwork {
  return {
    plcDirectoryUrl: stripTrailingSlash(input.plcDirectoryUrl || PUBLIC_PLC_DIRECTORY_URL),
    ingressProxyHost: input.ingressProxyHost || PUBLIC_INGRESS_PROXY_HOST,
    relayUrls: urlList(input.relayUrls),
    firehoseUrls: urlList(input.firehoseUrls),
    qrOrigin: stripTrailingSlash(input.qrOrigin || PUBLIC_QR_ORIGIN),
    ...(input.guestTlsUrl ? { guestTlsUrl: input.guestTlsUrl } : {}),
    ...(input.caBundleRef ? { caBundleRef: input.caBundleRef } : {}),
    ...(hostMap(input.hosts) ? { hosts: hostMap(input.hosts) } : {}),
  };
}

export function explicitRelayUrls(network: MarketNetwork, firehoseMode = "off"): string[] | undefined {
  if (network.relayUrls.length === 0 && network.firehoseUrls.length === 0) return undefined;
  return [...new Set([...network.relayUrls, ...(firehoseMode === "subscriberepos" ? network.firehoseUrls : [])])];
}

export function isCrawlRoot(u: string): boolean {
  try {
    const url = new URL(u);
    return (url.protocol === "http:" || url.protocol === "https:") && (url.pathname === "/" || url.pathname === "");
  } catch {
    return false;
  }
}

export function splitHostPort(host: string): { hostname: string; port?: string } {
  const m = host.match(/^(\[[^\]]*\]|[^:]*)(?::(\d+))?$/);
  if (!m) return { hostname: host };
  return m[2] ? { hostname: m[1], port: m[2] } : { hostname: m[1] };
}

export function isLocalhostName(hostname: string): boolean {
  return hostname === "localhost" || hostname.endsWith(".localhost");
}

export function isLocalMarketNetwork(network: MarketNetwork): boolean {
  const { hostname } = splitHostPort(network.ingressProxyHost);
  return isLocalhostName(hostname) || hostname.startsWith("127.");
}

export function isPrivatePlcDirectory(network: MarketNetwork): boolean {
  try {
    return new URL(network.plcDirectoryUrl).hostname !== new URL(PUBLIC_PLC_DIRECTORY_URL).hostname;
  } catch {
    return false;
  }
}

export function ingressPort(network: MarketNetwork): string | undefined {
  return splitHostPort(network.ingressProxyHost).port;
}

export function rewriteMarketUrl(
  network: MarketNetwork,
  url: string,
  opts: RewriteOptions = {},
): string | undefined {
  const publicPlcPrefix = `${PUBLIC_PLC_DIRECTORY_URL}/`;
  if (url.startsWith(publicPlcPrefix) && isPrivatePlcDirectory(network)) {
    return network.plcDirectoryUrl + url.slice(PUBLIC_PLC_DIRECTORY_URL.length);
  }
  const named = url.match(/^https:\/\/([^/?#]+)(.*)$/s);
  if (named) {
    const address = hostAddressOf(network, splitHostPort(named[1]).hostname);
    if (address) return `https://${address}${named[2]}`;
  }
  if (!isLocalMarketNetwork(network)) return undefined;
  const m = url.match(/^https:\/\/([^/?#]+)(.*)$/s);
  if (!m) return undefined;
  const { hostname, port } = splitHostPort(m[1]);
  if (!isLocalhostName(hostname)) return undefined;
  const resolvedPort = port ?? opts.hostPorts?.[hostname]?.toString() ?? ingressPort(network);
  const host = resolvedPort ? `${hostname}:${resolvedPort}` : hostname;
  return `${opts.keepHttps ? "https" : "http"}://${host}${m[2]}`;
}
