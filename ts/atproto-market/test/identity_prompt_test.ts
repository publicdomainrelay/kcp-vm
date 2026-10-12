import { assert, assertEquals, assertRejects, assertStringIncludes } from "@std/assert";
import { createLogger } from "@publicdomainrelay/logger";
import type { MarketIdentity, MarketIdentitySpec, MarketIdentityStatus } from "@publicdomainrelay/market-identity-abc";
import { findSecretMaterial } from "@publicdomainrelay/identity-prompt-abc";
import type { IdentityAnswers, IdentityPrompt } from "@publicdomainrelay/identity-prompt-abc";
import { createMemoryIdentityPrompt } from "@publicdomainrelay/identity-prompt-memory";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import { createTtyIdentityPrompt } from "@publicdomainrelay/identity-prompt-tty";
import type { TtyIdentityPromptOptions } from "@publicdomainrelay/identity-prompt-tty";
import { createMemoryIdentityVault } from "@publicdomainrelay/market-identity-memory";
import { startMarketIdentityReconciler } from "@publicdomainrelay/market-identity-atproto";
import { createFakeKcp, waitFor } from "./helpers/market_identity_fixtures.ts";

const logger = createLogger({ serviceName: "identity-prompt-test" });
const JWT = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJkaWQ6cGxjOmEifQ.c2lnbmF0dXJl";

function identity(spec: MarketIdentitySpec): MarketIdentity {
  return {
    apiVersion: "socialweb.computer/v1alpha1",
    kind: "MarketIdentity",
    metadata: { name: "self" },
    spec: { ...spec, keysRef: { mount: "app", path: "identity/self/keys" }, sessionRef: { mount: "app", path: "identity/self/session" } },
  };
}

function withNoExit<T>(fn: () => Promise<T>): Promise<T> {
  const realExit = Deno.exit;
  (Deno as { exit: unknown }).exit = (code?: number) => {
    throw new Error(`Deno.exit(${code}) called; a prompt never exits the process`);
  };
  return fn().finally(() => {
    (Deno as { exit: unknown }).exit = realExit;
  });
}

interface Terminal {
  text(): string;
  qrs: string[];
  questions: string[];
  opened: string[];
}

function tty(spec: MarketIdentitySpec, answers: Array<string | null>, extra: Partial<TtyIdentityPromptOptions> = {}) {
  const term: Terminal & { out: string[] } = { out: [], qrs: [], questions: [], opened: [], text: () => term.out.join("") };
  const prompt = createTtyIdentityPrompt({
    identity: identity(spec),
    logger,
    write: (t) => term.out.push(t),
    renderQr: (url) => term.qrs.push(url),
    ask: (q) => {
      term.questions.push(q);
      return answers.length ? answers.shift()! : null;
    },
    openUrl: (url) => term.opened.push(url),
    ...extra,
  });
  return { prompt, term };
}

Deno.test("tty: scanQr draws the QR and its url on the terminal, once per action", () =>
  withNoExit(async () => {
    const { prompt, term } = tty({ mode: "oauthQr" }, []);
    const status: MarketIdentityStatus = { phase: "AwaitingScan", pendingAction: { kind: "scanQr", url: "https://qr.example/#oauth=did%3Aplc%3Aa&n=1" } };
    await prompt.writeStatus("self", status);
    await prompt.writeStatus("self", { ...status, conditions: [] });
    assertEquals(term.qrs, ["https://qr.example/#oauth=did%3Aplc%3Aa&n=1"]);
    assertStringIncludes(term.text(), "https://qr.example/#oauth=did%3Aplc%3Aa&n=1");
    assertStringIncludes(term.text(), "Scan this QR code");
    assertEquals((await prompt.get("self"))!.status, { ...status, conditions: [] });
    prompt.stop();
  }));

Deno.test("tty: signInAgain on SessionExpired draws the QR again and the process stays up", () =>
  withNoExit(async () => {
    const { prompt, term } = tty({ mode: "oauthQr" }, []);
    await prompt.writeStatus("self", { phase: "AwaitingScan", pendingAction: { kind: "scanQr", url: "https://qr.example/#oauth=x&n=1" } });
    await prompt.writeStatus("self", { phase: "Ready", did: "did:plc:alice" });
    await prompt.writeStatus("self", { phase: "SessionExpired", pendingAction: { kind: "signInAgain", url: "https://qr.example/#oauth=x&n=2" } });
    assertEquals(term.qrs, ["https://qr.example/#oauth=x&n=1", "https://qr.example/#oauth=x&n=2"]);
    assertStringIncludes(term.text(), "expired");
    prompt.stop();
  }));

Deno.test("tty: approveAssociation asks on the terminal and a yes is an approval in the spec the reconciler reads", () =>
  withNoExit(async () => {
    const { prompt, term } = tty({ mode: "localRepo" }, ["y"]);
    const before = (await prompt.get("self"))!.metadata!.generation as number;
    await prompt.writeStatus("self", { phase: "AwaitingApproval", pendingAction: { kind: "approveAssociation", callerDid: "did:plc:carol", handle: "carol.test" } });
    await waitFor("the spec approval", async () => (await prompt.get("self"))!.spec!.approvedAssociations?.includes("did:plc:carol"));
    assertEquals(term.questions, ["Associate with carol.test? [y/N] "]);
    const after = (await prompt.get("self"))!;
    assertEquals(after.metadata!.generation, before + 1);
    assertEquals(after.spec!.rejectedAssociations, undefined);
    prompt.stop();
  }));

Deno.test("tty: no answer (stdin closed) is a rejection in the spec, never an exit", () =>
  withNoExit(async () => {
    const { prompt } = tty({ mode: "localRepo" }, [null]);
    await prompt.writeStatus("self", { phase: "AwaitingApproval", pendingAction: { kind: "approveAssociation", callerDid: "did:plc:mallory" } });
    await waitFor("the spec rejection", async () => (await prompt.get("self"))!.spec!.rejectedAssociations?.includes("did:plc:mallory"));
    assertEquals((await prompt.get("self"))!.spec!.approvedAssociations, undefined);
    prompt.stop();
  }));

Deno.test("tty: visitUrl opens the browser and hands the loopback callback to the vault side, never to the object", () =>
  withNoExit(async () => {
    const delivered: Array<Record<string, string>> = [];
    const callback = Promise.withResolvers<Record<string, string>>();
    let shutdowns = 0;
    const { prompt, term } = tty({ mode: "oauthBrowser", handle: "dana.test" }, [], {
      awaitCallback: () => ({ promise: callback.promise, shutdown: () => shutdowns++ }),
      deliverCallback: (params) => {
        delivered.push(params);
        return Promise.resolve();
      },
    });
    const url = "https://pds.example/oauth/authorize?request_uri=urn%3Areq";
    await prompt.writeStatus("self", { phase: "AwaitingAuthorization", pendingAction: { kind: "visitUrl", url } });
    assertEquals(term.opened, [url]);
    assertStringIncludes(term.text(), url);
    callback.resolve({ code: "authorization-code-1234", state: "st", iss: "https://pds.example" });
    await waitFor("the callback delivered", () => delivered.length === 1);
    assertEquals(delivered[0].code, "authorization-code-1234");
    assertEquals(shutdowns, 1);
    const obj = await prompt.get("self");
    assertEquals(findSecretMaterial(obj), [], "the callback never reaches the object");
    assert(!JSON.stringify(obj).includes("authorization-code-1234"));
    prompt.stop();
  }));

Deno.test("memory: an answer is a spec change with a new generation", async () => {
  const prompt = createMemoryIdentityPrompt([identity({ mode: "localRepo" })]);
  await prompt.answerAssociation("self", "did:plc:carol", "approve");
  await prompt.answerAssociation("self", "did:plc:mallory", "reject");
  await prompt.answerAssociation("self", "did:plc:carol", "reject");
  const obj = (await prompt.get("self"))!;
  assertEquals(obj.metadata!.generation, 4);
  assertEquals(obj.spec!.approvedAssociations, undefined);
  assertEquals(obj.spec!.rejectedAssociations, ["did:plc:mallory", "did:plc:carol"]);
  assertEquals(prompt.specWrites.length, 3);
});

Deno.test("kcp: an answer is a merge patch of the association lists only, retried on a stale resourceVersion", async () => {
  const kcp = createFakeKcp();
  kcp.put(identity({ mode: "localRepo", handle: "bob.test" }));
  let raced = false;
  const fetchFn = ((input: string | URL | Request, init?: RequestInit) => {
    const req = new Request(input, init);
    if (!raced && req.method === "PATCH" && req.headers.get("content-type") === "application/merge-patch+json") {
      raced = true;
      kcp.bumpResourceVersion("self");
    }
    return kcp.fetch(req);
  }) as typeof fetch;
  const prompt = createKcpIdentityPrompt({ server: kcp.server, workspace: kcp.workspace, fetch: fetchFn });
  await prompt.answerAssociation("self", "did:plc:carol", "approve");
  const obj = kcp.read("self")!;
  assertEquals(obj.spec!.approvedAssociations, ["did:plc:carol"]);
  assertEquals(obj.spec!.handle, "bob.test");
  assertEquals(obj.metadata!.generation, 2);
  const patches = kcp.writes.filter((w) => w.method === "PATCH" && !w.path.endsWith("/status"));
  assertEquals(patches.length, 2, "the conflicted patch is retried once");
  for (const p of patches) assertEquals(Object.keys(JSON.parse(p.body).spec).sort(), ["approvedAssociations", "rejectedAssociations"]);
});

Deno.test("no MarketIdentity write carries a key, token, DPoP key or code: every prompt refuses one in status and spec", async () => {
  const kcp = createFakeKcp();
  kcp.put(identity({ mode: "oauthQr" }));
  const prompts: Array<[string, IdentityPrompt & IdentityAnswers]> = [
    ["kcp", createKcpIdentityPrompt({ server: kcp.server, workspace: kcp.workspace, fetch: kcp.fetch })],
    ["memory", createMemoryIdentityPrompt([identity({ mode: "oauthQr" })])],
    ["tty", tty({ mode: "oauthQr" }, []).prompt],
  ];
  const bad: MarketIdentityStatus[] = [
    { phase: "Ready", message: JWT },
    { phase: "Ready", handle: "a", accessJwt: "x" } as MarketIdentityStatus,
    { phase: "Ready", pendingAction: { kind: "visitUrl", url: "https://cb.example/?code=abc&state=s" } },
    { phase: "Ready", message: "a".repeat(64) },
    { phase: "Ready", pendingAction: { kind: "scanQr", url: "https://qr.example/#oauth=x&n=1", dpopPrivateJwk: { kty: "EC", d: "x" } } as never },
  ];
  for (const [label, prompt] of prompts) {
    for (const status of bad) await assertRejects(() => prompt.writeStatus("self", status), Error, "secret", `${label} took ${JSON.stringify(status)}`);
    await assertRejects(() => prompt.answerAssociation("self", JWT, "approve"), Error, "secret", `${label} took a token as an association`);
  }
  assertEquals(kcp.writes.length, 0, "a refused write never reaches kcp");
  for (const [, prompt] of prompts) (prompt as { stop?: () => void }).stop?.();
});

Deno.test("localRepo through the tty prompt: a yes on the terminal completes the association, a no fails it with the process up", () =>
  withNoExit(async () => {
    for (const [answer, phase] of [["y", "Ready"], ["n", "Failed"]] as const) {
      const called = Promise.withResolvers<string>();
      let approved = false;
      let rejected = false;
      const { prompt, term } = tty({ mode: "localRepo" }, [answer]);
      const rec = startMarketIdentityReconciler({
        name: "self",
        prompt,
        vault: createMemoryIdentityVault(),
        logger,
        plcDirectoryUrl: "http://127.0.0.1:9",
        qrOrigin: "https://qr.example",
        vaultMount: "app",
        watchIntervalMs: 20,
        localRepo: {
          qrParam: "bdr",
          open: () => Promise.resolve({
            did: "did:plc:repo",
            associateCalled: called.promise,
            approveAssociation: () => {
              approved = true;
            },
            rejectAssociation: () => {
              rejected = true;
            },
            associations: () => Promise.resolve(approved ? ["did:plc:carol"] : []),
          }),
        },
      });
      try {
        await waitFor("the association QR", () => term.qrs.length === 1);
        assertEquals(term.qrs[0], "https://qr.example/#bdr=did:plc:repo");
        called.resolve("did:plc:carol");
        await waitFor(phase, async () => (await prompt.get("self"))?.status?.phase === phase);
        assertEquals(term.questions, ["Associate with did:plc:carol? [y/N] "]);
        assertEquals(approved, answer === "y");
        assertEquals(rejected, answer === "n");
      } finally {
        await rec.stop();
        prompt.stop();
      }
    }
  }));
