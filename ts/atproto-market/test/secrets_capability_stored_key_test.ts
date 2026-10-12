import { assert, assertEquals, assertNotEquals } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createSecretsCapability } from "@publicdomainrelay/guest-capability-secrets";
import { SECRETS_ROUTE } from "@publicdomainrelay/secrets-common";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { PrepareContext } from "@publicdomainrelay/guest-capability-abc";
import { installFetchInterceptor } from "./fetch-interceptor.ts";

const quiet = {
  debug: () => {},
  info: () => {},
  warn: () => {},
  error: () => {},
} as unknown as StructuredLoggerInterface;

function hexOf(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

async function localDispatcher() {
  const ctl = new AbortController();
  const port = Promise.withResolvers<number>();
  Deno.serve(
    {
      port: 0,
      hostname: "127.0.0.1",
      signal: ctl.signal,
      onListen: (addr) => port.resolve((addr as Deno.NetAddr).port),
    },
    createRelayFactory({ hostname: "localhost" }).createApp().fetch,
  );
  const dispPort = await port.promise;
  const restore = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl: "http://127.0.0.1:1",
    dispPort,
  });
  return {
    ingressProxyHost: `localhost:${dispPort}`,
    close() {
      restore();
      ctl.abort();
    },
  };
}

async function prepared(ingressProxyHost: string, privateKeyHex?: string) {
  const signer = await Secp256k1Keypair.create({ exportable: true });
  const capability = createSecretsCapability({
    secrets: [{ path: "/etc/x", value: "y" }],
    logger: quiet,
    ...(privateKeyHex ? { privateKeyHex } : {}),
  });
  const ctx: PrepareContext = {
    vmName: "node1",
    requesterDid: "did:plc:requester",
    ingressProxyHost,
    signer,
    log: () => {},
  };
  const out = await capability.prepare!(ctx);
  return { capability, secretsUrl: String(out.ctx?.secretsUrl ?? "") };
}

Deno.test({
  name: "a secrets server built from a stored key answers on the same ingress subdomain",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const dispatcher = await localDispatcher();
  try {
    const stored = await Secp256k1Keypair.create({ exportable: true });
    const storedHex = hexOf(await stored.export());

    const first = await prepared(dispatcher.ingressProxyHost, storedHex);
    assert(first.secretsUrl.startsWith("https://"), `no secrets URL: ${first.secretsUrl}`);
    const firstAnswer = await fetch(`${first.secretsUrl}${SECRETS_ROUTE}`);
    assertEquals(firstAnswer.status, 401, "the first server answers through its subdomain");
    await firstAnswer.body?.cancel();
    await first.capability.dispose!();

    const again = await prepared(dispatcher.ingressProxyHost, storedHex);
    assertEquals(again.secretsUrl, first.secretsUrl, "the same stored key serves the same URL");
    const subdomain = new URL(again.secretsUrl).hostname.split(".")[0];
    assertEquals(subdomain, stored.did().replaceAll(":", "-").toLowerCase());
    const againAnswer = await fetch(`${again.secretsUrl}${SECRETS_ROUTE}`);
    assertEquals(againAnswer.status, 401, "the rebuilt server answers on that subdomain");
    await againAnswer.body?.cancel();

    const fresh = await prepared(dispatcher.ingressProxyHost);
    assertNotEquals(fresh.secretsUrl, first.secretsUrl, "without a stored key each server gets its own key");
    await again.capability.dispose!();
    await fresh.capability.dispose!();
  } finally {
    dispatcher.close();
  }
});
