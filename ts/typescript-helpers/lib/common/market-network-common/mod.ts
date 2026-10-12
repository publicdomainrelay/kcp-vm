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
}

export interface MarketNetworkInput {
  plcDirectoryUrl?: string;
  ingressProxyHost?: string;
  relayUrls?: string | readonly string[];
  firehoseUrls?: string | readonly string[];
  qrOrigin?: string;
  guestTlsUrl?: string;
  caBundleRef?: string;
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
  };
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
  if (!isLocalMarketNetwork(network)) return undefined;
  const m = url.match(/^https:\/\/([^/?#]+)(.*)$/s);
  if (!m) return undefined;
  const { hostname, port } = splitHostPort(m[1]);
  if (!isLocalhostName(hostname)) return undefined;
  const resolvedPort = port ?? opts.hostPorts?.[hostname]?.toString() ?? ingressPort(network);
  const host = resolvedPort ? `${hostname}:${resolvedPort}` : hostname;
  return `${opts.keepHttps ? "https" : "http"}://${host}${m[2]}`;
}
