import { Secp256k1Keypair } from "@atproto/crypto";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createServe } from "@publicdomainrelay/serve";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import {
  buildSecretsRbacRecord,
  DEFAULT_ACCEPT_PATH_VM,
  SECRETS_ROUTE,
} from "@publicdomainrelay/secrets-common";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import { createSecretsAuthorizer } from "@publicdomainrelay/secrets-oidc";
import { createSecretsApp } from "@publicdomainrelay/hono-factory-secrets-oidc";
import type {
  CapabilityPrepared,
  GrantVars,
  GuestCapability,
  PrepareContext,
} from "@publicdomainrelay/guest-capability-abc";

export const SECRETS_CAPABILITY_ID = "secrets";

export interface CreateSecretsCapabilityOpts {
  secrets: SecretEntry[];
  logger: StructuredLoggerInterface;
  route?: string;
  acceptPathVm?: string;
}

/**
 * Serves an operator-supplied secrets bundle from a Hono server that exists only
 * for the life of one compute contract. The server gets its own keypair, so it
 * registers its own dispatcher subdomain rather than sharing the requester's.
 * Nothing is authorized until onContract installs the grant derived from the
 * winning bid, and onRevoke drops it when the VM delete event is sent.
 */
export function createSecretsCapability(
  opts: CreateSecretsCapabilityOpts,
): GuestCapability {
  const route = opts.route ?? SECRETS_ROUTE;
  const acceptPathVm = opts.acceptPathVm ?? DEFAULT_ACCEPT_PATH_VM;
  const logger = opts.logger;
  const authorizer = createSecretsAuthorizer();
  let relay: IngressRef | null = null;
  let serve: ServeHandle | null = null;

  return {
    id: SECRETS_CAPABILITY_ID,
    userDataModule: "secrets",

    async prepare(ctx: PrepareContext): Promise<CapabilityPrepared> {
      const keypair = await Secp256k1Keypair.create({ exportable: true });
      relay = createIngress({
        logger,
        ingressProxyHost: ctx.ingressProxyHost,
        signer: ctx.signer,
        keypair,
        label: "secrets",
        tls: ctx.tls,
      });
      serve = createServe({ logger, relays: [relay] });
      serve.app.route(
        "/",
        createSecretsApp({
          authorizer,
          getSecrets: () => opts.secrets,
          route,
          log: (event, extra) => logger.info(event, extra ?? {}),
        }),
      );
      await serve.beginServe();

      const secretsUrl = relay.ingressUrl;
      ctx.log("secrets_server_ready", {
        secretsUrl,
        route,
        count: opts.secrets.length,
      });

      return {
        ctx: {
          secretsUrl,
          secretsRoute: route,
          secretsAud: `api://ATProto?actx=${ctx.requesterDid}`,
          secretsAcceptPath: acceptPathVm,
        },
      };
    },

    onContract(vars: GrantVars): void {
      authorizer.install({
        rbac: buildSecretsRbacRecord({
          role: vars.role,
          subject: vars.subject,
          issuerUri: vars.issuerUri,
          expectedAud: vars.expectedAud,
          serviceUrl: relay?.ingressUrl ?? "",
          route,
        }),
        issuerUri: vars.issuerUri,
        expectedAud: vars.expectedAud,
      });
      logger.info("secrets_grant_installed", {
        role: vars.role,
        subject: vars.subject,
        issuerUri: vars.issuerUri,
      });
    },

    onRevoke(): void {
      authorizer.revoke();
      logger.info("secrets_grant_revoked", {});
    },

    dispose(): Promise<void> {
      authorizer.revoke();
      serve?.shutdown();
      serve = null;
      relay = null;
      return Promise.resolve();
    },
  };
}
