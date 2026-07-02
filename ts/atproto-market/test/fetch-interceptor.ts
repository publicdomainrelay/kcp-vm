// Shared fetch interception for local integration tests.
// Handles *.localhost DNS resolution cross-platform: macOS resolves *.localhost
// via system resolver, Windows does not. Deno's fetch() ignores manual Host
// headers, so *.localhost URLs use raw HTTP via Deno.connect to 127.0.0.1
// with the correct Host header for dispatcher subdomain routing.

const encoder = new TextEncoder();

async function rawHttpFetch(dispPort: number, urlStr: string, init?: RequestInit): Promise<Response> {
  const u = new URL(urlStr);
  const method = init?.method ?? "GET";
  const headers = new Headers(init?.headers);
  headers.set("Host", u.host);
  const bodyStr = init?.body as string | undefined;
  if (bodyStr && !headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  if (bodyStr && !headers.has("content-length")) {
    headers.set("content-length", String(encoder.encode(bodyStr).length));
  }

  const headerLines = [`${method} ${u.pathname}${u.search} HTTP/1.1`];
  for (const [k, v] of headers) headerLines.push(`${k}: ${v}`);
  headerLines.push("connection: close");
  const reqStr = headerLines.join("\r\n") + "\r\n\r\n";
  const reqBytes = encoder.encode(reqStr);

  const conn = await Deno.connect({ hostname: "127.0.0.1", port: dispPort });
  try {
    await conn.write(reqBytes);
    if (bodyStr) await conn.write(encoder.encode(bodyStr));
    // Read until connection close
    const chunks: Uint8Array[] = [];
    const readBuf = new Uint8Array(8192);
    while (true) {
      let n: number | null = null;
      try { n = await conn.read(readBuf); } catch { break; }
      if (n === null || n === 0) break;
      chunks.push(readBuf.slice(0, n));
    }
    const rawBytes = chunks.reduce((acc, c) => {
      const t = new Uint8Array(acc.length + c.length);
      t.set(acc); t.set(c, acc.length); return t;
    }, new Uint8Array(0));
    const raw = new TextDecoder().decode(rawBytes);
    const headerEnd = raw.indexOf("\r\n\r\n");
    if (headerEnd < 0) return new Response(raw, { status: 502 });
    const headerSection = raw.slice(0, headerEnd);
    let bodySection = raw.slice(headerEnd + 4);
    const lines = headerSection.split("\r\n");
    const statusLine = lines[0];
    const status = parseInt(statusLine.split(" ")[1] || "500");
    const respHeaders = new Headers();
    let isChunked = false;
    for (let i = 1; i < lines.length; i++) {
      const ci = lines[i].indexOf(": ");
      if (ci >= 0) {
        const k = lines[i].slice(0, ci).toLowerCase();
        const v = lines[i].slice(ci + 2);
        respHeaders.set(k, v);
        if (k === "transfer-encoding" && v === "chunked") isChunked = true;
      }
    }
    if (isChunked) {
      let out = "";
      while (bodySection.length > 0) {
        const crlf = bodySection.indexOf("\r\n");
        if (crlf < 0) break;
        const sizeHex = bodySection.slice(0, crlf);
        const size = parseInt(sizeHex, 16);
        if (size === 0) break;
        out += bodySection.slice(crlf + 2, crlf + 2 + size);
        bodySection = bodySection.slice(crlf + 2 + size + 2);
      }
      bodySection = out;
    }
    return new Response(bodySection, { status, headers: respHeaders });
  } finally {
    try { conn.close(); } catch { /* ok */ }
  }
}

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
      return rawHttpFetch(dispPort, url, input instanceof Request ? input : init);
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;

  return () => { globalThis.fetch = realFetch; };
}
