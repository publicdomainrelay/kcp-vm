// Shared fetch interception for local integration tests.
// Handles plc.directory → local PLC redirect and https://*.localhost →
// http://*:dispPort downgrade. The *.localhost DNS resolution is handled
// transparently by the platform (macOS/Linux system resolver) or by
// container-backend-docker (Windows raw HTTP interceptor).

export function installFetchInterceptor(opts: {
  realFetch: typeof globalThis.fetch;
  plcDirectoryUrl: string;
  dispPort: number;
}): () => void {
  const { realFetch, plcDirectoryUrl, dispPort } = opts;

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
      url = `http://${host}${m[2] ?? ""}`;
      return realFetch(new Request(url, input instanceof Request ? input : init));
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;

  return () => { globalThis.fetch = realFetch; };
}
