export const FEDPROXY_RBAC_NSID = "com.fedproxy.rbac";
export const FEDPROXY_SSH_KEY_NSID = "com.fedproxy.sshPublicKey";

export interface SshKeyRbacContext {
  serviceName: string;
  issuerUri: string;
  actx: string;
  requesterDid: string;
  subjectTemplate?: string;
}

export function didPlcKey(did: string): string {
  return did.replace(/^did:plc:/, "");
}

export function renderSubject(
  template: string,
  vars: { actx: string; didPlcKey: string; role: string },
): string {
  return template
    .replace(/\{actx\}/g, vars.actx)
    .replace(/\{did-plc-key\}/g, vars.didPlcKey)
    .replace(/\{role\}/g, vars.role);
}

export function buildSshKeyRbacRecord(
  ctx: SshKeyRbacContext,
): Record<string, unknown> {
  const { serviceName, issuerUri, actx, requesterDid } = ctx;
  const requesterPlcKey = didPlcKey(requesterDid);
  const subjectTemplate = ctx.subjectTemplate ?? "actx:{actx}:plc:{did-plc-key}:role:{role}";
  const sub = renderSubject(subjectTemplate, {
    actx,
    didPlcKey: requesterPlcKey,
    role: serviceName,
  });
  const policyName = `${serviceName}-ssh-key-register`;

  return {
    $type: FEDPROXY_RBAC_NSID,
    roles: {
      [serviceName]: {
        role_name: serviceName,
        definition: {
          aud: `api://ATProto?actx=${requesterDid}`,
          iss: issuerUri,
          sub,
          policies: [policyName],
        },
      },
    },
    policies: {
      [policyName]: {
        meta: { policy: "ssh-key-register" },
        schemas: {
          "/xrpc/com.atproto.repo.createRecord": {
            type: "object",
            $schema: "http://json-schema.org/draft-07/schema#",
            required: ["capability", "body"],
            properties: {
              capability: { enum: ["create"] },
              body: {
                type: "object",
                additionalProperties: false,
                required: ["collection", "record"],
                properties: {
                  collection: { type: "string", const: FEDPROXY_SSH_KEY_NSID },
                  record: {
                    type: "object",
                    properties: { service: { type: "string", const: serviceName } },
                    required: ["service"],
                  },
                },
              },
            },
          },
        },
      },
    },
    custom_claims_roles_index: { job_workflow_ref: {} },
    createdAt: new Date().toISOString(),
  };
}
