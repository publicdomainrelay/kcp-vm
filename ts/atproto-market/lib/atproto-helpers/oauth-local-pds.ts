import type { Secp256k1Keypair } from "@atproto/crypto";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage } from "@publicdomainrelay/atproto-repo-deno";
import { createGenesisOp, createTombstoneOp, PlcClient } from "@publicdomainrelay/did-plc";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";

export interface OAuthLocalPdsOpts {
  logger: StructuredLoggerInterface;
  keypair: Secp256k1Keypair;
  serve: ServeHandle;
  plcDirectoryUrl: string;
  ingressProxyHost: string;
  tls?: boolean;
}

export interface OAuthLocalPds {
  did: string;
  issuer: string;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  relay: IngressRef;
  approve(authorizationUrl: string): Promise<string>;
  beginServe(): Promise<void>;
  tombstone(): Promise<void>;
  stop(): void;
}

export const OAUTH_LOCAL_PDS_CLOSED_PATHS = [
  "/xrpc/com.atproto.server.createAccount",
  "/xrpc/com.atproto.server.createSession",
  "/xrpc/com.atproto.server.refreshSession",
  "/xrpc/com.atproto.server.createInviteCode",
  "/xrpc/com.atproto.server.createInviteCodes",
  "/xrpc/com.atproto.admin.getInviteCodes",
];

export async function tombstonePlcDid(opts: { plcDirectoryUrl: string; did: string; keypair: Secp256k1Keypair }): Promise<void> {
  const plc = new PlcClient({ baseUrl: opts.plcDirectoryUrl });
  const last = (await plc.getAuditLog(opts.did)).filter((e) => !e.nullified).at(-1);
  if (!last) throw new Error(`${opts.did} has no operation to tombstone`);
  if (last.operation.type === "plc_tombstone") return;
  await plc.submitOp(opts.did, await createTombstoneOp({ prev: last.cid, sign: (b) => opts.keypair.sign(b) }));
}

export async function createOAuthLocalPds(opts: OAuthLocalPdsOpts): Promise<OAuthLocalPds> {
  const { logger, keypair, serve } = opts;
  const sub = keypair.did().replace(/:/g, "-").toLowerCase();
  const issuer = `https://${sub}.${opts.ingressProxyHost.replace(/:\d+$/, "")}`;
  const { did, op } = await createGenesisOp({
    rotationKeys: [keypair.did()],
    verificationMethods: { atproto: keypair.did() },
    services: { atproto_pds: { type: "AtprotoPersonalDataServer", endpoint: issuer } },
    sign: (b) => keypair.sign(b),
  });
  await new PlcClient({ baseUrl: opts.plcDirectoryUrl }).submitOp(did, op);
  logger.info("oauth_local_pds_plc_registered", { did, issuer });

  const signer = { did: () => did, sign: (b: Uint8Array) => keypair.sign(b) };
  const repo = createRepoFactory({
    storage: new MemoryStorage(),
    signer,
    did: did as never,
    publicKeyDid: keypair.did(),
    oauthServer: { enabled: true, issuer, publicIssuer: issuer },
  });
  await repo.api.applyWrites(did as never, []);

  for (const path of OAUTH_LOCAL_PDS_CLOSED_PATHS) serve.app.all(path, (c) => c.json({ error: "MethodNotImplemented" }, 404));
  serve.app.use("/xrpc/com.atproto.server.getServiceAuth", async (c: { req: { header(n: string): string | undefined }; json(o: unknown, s: number): Response }, next: () => Promise<void>) => {
    if (!(c.req.header("authorization") ?? "").startsWith("DPoP ")) return c.json({ error: "AuthenticationRequired", message: "an OAuth session of this PDS's account is required" }, 401);
    await next();
  });
  serve.app.route("/", repo.app as never);

  const relay = createIngress({ logger, ingressProxyHost: opts.ingressProxyHost, tls: opts.tls, signer, keypair, label: "oauth-local-pds" });
  serve.addRelay(relay);

  return {
    did,
    issuer,
    signer,
    relay,
    async approve(authorizationUrl: string): Promise<string> {
      const u = new URL(authorizationUrl);
      const requestUri = u.searchParams.get("request_uri") ?? "";
      const clientId = u.searchParams.get("client_id") ?? "";
      if (`${u.origin}` !== issuer || !requestUri || !clientId) {
        throw new Error(`the authorization URL ${u.origin}${u.pathname} is not a PAR request of ${issuer}`);
      }
      const session = await repo.sessionInjector!.injectSession({ userDid: did, handle: did });
      const res = await repo.app.fetch(new Request(`${issuer}/oauth/authorize`, {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded", authorization: `DPoP ${session.sessionData.accessJwt}` },
        body: new URLSearchParams({ request_uri: requestUri, client_id: clientId }).toString(),
      }));
      const out = await res.json().catch(() => ({})) as { redirect_uri?: string; error?: string; error_description?: string };
      if (!res.ok || !out.redirect_uri) throw new Error(`authorize answered ${res.status}: ${out.error_description ?? out.error ?? ""}`);
      return out.redirect_uri;
    },
    beginServe: () => serve.beginServe(),
    tombstone: () => tombstonePlcDid({ plcDirectoryUrl: opts.plcDirectoryUrl, did, keypair }),
    stop: () => serve.shutdown(),
  };
}
