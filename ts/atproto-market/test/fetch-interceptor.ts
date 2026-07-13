// Shared fetch interception for local integration tests.
// Handles plc.directory → local PLC redirect and https://*.localhost →
// http://*:dispPort downgrade. The *.localhost DNS resolution is handled
// transparently by the platform (macOS/Linux system resolver) or by
// container-backend-docker (Windows raw HTTP interceptor).

export function installFetchInterceptor(opts: {
  realFetch: typeof globalThis.fetch;
  plcDirectoryUrl: string;
  dispPort: number;
  /** Self-signed CA PEM. When set, *.localhost URLs keep HTTPS (no downgrade)
   * and are fetched with an HttpClient that trusts this CA. PLC redirects
   * are unaffected (local PLC serves plain HTTP). */
  caCertPem?: string;
}): () => void {
  const { realFetch, plcDirectoryUrl, dispPort, caCertPem } = opts;
  const client = caCertPem ? Deno.createHttpClient({ caCerts: [caCertPem] }) : undefined;

  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
    let url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("https://plc.directory/")) {
      url = plcDirectoryUrl + url.slice("https://plc.directory".length);
      return realFetch(new Request(url, input instanceof Request ? input : init));
    }
    const m = url.match(/^https:\/\/([^/]+)(\/.*)?$/);
    if (m && (m[1].endsWith(".localhost") || m[1] === "localhost" || m[1].includes(".localhost:"))) {
      let host = m[1];
      if (!host.includes(":")) host = `${host}:${dispPort}`;
      const protocol = client ? "https" : "http";
      url = `${protocol}://${host}${m[2] ?? ""}`;
      const req = new Request(url, input instanceof Request ? input : init);
      return client
        ? realFetch(req, { client } as RequestInit)
        : realFetch(req);
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;

  return () => {
    globalThis.fetch = realFetch;
    client?.close();
  };
}
