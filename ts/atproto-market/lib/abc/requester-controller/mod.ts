import type { UserDataModule } from "@publicdomainrelay/cloud-init-common";
import type { ContractState } from "@publicdomainrelay/compute-request-abc";
import type { GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import type { ComputeContractPhase, ComputeContractSpec, NodeClaimGrant } from "@publicdomainrelay/socialweb-computer-k8s-api";

export const DEFAULT_MAX_CONCURRENT = 64;

export const DEFAULT_PER_ACCOUNT = 16;

export const FINISHED_PHASES: ReadonlySet<ComputeContractPhase> = new Set(["Released", "Failed"]);

export interface AdmissionLimits {
  maxConcurrent: number;
  perAccount: number;
}

export type AdmissionDecision = { admit: true } | { admit: false; reason: string };

export class AdmissionState {
  readonly #running = new Map<string, string>();

  constructor(readonly limits: AdmissionLimits) {}

  decide(name: string, account: string): AdmissionDecision {
    if (this.#running.has(name)) return { admit: true };
    if (this.#running.size >= this.limits.maxConcurrent) {
      return { admit: false, reason: `waiting for admission: ${this.#running.size} of ${this.limits.maxConcurrent} contracts are running` };
    }
    const mine = [...this.#running.values()].filter((a) => a === account).length;
    if (mine >= this.limits.perAccount) {
      return { admit: false, reason: `waiting for admission: this account has ${mine} of ${this.limits.perAccount} contracts running` };
    }
    return { admit: true };
  }

  admit(name: string, account: string): void {
    this.#running.set(name, account);
  }

  finish(name: string): void {
    this.#running.delete(name);
  }

  has(name: string): boolean {
    return this.#running.has(name);
  }

  get running(): number {
    return this.#running.size;
  }
}

export type ContractDisposition =
  | { kind: "start" }
  | { kind: "resume"; state: ContractState }
  | { kind: "done"; reason: string };

export interface DispositionInput {
  phase: ComputeContractPhase | undefined;
  state: ContractState | null;
  release: boolean;
}

export function dispositionOf(input: DispositionInput): ContractDisposition {
  const { phase, state } = input;
  if (phase === "Released" || state?.phase === "released") return { kind: "done", reason: "the contract was released" };
  if (phase === "Failed" && !state?.receipt) return { kind: "done", reason: "the contract failed before it held a VM" };
  if (state) return { kind: "resume", state };
  if (input.release) return { kind: "done", reason: "the contract was released before it started" };
  return { kind: "start" };
}

export function accountOfSpec(spec: Pick<ComputeContractSpec, "ownerDid">): string {
  return spec.ownerDid ?? "";
}

export interface AccountPdsProvider {
  readonly accounts: boolean;
  pdsFor(ownerDid: string | undefined): Promise<RequesterPDS>;
  did(ownerDid: string | undefined): Promise<string>;
}

export interface ProfileInput {
  name: string;
  spec: ComputeContractSpec;
  secrets: SecretEntry[];
  logger: StructuredLoggerInterface;
  writeGrant(grant: NodeClaimGrant): Promise<boolean>;
}

export interface ProfilePreparation {
  userDataModules: Array<string | UserDataModule>;
  wrapCapability?(capability: GuestCapability): GuestCapability;
  abortReason?(): string | undefined;
  requireSecretsFetched: boolean;
  explainEnd(state: ContractState | null, facts: { error?: string; grantFailed?: boolean }): string;
}

export interface ContractProfile {
  readonly name: string;
  appliesTo(spec: ComputeContractSpec): boolean;
  prepare(input: ProfileInput): Promise<ProfilePreparation>;
}

export interface ContractFacts {
  releaseRequested: boolean;
  deleted: boolean;
}
