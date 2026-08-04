import { assertEquals } from "@std/assert";
import { Hono } from "@hono/hono";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage } from "@publicdomainrelay/atproto-repo-deno";
import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import {
  badgeBlueKeysRkey,
  createBadgeBlueKeysRecord,
  createDidKeyResolver,
  keyBoundByBadgeBlueKey,
  loadOrGenerateKeypair,
} from "@publicdomainrelay/market-atproto";

const DID = "did:plc:badgebluekeys0";

function serveOnPort0(fetch: (req: Request) => Response | Promise<Response>): Promise<{ port: number; stop(): Promise<void> }> {
  const { promise, resolve } = Promise.withResolvers<number>();
  const server = Deno.serve(
    { port: 0, hostname: "127.0.0.1", onListen: (a) => resolve((a as Deno.NetAddr).port) },
    fetch,
  );
  return promise.then((port) => ({
    port,
    stop: async () => { try { await server.shutdown(); } catch { /* closed */ } },
  }));
}

/** Fake PLC serving {@link DID}'s doc with ONLY the #atproto key + a PDS endpoint. */
function createFakePlc(pdsEndpoint: string, atprotoKey: string) {
  const app = new Hono();
  app.get("/:did", (c) => {
    if (c.req.param("did") !== DID) return c.json({ message: "not found" }, 404);
    return c.json({
      "@context": ["https://www.w3.org/ns/did/v1", "https://w3id.org/security/multikey/v1"],
      id: DID,
      verificationMethod: [{
        id: `${DID}#atproto`, type: "Multikey", controller: DID,
        publicKeyMultibase: atprotoKey.replace(/^did:key:/, ""),
      }],
      service: [{ id: "#atproto_pds", type: "AtprotoPersonalDataServer", serviceEndpoint: pdsEndpoint }],
    });
  });
  return app;
}

Deno.test("badgeBlueKeys rkey is deterministic and the binding getRecord finds the minted record", async () => {
  const repoKp = await Secp256k1Keypair.create({ exportable: true });
  // A badge key SEPARATE from the account's atproto key — NOT published in the DID doc.
  const badgeSigner = await loadOrGenerateKeypair(undefined as string | undefined);

  // One repo factory instance: writes via api, serves getRecord via app.
  const repo = createRepoFactory({
    storage: new MemoryStorage(),
    signer: { did: () => DID, sign: (b) => repoKp.sign(b) },
    did: DID,
  });

  const { port: pdsPort, stop: stopPds } = await serveOnPort0(repo.app.fetch);
  const plcApp = createFakePlc(`http://127.0.0.1:${pdsPort}`, repoKp.did());
  const { port: plcPort, stop: stopPlc } = await serveOnPort0(plcApp.fetch);

  try {
    // Mint the badgeBlueKeys record at the deterministic rkey in the account repo.
    const ref = await createBadgeBlueKeysRecord({
      did: DID,
      keyId: badgeSigner.did(),
      service: "bidder_associate",
      writeRecord: async (did, collection, rkey, record) => {
        await repo.api.applyWrites(did, [{ action: "create", collection, rkey, record }]);
        return { uri: `at://${did}/${collection}/${rkey}`, cid: "bafyreiboguscid000000000000000000000000000000000000000000" };
      },
    });
    // Written rkey must equal the deterministic derivation.
    assertEquals(ref.uri.split("/").pop(), await badgeBlueKeysRkey(DID, badgeSigner.did()));

    const idResolver = new IdResolver({ plcUrl: `http://127.0.0.1:${plcPort}` });

    // Badge key is NOT in the DID doc (only #atproto).
    const docKeys = await createDidKeyResolver()(DID);
    assertEquals(docKeys.includes(badgeSigner.did()), false, "badge key not in DID doc");

    // ...but the badgeBlueKeys binding finds it via the deterministic getRecord.
    const bound = await keyBoundByBadgeBlueKey(idResolver, DID, badgeSigner.did());
    assertEquals(bound, true, "badge.blue key bound via badgeBlueKeys record");
  } finally {
    await stopPds();
    await stopPlc();
  }
});
