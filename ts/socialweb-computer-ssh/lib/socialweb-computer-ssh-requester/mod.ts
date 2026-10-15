import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import {
  DEFAULT_TRANSPORT_MODULE,
  ensureDumbpipe,
  runComputeContract,
} from "@publicdomainrelay/requester-xrpc";
import type { SshSessionProvider } from "@publicdomainrelay/requester-abc";
import type { SessionStore } from "@publicdomainrelay/socialweb-computer-abc";
import type { CommandIo, ComputeCommandRunner } from "@publicdomainrelay/socialweb-computer-abc";
import { createAccountAgents } from "@publicdomainrelay/socialweb-computer-account-agents";
import { createAccountRequesters } from "@publicdomainrelay/socialweb-computer-account-requesters";
import type { GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import type { AuthorizedAccount } from "@publicdomainrelay/socialweb-computer-common";
import { policyFromEnv, renderExecCommand, vmNameFromEnv } from "@publicdomainrelay/socialweb-computer-common";
import { createIrohSessionProvider, dumbpipeIrohDialer, generateGuestKeypair } from "./bridge.ts";
import type { IrohDialer } from "./bridge.ts";

export {
  createIrohSessionProvider,
  dumbpipeIrohDialer,
  generateGuestKeypair,
  NonIrohGuestAddressError,
  TERMINATED_SESSION_EXIT_CODE,
  unixSocketIrohDialer,
} from "./bridge.ts";
export type { GatewayLog, IrohDial, IrohDialer, IrohSessionProviderOptions } from "./bridge.ts";

export function contractUserData(base?: string): { transport: typeof DEFAULT_TRANSPORT_MODULE; base?: string } {
  return base ? { transport: DEFAULT_TRANSPORT_MODULE, base } : { transport: DEFAULT_TRANSPORT_MODULE };
}

export function guestBaseUserData(opts: { caPem?: string; curlHome?: string }): string | undefined {
  const base: Record<string, unknown> = {};
  if (opts.caPem?.trim()) base.ca_certs = { trusted: [opts.caPem.trim()] };
  if (opts.curlHome) base.bootcmd = [["systemctl", "set-environment", `CURL_HOME=${opts.curlHome}`]];
  return Object.keys(base).length > 0 ? "#cloud-config\n" + JSON.stringify(base) : undefined;
}

export interface InProcessRequesterOptions {
  sessionStore: SessionStore;
  requesterKeyHex: () => Promise<string>;
  plcDirectoryUrl?: string;
  ingressProxyHost?: string;
  relayUrls?: string[];
  vmReadyTimeoutSec?: number;
  /**
   * OAuth client_id to refresh as, when the stored session does not carry one.
   *
   * A refresh token is bound to the client that obtained it. The session
   * deposited by the page normally carries its own client_id; this is the
   * fallback for a session that does not, and must match the metadata document
   * this deployment publishes.
   */
  oauthClientId?: string;
  /** /etc/hosts entries the guest needs to reach this dispatcher. */
  guestHostAliases?: string[];
  extraBidderDids?: string[];
  irohDialer?: IrohDialer;
  guestBaseUserData?: string;
  agentWithoutOAuthFor?: (did: string) => Promise<unknown> | null;
  /**
   * Write the com.fedproxy.rbac record that authorizes the guest's ssh host
   * key. Off by default: this app's OAuth scope grants badgeBlueKeys only, so
   * the write would be refused. Turning it on means widening the scope.
   */
  rbac?: boolean;
  /**
   * Builds the guest capabilities for one run, for one account.
   *
   * Called inside run() rather than at construction because a capability's
   * prepare() mints its own keypair and mounts its own serve handle, and the
   * secrets capability holds that at module scope -- so one instance serves
   * exactly one contract, while this requester serves every connection from one
   * process. Returning an empty list runs the contract with no capabilities,
   * which is what an account that never paired should get.
   */
  capabilityFor?: (did: string) => Promise<GuestCapability[]>;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export interface InProcessRequester extends ComputeCommandRunner {
  shutdown(): Promise<void>;
}

const encoder = new TextEncoder();

export interface ContractTermination {
  reason: string;
  exitCode?: number;
  observedAt: string;
}

export function explainTermination(terminated: ContractTermination): string {
  const exit = terminated.exitCode !== undefined ? `, guest exit ${terminated.exitCode}` : "";
  return `The VM was terminated by its bidder (${terminated.reason}${exit}, at ${terminated.observedAt}); the session ended.\n`;
}

/**
 * What to tell a client whose command never ran.
 *
 * A run can stop short in ways that are nobody's error and are invisible from
 * the client -- no bidder online, or a policy no bidder satisfies, both look
 * like the connection simply closing. The reason is on stderr because that is
 * all the client sees besides the exit status.
 */
export function explainNoSession(
  outcome: { event?: string; error?: string; sshReady?: boolean; terminated?: ContractTermination },
  ctx: { policy: string; bidWindowSec: unknown; vmReadyTimeoutSec?: number },
): string {
  const window = typeof ctx.bidWindowSec === "number" ? `${ctx.bidWindowSec}s` : "the bid window";
  const lines: string[] = [];

  if (outcome.terminated) return explainTermination(outcome.terminated);

  if (outcome.event === "no_bids") {
    lines.push(
      `No bids arrived within ${window}, so no VM was provisioned and nothing ran.`,
      "",
      "Worth checking:",
      `  - LC_POLICY_BID_WINDOW_SEC (currently ${ctx.bidWindowSec ?? "default"}) gives bidders more time.`,
      `  - LC_POLICY (currently ${ctx.policy}) decides which bids are acceptable; a policy no`,
      "    bidder satisfies looks exactly like this.",
      "  - whether any bidder is online for this market.",
    );
  } else if (outcome.event === "policy_rejected") {
    lines.push(
      "A bid won, but the fulfillment policy rejected it, so no VM was provisioned.",
    );
    if (outcome.error) lines.push(`  ${outcome.error}`);
    lines.push(
      "",
      `Worth checking LC_POLICY (currently ${ctx.policy}) and LC_POLICY_ARGS.`,
    );
  } else if (outcome.sshReady === false) {
    lines.push(
      `A VM was provisioned but its SSH did not come up within ${ctx.vmReadyTimeoutSec ?? "the"} seconds,`,
      "so nothing ran.",
    );
  } else {
    lines.push(
      `Provisioning did not complete (${outcome.event ?? "unknown event"}).`,
    );
    if (outcome.error) lines.push(`  ${outcome.error}`);
  }

  return lines.join("\n") + "\n";
}

export function createInProcessRequester(opts: InProcessRequesterOptions): InProcessRequester {
  const log = opts.log ?? (() => {});
  const plcUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const logger = {
    debug: () => {},
    info: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    warn: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    error: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
  };
  const agents = createAccountAgents({
    sessionStore: opts.sessionStore,
    oauthClientId: opts.oauthClientId,
    agentWithoutOAuthFor: opts.agentWithoutOAuthFor,
    log,
  });
  const accounts = createAccountRequesters({
    agents,
    requesterKeyHex: opts.requesterKeyHex,
    plcDirectoryUrl: opts.plcDirectoryUrl,
    ingressProxyHost: opts.ingressProxyHost,
    label: "socialweb-computer-ssh",
    log,
  });
  const requesterFor = accounts.requesterFor;

  let dumbpipe: Promise<string> | null = null;
  function dumbpipePath(): Promise<string> {
    if (!dumbpipe) {
      dumbpipe = ensureDumbpipe(logger);
      dumbpipe.catch(() => (dumbpipe = null));
    }
    return dumbpipe;
  }

  async function providerFor(io: CommandIo): Promise<SshSessionProvider> {
    return createIrohSessionProvider({
      io,
      log,
      dial: opts.irohDialer ?? dumbpipeIrohDialer(await dumbpipePath()),
      generateKeypair: generateGuestKeypair,
    });
  }

  return {
    async run(account: AuthorizedAccount, command: string, env: Record<string, string>, io: CommandIo): Promise<void> {
      let eventStreams: { close(): void } | null = null;
      try {
        const { policy, args: policyArgs } = policyFromEnv(env);
        const execCommand = renderExecCommand(command, env);
        const vmName = vmNameFromEnv(env);
        log("requester_start", { did: account.did, policy, commandChars: command.length });

        const pds = await requesterFor(account.did);

        // A capability that cannot be built must not cost the client its session.
        // Whatever it carries is a convenience, and letting a failed read here
        // throw would turn a provisionable run into "provisioning failed" on
        // stderr -- a worse outcome than running without it.
        const capabilities = await opts.capabilityFor?.(account.did)
          .catch((err) => {
            log("capability_build_failed", { did: account.did, error: String(err) });
            return [] as GuestCapability[];
          }) ?? [];

        eventStreams = createDefaultATProtoEventStreamsClient({
          onlyRelays: opts.relayUrls?.length ? opts.relayUrls : undefined,
          log: logger,
        }) as unknown as { close(): void };

        const result = await runComputeContract(pds, {
          plcUrl,
          ingressProxyHost: opts.ingressProxyHost,
          relayUrls: opts.relayUrls,
          eventStreams: eventStreams as never,
          execProgram: execCommand,
          vmName,
          vmReadyTimeoutSec: opts.vmReadyTimeoutSec,
          sshProvider: await providerFor(io),
          policy: { name: policy, args: policyArgs },
          guestHostAliases: opts.guestHostAliases,
          extraBidderDids: opts.extraBidderDids,
          userData: contractUserData(opts.guestBaseUserData),
          rbac: opts.rbac ?? false,
          capabilities,
          logger,
        });

        // Exit status is the guest's, and only exists once a guest ran something.
        // When provisioning stopped short there is no status to forward, and
        // `?? 0` used to report that as success: a client whose run never started
        // got exit 0 and one line saying the connection closed. Anything short of
        // a session is now a failure, with the reason on stderr.
        const outcome = result as {
          sshExitCode?: number;
          event?: string;
          error?: string;
          bids?: number;
          sshReady?: boolean;
          terminated?: ContractTermination;
        };

        if (outcome.terminated) {
          log("requester_terminated", {
            did: account.did,
            reason: outcome.terminated.reason,
            exitCode: outcome.terminated.exitCode,
            sshExitCode: outcome.sshExitCode,
          });
          await io.writeErr(encoder.encode(explainTermination(outcome.terminated)));
          io.exit(typeof outcome.sshExitCode === "number" ? outcome.sshExitCode : 1);
        } else if (typeof outcome.sshExitCode === "number") {
          log("requester_done", { did: account.did, code: outcome.sshExitCode });
          io.exit(outcome.sshExitCode);
        } else {
          const explanation = explainNoSession(outcome, {
            policy,
            bidWindowSec: policyArgs.bidWindowSec,
            vmReadyTimeoutSec: opts.vmReadyTimeoutSec,
          });
          log("requester_no_session", {
            did: account.did,
            event: outcome.event,
            error: outcome.error,
            bids: outcome.bids,
            sshReady: outcome.sshReady,
          });
          await io.writeErr(encoder.encode(explanation));
          io.exit(1);
        }
      } catch (err) {
        log("requester_failed", { did: account.did, error: String(err) });
        await io.writeErr(encoder.encode(`provisioning failed: ${String(err)}\n`));
        io.exit(1);
      } finally {
        eventStreams?.close();
      }
    },

    shutdown(): Promise<void> {
      return agents.shutdown();
    },
  };
}
