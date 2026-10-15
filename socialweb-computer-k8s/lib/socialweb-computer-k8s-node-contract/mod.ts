import type { ContractState } from "@publicdomainrelay/compute-request-abc";
import type { GrantVars, GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import { createK3sJoinModule, MESHD_ARCHES, type MeshdPin } from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import { NODE_CLAIM_DOC_PATH, nodeAudience, nodeClaimDocument } from "@publicdomainrelay/socialweb-computer-k8s-common";
import type { ComputeContractMeshd, ComputeContractSpec, NodeClaimGrant } from "@publicdomainrelay/socialweb-computer-k8s-api";
import type { ContractProfile } from "@publicdomainrelay/requester-controller-abc";

export const NODE_JOIN_PROFILE = "node-join";

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function meshdPinOfContract(meshd: ComputeContractMeshd): MeshdPin {
  const sha256: Partial<MeshdPin["sha256"]> = {};
  for (const arch of MESHD_ARCHES) {
    const found = meshd.pins.find((p) => p.arch === arch);
    if (!found) throw new Error(`the contract's meshd pin names no ${arch} build`);
    sha256[arch] = found.sha256;
  }
  return { downloadBase: meshd.downloadBase, sha256: sha256 as MeshdPin["sha256"] };
}

export function nodeGrantOf(cluster: string, nodeName: string, vars: Pick<GrantVars, "issuerUri" | "subject">): NodeClaimGrant {
  return {
    issuer: vars.issuerUri,
    subject: vars.subject,
    audience: nodeAudience(cluster, nodeName),
    role: nodeName,
  };
}

export const NODE_RBAC_ROLE_SUFFIX = "-node";

export function nodeRbacRecord(
  record: Record<string, unknown> | undefined,
  vars: GrantVars,
  cluster: string,
  nodeName: string,
): Record<string, unknown> | undefined {
  if (!record) return record;
  const roles = (record.roles ?? {}) as Record<string, unknown>;
  const roleName = `${vars.role}${NODE_RBAC_ROLE_SUFFIX}`;
  return {
    ...record,
    roles: {
      ...roles,
      [roleName]: {
        role_name: roleName,
        definition: { iss: vars.issuerUri, aud: nodeAudience(cluster, nodeName), sub: vars.subject, policies: [] },
      },
    },
  };
}

export function grantingCapability(
  inner: GuestCapability,
  beforeGrant: (vars: GrantVars) => Promise<void>,
  nodeRole?: { cluster: string; nodeName: string },
): GuestCapability {
  return {
    id: inner.id,
    userDataModule: inner.userDataModule,
    prepare: inner.prepare ? (ctx) => inner.prepare!(ctx) : undefined,
    rbacRecord: inner.rbacRecord
      ? (vars) => nodeRole ? nodeRbacRecord(inner.rbacRecord!(vars), vars, nodeRole.cluster, nodeRole.nodeName) : inner.rbacRecord!(vars)
      : undefined,
    async onContract(vars) {
      await beforeGrant(vars);
      await inner.onContract?.(vars);
    },
    onRevoke: inner.onRevoke ? () => inner.onRevoke!() : undefined,
    dispose: inner.dispose ? () => inner.dispose!() : undefined,
  };
}

export function explainEnd(state: ContractState | null, facts: { error?: string; grantFailed?: boolean } = {}): string {
  if (facts.error) return `the contract failed: ${facts.error}`;
  if (!state) return "the contract did not run";
  if (state.outcome === "no_bids") return "no bids arrived for this node";
  if (state.outcome === "policy_rejected") return `the winning bid was rejected by policy${state.error ? `: ${state.error}` : ""}`;
  if (facts.grantFailed) {
    return "the node's workload grant could not be written to its claim, so the guest could never log in to the cluster OpenBao";
  }
  if (state.outcome === "network_timeout") {
    return "a VM was accepted but it never reported that it was on the network before the VM ready timeout";
  }
  if (state.receiptOk) return "a VM was provisioned but it never fetched its join secrets";
  return `provisioning did not complete (${state.outcome ?? state.phase})${state.error ? `: ${state.error}` : ""}`;
}

export function createNodeJoinProfile(): ContractProfile {
  return {
    name: NODE_JOIN_PROFILE,
    appliesTo: (spec: ComputeContractSpec) => spec.nodeJoin !== undefined && !spec.adopt,
    prepare(input) {
      const nodeJoin = input.spec.nodeJoin!;
      const nodeName = input.spec.vmName ?? input.name;
      const joinModule = createK3sJoinModule(nodeJoin.meshd ? { meshd: meshdPinOfContract(nodeJoin.meshd) } : {});
      let grantFailed = false;
      return Promise.resolve({
        userDataModules: ["k3s", joinModule],
        requireSecretsFetched: true,
        abortReason: () => grantFailed ? "the node's workload grant could not be written to its contract" : undefined,
        wrapCapability: (inner) =>
          grantingCapability(inner, async (vars) => {
            const grant = nodeGrantOf(nodeJoin.cluster, nodeName, vars);
            if (!(await input.writeGrant(grant))) {
              grantFailed = true;
              throw new Error("the node's workload grant could not be written to its contract");
            }
            const doc = nodeClaimDocument(nodeJoin.cluster, nodeName, await sha256Hex(vars.issuerUri));
            input.secrets.push({ path: NODE_CLAIM_DOC_PATH, value: `${JSON.stringify(doc)}\n` });
            input.logger.info("node_contract_grant_written", { issuer: grant.issuer, audience: grant.audience, jwtMount: doc.jwtMount });
          }, { cluster: nodeJoin.cluster, nodeName }),
        explainEnd,
      });
    },
  };
}
