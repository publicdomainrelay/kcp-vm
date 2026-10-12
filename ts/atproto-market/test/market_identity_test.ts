import { assert, assertEquals, assertRejects, assertStringIncludes } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createLocalPDSAgent, createOAuthAgentFromSession } from "@publicdomainrelay/atproto-helpers";
import type { LocalPDSAgent, OAuthSessionData } from "@publicdomainrelay/atproto-helpers";
import { createRequesterPDS } from "@publicdomainrelay/requester-xrpc";
import { BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";
import type { IdentityVault, MarketIdentity, MarketIdentitySpec, VaultRef } from "@publicdomainrelay/market-identity-abc";
import { findSecretMaterial } from "@publicdomainrelay/market-identity-abc";
import { createMemoryIdentityVault } from "@publicdomainrelay/market-identity-memory";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import {
  bidderAssociatingRepo,
  requesterAssociatingRepo,
  startMarketIdentityReconciler,
} from "@publicdomainrelay/market-identity-atproto";
import type { MarketIdentityReconcilerOptions } from "@publicdomainrelay/market-identity-atproto";
import {
  completeOAuthQrWithHonoPds,
  createAssociationCaller,
  createFakeKcp,
  type FakeKcp,
  startFakeQrRelay,
  startOAuthPds,
  startPlcDirectory,
  waitFor,
} from "./helpers/market_identity_fixtures.ts";

const logger = createLogger({ serviceName: "market-identity-test" });
const SESSION_REF: VaultRef = { mount: "app", path: "identity/self/session" };
const KEYS_REF: VaultRef = { mount: "app", path: "identity/self/keys" };

function identity(spec: Omit<MarketIdentitySpec, "keysRef" | "sessionRef">): MarketIdentity {
  return {
    apiVersion: "socialweb.computer/v1alpha1",
    kind: "MarketIdentity",
    metadata: { name: "self" },
    spec: { ...spec, keysRef: KEYS_REF, sessionRef: SESSION_REF },
  };
}

function promptOver(kcp: FakeKcp) {
  return createKcpIdentityPrompt({ server: kcp.server, workspace: kcp.workspace, fetch: kcp.fetch });
}

function assertNoSecretInAnyWrite(kcp: FakeKcp, vault: IdentityVault & { dump(): string }, extra: string[] = []) {
  assert(kcp.writes.length > 0, "the reconciler wrote nothing to kcp");
  const vaultText = vault.dump();
  const secrets = [...extra];
  for (const m of vaultText.matchAll(/"(?:accessJwt|refreshJwt|d|repoKeyHex|password|code)":"([^"]{8,})"/g)) secrets.push(m[1]);
  for (const w of kcp.writes) {
    const parsed = w.body ? JSON.parse(w.body) : null;
    assertEquals(findSecretMaterial(parsed), [], `kcp write ${w.method} ${w.path} carries secret material: ${w.body}`);
    for (const s of secrets) assert(!w.body.includes(s), `kcp write ${w.method} ${w.path} carries a vault secret`);
  }
}

function withNoExit<T>(fn: () => Promise<T>): Promise<T> {
  const realExit = Deno.exit;
  (Deno as { exit: unknown }).exit = (code?: number) => {
    throw new Error(`Deno.exit(${code}) called; an identity reconciler never exits the process`);
  };
  return fn().finally(() => {
    (Deno as { exit: unknown }).exit = realExit;
  });
}

function baseOptions(kcp: FakeKcp, vault: IdentityVault, plcUrl: string, qrOrigin: string): MarketIdentityReconcilerOptions {
  return {
    name: "self",
    prompt: promptOver(kcp),
    vault,
    logger,
    plcDirectoryUrl: plcUrl,
    qrOrigin,
    vaultMount: "app",
    watchIntervalMs: 20,
    qrPollIntervalMs: 20,
    scanTimeoutMs: 60_000,
  };
}

Deno.test("oauthQr: the QR url appears in status.pendingAction and the scan completes through the hono-pds session-minting driver", () =>
  withNoExit(async () => {
    const plc = await startPlcDirectory();
    const qr = await startFakeQrRelay();
    const pds = await startOAuthPds();
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    try {
      const alice = await pds.createAccount("alice.test");
      kcp.put(identity({ mode: "oauthQr", handle: "alice.test" }));
      const rec = startMarketIdentityReconciler(baseOptions(kcp, vault, plc.url, qr.url));
      try {
        const asked = await waitFor("a scanQr pending action", () => {
          const s = kcp.read("self")?.status;
          return s?.phase === "AwaitingScan" && s.pendingAction?.kind === "scanQr" ? s : null;
        });
        assert(asked.pendingAction!.url!.startsWith(`${qr.url}/#oauth=`), asked.pendingAction!.url);
        assert(asked.pendingAction!.expiresAt && !Number.isNaN(Date.parse(asked.pendingAction!.expiresAt)));
        assert(asked.serviceDid?.startsWith("did:plc:"), "the CLI's own did:plc is reported as serviceDid");

        const minted = await completeOAuthQrWithHonoPds({ action: asked.pendingAction, pds, account: alice });
        const ready = await rec.ready();
        assertEquals(ready.did, alice.did);
        const status = await waitFor("Ready", () => kcp.read("self")?.status?.phase === "Ready" ? kcp.read("self")!.status! : null);
        assertEquals(status.did, alice.did);
        assertEquals(status.handle, "alice.test");
        assertEquals(status.pdsUrl, pds.url);
        assertEquals(status.pendingAction, undefined);
        assertEquals(status.sessionRef, SESSION_REF);
        assert(status.conditions?.some((c) => c.type === "SignedIn" && c.status === "True"));
        const stored = await vault.read(SESSION_REF) as unknown as OAuthSessionData;
        assertEquals(stored.userDid, alice.did);
        assert((await vault.read(KEYS_REF))?.repoKeyHex, "the CLI key lives in the vault");
        const listed = await rec.agent()!.listRecords(alice.did, BADGE_BLUE_KEYS_NSID, { limit: 1 });
        assert(listed && "records" in listed, "the signed-in agent reaches the user's PDS");
        assertNoSecretInAnyWrite(kcp, vault, [minted.accessJwt, minted.refreshJwt, minted.dpopPrivateJwk.d]);
      } finally {
        await rec.stop();
      }

      const writesBefore = kcp.writes.length;
      const again = startMarketIdentityReconciler(baseOptions(kcp, vault, plc.url, qr.url));
      try {
        const ready = await again.ready();
        assertEquals(ready.did, alice.did);
        const newWrites = kcp.writes.slice(writesBefore).map((w) => JSON.parse(w.body));
        assert(newWrites.every((ops) => ops[0].value.pendingAction === undefined), "a session restored from the vault needs no scan");
      } finally {
        await again.stop();
      }
    } finally {
      await Promise.all([plc.close(), qr.close(), pds.close()]);
    }
  }));

Deno.test("oauthQr: an expired session sets SessionExpired with a sign-in-again action and the process stays up", () =>
  withNoExit(async () => {
    const plc = await startPlcDirectory();
    const qr = await startFakeQrRelay();
    const pds = await startOAuthPds();
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    try {
      const alice = await pds.createAccount("alice.test");
      kcp.put(identity({ mode: "oauthQr" }));
      const rec = startMarketIdentityReconciler(baseOptions(kcp, vault, plc.url, qr.url));
      try {
        const asked = await waitFor("scanQr", () => kcp.read("self")?.status?.pendingAction);
        const minted = await completeOAuthQrWithHonoPds({ action: asked, pds, account: alice });
        await rec.ready();

        const thief = await createOAuthAgentFromSession(structuredClone(minted), { clientId: "http://localhost" });
        await thief.proactiveRefresh();
        thief.dispose();
        const live = rec.agent() as unknown as { proactiveRefresh(): Promise<void> };
        await live.proactiveRefresh().catch(() => {});

        const expired = await waitFor("SessionExpired", () => {
          const s = kcp.read("self")?.status;
          return s?.phase === "SessionExpired" && s.pendingAction?.kind === "signInAgain" ? s : null;
        });
        assert(expired.pendingAction!.url!.startsWith(`${qr.url}/#oauth=`));
        assert(expired.conditions?.some((c) => c.type === "SignedIn" && c.status === "False" && c.reason === "SessionExpired"));
        assertEquals(await vault.read(SESSION_REF), null, "a dead session is removed from the vault");

        const fresh = await completeOAuthQrWithHonoPds({ action: expired.pendingAction, pds, account: alice });
        await waitFor("Ready again", () => kcp.read("self")?.status?.phase === "Ready");
        assertEquals((await rec.ready()).did, alice.did);
        assertNoSecretInAnyWrite(kcp, vault, [minted.accessJwt, minted.refreshJwt, fresh.accessJwt, fresh.refreshJwt]);
      } finally {
        await rec.stop();
      }
    } finally {
      await Promise.all([plc.close(), qr.close(), pds.close()]);
    }
  }));

async function associationScenario(role: "bidder" | "requester") {
  const plc = await startPlcDirectory();
  const kcp = createFakeKcp(`root:${role}`);
  const vault = createMemoryIdentityVault();
  const opened: Array<{ stop(): void; listBadges(): Promise<Array<Record<string, unknown>>>; did: string; fetch(r: Request): Response | Promise<Response> }> = [];
  try {
    kcp.put(identity({ mode: "localRepo" }));
    const caller = await createAssociationCaller(plc.url, "carol.test");
    const rec = startMarketIdentityReconciler({
      ...baseOptions(kcp, vault, plc.url, "https://qr.example"),
      localRepo: {
        qrParam: role === "bidder" ? "bdr" : "plc",
        async open(keyHex: string) {
          const serve = createServe({ logger });
          if (role === "bidder") {
            const agent: LocalPDSAgent = await createLocalPDSAgent({
              logger,
              keypair: await Secp256k1Keypair.import(keyHex, { exportable: true }),
              serve,
              plcDirectoryUrl: plc.url,
              ingressProxyHost: "relay.test",
              associateServiceId: "bidder_associate",
            });
            opened.push({
              did: agent.did,
              stop: () => agent.stop(),
              fetch: (r) => serve.app.fetch(r),
              listBadges: async () => (await agent.repoApi.listRecords(agent.did, BADGE_BLUE_KEYS_NSID, { limit: 100 })).records.map((r) => r.value as Record<string, unknown>),
            });
            return bidderAssociatingRepo(agent, "bidder_associate");
          }
          const pds = await createRequesterPDS({ logger, serve, privateKeyHex: keyHex, plcDirectoryUrl: plc.url, ingressProxyHost: "relay.test", skipIngress: true });
          opened.push({
            did: pds.did,
            stop: () => serve.shutdown(),
            fetch: (r) => serve.app.fetch(r),
            listBadges: async () => (await pds.api.listRecords(pds.did, BADGE_BLUE_KEYS_NSID, { limit: 100 })).records.map((r) => r.value as Record<string, unknown>),
          });
          return requesterAssociatingRepo(pds);
        },
      },
    });
    try {
      const scan = await waitFor("association scanQr", () => {
        const s = kcp.read("self")?.status;
        return s?.phase === "AwaitingScan" && s.pendingAction?.kind === "scanQr" ? s : null;
      });
      const repo = opened[0];
      assertEquals(scan.did, repo.did);
      assertEquals(scan.pendingAction!.url, `https://qr.example/#${role === "bidder" ? "bdr" : "plc"}=${repo.did}`);

      const confirmed = caller.confirm(repo);
      const ask = await waitFor("approveAssociation", () => {
        const s = kcp.read("self")?.status;
        return s?.phase === "AwaitingApproval" && s.pendingAction?.kind === "approveAssociation" ? s : null;
      });
      assertEquals(ask.pendingAction!.callerDid, caller.did);
      assertEquals(ask.pendingAction!.handle, "carol.test");
      assertEquals(await repo.listBadges(), [], "nothing is recorded before the approval");

      kcp.patchSpec("self", (spec) => {
        spec.approvedAssociations = [...(spec.approvedAssociations ?? []), caller.did];
      });
      const res = await confirmed;
      assertEquals(res.status, 200, await res.clone().text());
      await res.body?.cancel();

      const ready = await waitFor("Ready", () => kcp.read("self")?.status?.phase === "Ready" ? kcp.read("self")!.status! : null);
      assertEquals(ready.did, repo.did);
      assertEquals(ready.associations, [caller.did]);
      assert(ready.conditions?.some((c) => c.type === "Associated" && c.status === "True"));
      const badges = await repo.listBadges();
      assertEquals(badges.length, 1);
      const [owner, member] = role === "bidder" ? [badges[0].challenge, badges[0].keyId] : [badges[0].keyId, badges[0].challenge];
      assertEquals(owner, repo.did);
      assertEquals(member, caller.did);
      assertEquals((await rec.ready()).did, repo.did);
      assertNoSecretInAnyWrite(kcp, vault);
    } finally {
      await rec.stop();
    }

    const restarted = startMarketIdentityReconciler({
      ...baseOptions(kcp, vault, plc.url, "https://qr.example"),
      localRepo: {
        qrParam: "bdr",
        open: () => Promise.resolve({ did: opened[0].did, associateCalled: new Promise<string>(() => {}), approveAssociation() {}, rejectAssociation() {}, associations: async () => (await opened[0].listBadges()).length ? ["did:plc:prior"] : [] }),
      },
    });
    try {
      assertEquals((await restarted.ready()).did, opened[0].did);
      const s = kcp.read("self")!.status!;
      assertEquals(s.phase, "Ready");
      assertEquals(s.pendingAction, undefined, "a repo that already holds its association asks for nothing");
    } finally {
      await restarted.stop();
    }
  } finally {
    for (const o of opened) o.stop();
    await plc.close();
  }
}

Deno.test("localRepo: an approval patched into spec completes the association for the bidder", () => withNoExit(() => associationScenario("bidder")));

Deno.test("localRepo: an approval patched into spec completes the association for the requester", () => withNoExit(() => associationScenario("requester")));

Deno.test("localRepo: a rejection patched into spec refuses the caller and the process stays up", () =>
  withNoExit(async () => {
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    kcp.put(identity({ mode: "localRepo" }));
    const called = Promise.withResolvers<string>();
    let rejected = "";
    let approved = false;
    const rec = startMarketIdentityReconciler({
      ...baseOptions(kcp, vault, "http://127.0.0.1:9", "https://qr.example"),
      localRepo: {
        qrParam: "bdr",
        open: () => Promise.resolve({
          did: "did:plc:repo",
          associateCalled: called.promise,
          approveAssociation: () => {
            approved = true;
          },
          rejectAssociation: (err: Error) => {
            rejected = err.message;
          },
          associations: () => Promise.resolve([]),
        }),
      },
    });
    try {
      await waitFor("scanQr", () => kcp.read("self")?.status?.pendingAction?.kind === "scanQr");
      called.resolve("did:plc:mallory");
      await waitFor("approveAssociation", () => kcp.read("self")?.status?.pendingAction?.callerDid === "did:plc:mallory");
      kcp.patchSpec("self", (spec) => {
        spec.rejectedAssociations = ["did:plc:mallory"];
      });
      const failed = await waitFor("Failed", () => kcp.read("self")?.status?.phase === "Failed" ? kcp.read("self")!.status! : null);
      assertStringIncludes(failed.message ?? "", "did:plc:mallory");
      assertStringIncludes(rejected, "did:plc:mallory");
      assert(!approved);
    } finally {
      await rec.stop();
    }
  }));

Deno.test("localRepo: association Skip reaches Ready without asking", () =>
  withNoExit(async () => {
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    kcp.put(identity({ mode: "localRepo", association: "Skip" }));
    const rec = startMarketIdentityReconciler({
      ...baseOptions(kcp, vault, "http://127.0.0.1:9", "https://qr.example"),
      localRepo: {
        qrParam: "bdr",
        open: () => Promise.resolve({ did: "did:plc:repo", associateCalled: new Promise<string>(() => {}), approveAssociation() {}, rejectAssociation() {}, associations: () => Promise.resolve([]) }),
      },
    });
    try {
      assertEquals((await rec.ready()).did, "did:plc:repo");
      assert(kcp.writes.every((w) => !w.body.includes("scanQr")));
      assert((await vault.read(KEYS_REF))?.repoKeyHex, "the repo key is created in the vault once");
    } finally {
      await rec.stop();
    }
  }));

Deno.test("localRepo: the repo key is read back from the vault, so the did survives a restart", () =>
  withNoExit(async () => {
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    kcp.put(identity({ mode: "localRepo", association: "Skip" }));
    const keys: string[] = [];
    const open = (keyHex: string) => {
      keys.push(keyHex);
      return Promise.resolve({ did: `did:plc:${keyHex.slice(0, 8)}`, associateCalled: new Promise<string>(() => {}), approveAssociation() {}, rejectAssociation() {}, associations: () => Promise.resolve([]) });
    };
    for (let i = 0; i < 2; i++) {
      const rec = startMarketIdentityReconciler({ ...baseOptions(kcp, vault, "http://127.0.0.1:9", "https://qr.example"), localRepo: { qrParam: "bdr", open } });
      await rec.ready();
      await rec.stop();
    }
    assertEquals(keys.length, 2);
    assertEquals(keys[0], keys[1]);
    assertEquals(keys[0].length, 64);
  }));

Deno.test("pdsAccount: signs in with the password held in the vault, never in the object", () =>
  withNoExit(async () => {
    const pds = await startOAuthPds();
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    try {
      const bob = await pds.createAccount("bob.test", "bob-secret-password");
      kcp.put(identity({ mode: "pdsAccount", handle: "bob.test", pdsUrl: pds.url }));
      const rec = startMarketIdentityReconciler(baseOptions(kcp, vault, "http://127.0.0.1:9", "https://qr.example"));
      try {
        const failed = await waitFor("Failed without a password", () => kcp.read("self")?.status?.phase === "Failed" ? kcp.read("self")!.status! : null);
        assertStringIncludes(failed.message ?? "", "keysRef");
        await vault.write(KEYS_REF, { password: "bob-secret-password" });
        const ready = await rec.ready();
        assertEquals(ready.did, bob.did);
        await waitFor("Ready", () => kcp.read("self")?.status?.phase === "Ready");
        assertEquals(kcp.read("self")!.status!.did, bob.did);
        assertNoSecretInAnyWrite(kcp, vault, ["bob-secret-password"]);
      } finally {
        await rec.stop();
      }
    } finally {
      await pds.close();
    }
  }));

Deno.test("oauthBrowser: visitUrl surfaces in status and the callback the foreground puts in the vault completes it", () =>
  withNoExit(async () => {
    const kcp = createFakeKcp();
    const vault = createMemoryIdentityVault();
    kcp.put(identity({ mode: "oauthBrowser", handle: "dana.test" }));
    let completedWith: Record<string, string> | null = null;
    const rec = startMarketIdentityReconciler({
      ...baseOptions(kcp, vault, "http://127.0.0.1:9", "https://qr.example"),
      oauthBrowser: {
        open: () => Promise.resolve({
          restore: () => Promise.resolve(false),
          startFlow: () => Promise.resolve("https://pds.example/oauth/authorize?request_uri=urn%3Areq"),
          completeFlow: (params: Record<string, string>) => {
            completedWith = params;
            return Promise.resolve();
          },
          agent: { did: "did:plc:dana" } as never,
        }),
      },
    });
    try {
      const visit = await waitFor("visitUrl", () => {
        const s = kcp.read("self")?.status;
        return s?.phase === "AwaitingAuthorization" && s.pendingAction?.kind === "visitUrl" ? s : null;
      });
      assertEquals(visit.pendingAction!.url, "https://pds.example/oauth/authorize?request_uri=urn%3Areq");
      await vault.write({ mount: SESSION_REF.mount, path: `${SESSION_REF.path}/callback` }, { code: "authorization-code-1234", state: "st", iss: "https://pds.example" });
      assertEquals((await rec.ready()).did, "did:plc:dana");
      assertEquals(completedWith, { code: "authorization-code-1234", state: "st", iss: "https://pds.example" });
      assertEquals(await vault.read({ mount: SESSION_REF.mount, path: `${SESSION_REF.path}/callback` }), null, "the one-time callback is consumed");
      assertNoSecretInAnyWrite(kcp, vault, ["authorization-code-1234"]);
    } finally {
      await rec.stop();
    }
  }));

Deno.test("no MarketIdentity write carries a key, token, DPoP key or code: the kcp port refuses one", async () => {
  const kcp = createFakeKcp();
  kcp.put(identity({ mode: "oauthQr" }));
  const prompt = promptOver(kcp);
  const jwt = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJkaWQ6cGxjOmEifQ.c2lnbmF0dXJl";
  for (
    const status of [
      { phase: "Ready", message: jwt },
      { phase: "Ready", handle: "a", accessJwt: "x" },
      { phase: "Ready", pendingAction: { kind: "visitUrl", url: `https://cb.example/?code=abc&state=s` } },
      { phase: "Ready", message: "a".repeat(64) },
    ]
  ) {
    await assertRejects(() => prompt.writeStatus("self", status as never), Error, "secret");
  }
  assertEquals(kcp.writes.length, 0, "a refused status never reaches kcp");
  assertEquals(findSecretMaterial({ dpopPrivateJwk: { d: "x" } }).length > 0, true);
  assertEquals(findSecretMaterial({ phase: "Ready", did: "did:plc:abc", handle: "alice.test", url: "https://qr.example/#oauth=did%3Aplc%3Aabc&n=0123456789abcdef0123456789abcdef" }), []);
});
