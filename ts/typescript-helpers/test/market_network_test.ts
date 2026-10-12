import { assertEquals } from "@std/assert";
import {
  isLocalMarketNetwork,
  marketNetwork,
  PUBLIC_INGRESS_PROXY_HOST,
  PUBLIC_PLC_DIRECTORY_URL,
  rewriteMarketUrl,
} from "@publicdomainrelay/market-network-common";
import type { MarketNetworkInput } from "@publicdomainrelay/market-network-common";
import { createMarketNetworkFetch, installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";

const PLC = "https://127.0.0.1:41001";
const DISPATCHER_PORT = 41002;
const INGRESS = `relay.localhost:${DISPATCHER_PORT}`;
const DID = "did:plc:abcdefghijklmnopqrstuvwx";

interface CallSite {
  name: string;
  input: MarketNetworkInput;
}

const callSites: CallSite[] = [
  {
    name: "hono-bidder",
    input: {
      plcDirectoryUrl: PLC,
      ingressProxyHost: INGRESS,
      relayUrls: "https://relay.default.relay.svc.kcp.local",
      firehoseUrls: "",
      guestTlsUrl: "https://relay.localhost:41003",
    },
  },
  {
    name: "request-vm-ssh",
    input: {
      plcDirectoryUrl: PLC,
      ingressProxyHost: INGRESS,
      relayUrls: "",
      firehoseUrls: "wss://a.localhost/xrpc/com.atproto.sync.subscribeRepos, wss://b.localhost/x",
    },
  },
  {
    name: "market-requester",
    input: {
      plcDirectoryUrl: PLC,
      ingressProxyHost: INGRESS,
      caBundleRef: "/var/tmp/market-network/guest-tls/ca.crt",
    },
  },
  {
    name: "test/fetch-interceptor",
    input: {
      plcDirectoryUrl: PLC,
      ingressProxyHost: `localhost:${DISPATCHER_PORT}`,
    },
  },
];

const rewrites: Array<[string, string]> = [
  ["https://x.relay.localhost/xrpc/com.example.ping?a=1", `http://x.relay.localhost:${DISPATCHER_PORT}/xrpc/com.example.ping?a=1`],
  ["https://x.relay.localhost", `http://x.relay.localhost:${DISPATCHER_PORT}`],
  ["https://x.relay.localhost:5555/p", "http://x.relay.localhost:5555/p"],
  ["https://localhost/p", `http://localhost:${DISPATCHER_PORT}/p`],
  [`https://plc.directory/${DID}`, `${PLC}/${DID}`],
  [`https://plc.directory/${DID}/log/audit`, `${PLC}/${DID}/log/audit`],
  ["https://bsky.social/xrpc/x", "https://bsky.social/xrpc/x"],
  ["http://x.relay.localhost:9/p", "http://x.relay.localhost:9/p"],
];

function recordingFetch(seen: string[]): typeof fetch {
  return ((input: string | URL | Request, _init?: RequestInit) => {
    seen.push(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    return Promise.resolve(new Response("ok"));
  }) as typeof fetch;
}

Deno.test("market-network: every call site resolves *.localhost and plc.directory identically through the adapter", async () => {
  const resolved = new Map<string, string[]>();
  for (const site of callSites) {
    const seen: string[] = [];
    const adapter = createMarketNetworkFetch({ network: marketNetwork(site.input), realFetch: recordingFetch(seen) });
    for (const [from] of rewrites) await adapter.fetch(from);
    for (const [from] of rewrites) await adapter.fetch(new Request(from));
    adapter.close();
    resolved.set(site.name, seen);
  }
  const expected = [...rewrites.map(([, to]) => to), ...rewrites.map(([, to]) => new URL(to).href)];
  for (const site of callSites) {
    assertEquals(resolved.get(site.name), expected, site.name);
  }
});

Deno.test("market-network: the public network leaves every URL untouched", async () => {
  const seen: string[] = [];
  const adapter = createMarketNetworkFetch({ network: marketNetwork({}), realFetch: recordingFetch(seen) });
  for (const [from] of rewrites) await adapter.fetch(from);
  adapter.close();
  assertEquals(seen, rewrites.map(([from]) => from));
});

Deno.test("market-network: defaults and list splitting", () => {
  const net = marketNetwork({ plcDirectoryUrl: "", ingressProxyHost: "", relayUrls: "a, b,,", firehoseUrls: ["c"] });
  assertEquals(net.plcDirectoryUrl, PUBLIC_PLC_DIRECTORY_URL);
  assertEquals(net.ingressProxyHost, PUBLIC_INGRESS_PROXY_HOST);
  assertEquals(net.relayUrls, ["a", "b"]);
  assertEquals(net.firehoseUrls, ["c"]);
  assertEquals(isLocalMarketNetwork(net), false);
  assertEquals(isLocalMarketNetwork(marketNetwork({ ingressProxyHost: "127.0.0.1:8080" })), true);
  assertEquals(isLocalMarketNetwork(marketNetwork({ ingressProxyHost: INGRESS })), true);
});

Deno.test("market-network: keepHttps and hostPorts", () => {
  const net = marketNetwork({ plcDirectoryUrl: PLC, ingressProxyHost: INGRESS });
  assertEquals(rewriteMarketUrl(net, "https://x.relay.localhost/p", { keepHttps: true }), `https://x.relay.localhost:${DISPATCHER_PORT}/p`);
  assertEquals(rewriteMarketUrl(net, "https://pds.localhost/p", { hostPorts: { "pds.localhost": 7 } }), "http://pds.localhost:7/p");
  assertEquals(rewriteMarketUrl(net, "https://bsky.social/p"), undefined);
});

Deno.test("market-network: install patches globalThis.fetch and restore undoes it", async () => {
  const before = globalThis.fetch;
  const seen: string[] = [];
  const failures: string[] = [];
  const restore = installMarketNetworkFetch({
    network: marketNetwork({ plcDirectoryUrl: PLC, ingressProxyHost: INGRESS }),
    realFetch: ((input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.includes("fail")) return Promise.reject(new TypeError("fetch failed", { cause: "refused" }));
      return recordingFetch(seen)(input, init);
    }) as typeof fetch,
    onFetchError: (f) => failures.push(`${f.url} ${f.cause}`),
  });
  await fetch(`https://plc.directory/${DID}`);
  await fetch("https://fail.localhost/p").catch(() => {});
  restore();
  assertEquals(globalThis.fetch, before);
  assertEquals(seen, [`${PLC}/${DID}`]);
  assertEquals(failures, ["https://fail.localhost/p refused"]);
});
