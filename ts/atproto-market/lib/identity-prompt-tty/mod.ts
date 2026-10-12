import { qrcode } from "@libs/qrcode";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { startLoopbackCallbackServer } from "@publicdomainrelay/atproto-oauth-helpers";
import type { MarketIdentity, MarketIdentityStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import type { IdentityAnswers, IdentityPrompt } from "@publicdomainrelay/identity-prompt-abc";
import {
  isYes,
  nextGeneration,
  pendingActionKey,
  pendingActionView,
  refuseSecretMaterial,
  specWithAnswer,
} from "@publicdomainrelay/identity-prompt-abc";

export interface LoopbackCallback {
  promise: Promise<Record<string, string>>;
  shutdown(): void;
}

export interface TtyIdentityPromptOptions {
  identity: MarketIdentity;
  logger: StructuredLoggerInterface;
  write?: (text: string) => void;
  renderQr?: (url: string) => void;
  ask?: (question: string) => string | null;
  openUrl?: (url: string) => void;
  callbackPort?: number;
  awaitCallback?: () => LoopbackCallback;
  deliverCallback?: (params: Record<string, string>) => Promise<void>;
}

export interface TtyIdentityPrompt extends IdentityPrompt, IdentityAnswers {
  stop(): void;
}

const RULE = "=".repeat(60);

function defaultWrite(text: string): void {
  Deno.stdout.writeSync(new TextEncoder().encode(text));
}

function defaultRenderQr(url: string): void {
  qrcode(url, { output: "console", ecl: "HIGH" });
}

function defaultAsk(question: string): string | null {
  return globalThis.prompt(question);
}

function defaultOpenUrl(url: string): void {
  const [cmd, args] = Deno.build.os === "darwin"
    ? ["open", [url]]
    : Deno.build.os === "windows"
    ? ["cmd", ["/c", "start", url]]
    : ["xdg-open", [url]];
  try {
    new Deno.Command(cmd, { args, stdin: "null", stdout: "null", stderr: "null" }).spawn();
  } catch {
    return;
  }
}

export function createTtyIdentityPrompt(opts: TtyIdentityPromptOptions): TtyIdentityPrompt {
  const name = opts.identity.metadata?.name;
  if (!name) throw new Error("a MarketIdentity needs metadata.name");
  const write = opts.write ?? defaultWrite;
  const renderQr = opts.renderQr ?? defaultRenderQr;
  const ask = opts.ask ?? defaultAsk;
  const openUrl = opts.openUrl ?? defaultOpenUrl;
  const awaitCallback = opts.awaitCallback ?? (() => startLoopbackCallbackServer(opts.callbackPort ?? 0));
  const { logger } = opts;
  let obj: MarketIdentity = structuredClone({ ...opts.identity, metadata: { ...opts.identity.metadata, generation: opts.identity.metadata?.generation ?? 1 } });
  let lastKey = "";
  let callback: LoopbackCallback | null = null;

  function own(n: string): MarketIdentity {
    if (n !== name) throw new Error(`marketidentity ${n} not found; this terminal answers for ${name}`);
    return obj;
  }

  function answer(callerDid: string, yes: boolean): void {
    const spec = specWithAnswer(obj.spec!, callerDid, yes ? "approve" : "reject");
    refuseSecretMaterial(name!, "spec", spec);
    obj = { ...obj, spec, metadata: { ...obj.metadata, generation: nextGeneration(obj) } };
    logger.info(yes ? "identity_association_approved_on_terminal" : "identity_association_rejected_on_terminal", { name, callerDid });
  }

  function stopCallback(): void {
    callback?.shutdown();
    callback = null;
  }

  function awaitAuthorization(): void {
    stopCallback();
    const cb = awaitCallback();
    callback = cb;
    cb.promise.then(async (params) => {
      if (callback !== cb) return;
      stopCallback();
      if (!opts.deliverCallback) {
        logger.warn("identity_authorization_response_dropped", { name, hint: "no vault side to hand it to" });
        return;
      }
      await opts.deliverCallback(params);
      logger.info("identity_authorization_response_delivered", { name });
    }).catch((err) => logger.error("identity_authorization_response_failed", { name, error: String(err) }));
  }

  function render(status: MarketIdentityStatus): void {
    const view = pendingActionView(status);
    const action = status.pendingAction;
    if (!view || !action) {
      if (status.phase === "Failed" && status.message) write(`\n  Sign-in failed: ${status.message}\n\n`);
      return;
    }
    if (action.kind === "approveAssociation") {
      if (!action.callerDid) return;
      answer(action.callerDid, isYes(ask(view.question!)));
      return;
    }
    write(`\n${RULE}\n  ${view.title}:\n\n`);
    if (action.url && action.kind !== "visitUrl") renderQr(action.url);
    if (action.url) write(`\n  ${action.kind === "visitUrl" ? "URL" : "Or open this URL"}:\n  ${action.url}\n`);
    write(`${RULE}\n\n`);
    if (action.kind === "visitUrl" && action.url) {
      openUrl(action.url);
      awaitAuthorization();
    }
  }

  return {
    get(n) {
      try {
        return Promise.resolve(structuredClone(own(n)));
      } catch {
        return Promise.resolve(null);
      }
    },
    writeStatus(n, status) {
      try {
        own(n);
        refuseSecretMaterial(n, "status", status);
      } catch (err) {
        return Promise.reject(err);
      }
      obj = { ...obj, status: structuredClone(status) };
      const key = pendingActionKey(status);
      if (key === lastKey) return Promise.resolve();
      lastKey = key;
      if (status.pendingAction?.kind !== "visitUrl") stopCallback();
      try {
        render(status);
      } catch (err) {
        return Promise.reject(err);
      }
      return Promise.resolve();
    },
    answerAssociation(n, callerDid, a) {
      try {
        own(n);
        answer(callerDid, a === "approve");
      } catch (err) {
        return Promise.reject(err);
      }
      return Promise.resolve();
    },
    stop() {
      stopCallback();
    },
  };
}
