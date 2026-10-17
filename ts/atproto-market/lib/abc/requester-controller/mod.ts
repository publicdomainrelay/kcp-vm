import type { UserDataModule } from "@publicdomainrelay/cloud-init-common";
import type { ContractState } from "@publicdomainrelay/compute-request-abc";
import type { GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import type { ComputeContractPhase, ComputeContractSpec, ComputeContractStatus, NodeClaimGrant } from "@publicdomainrelay/socialweb-computer-k8s-api";

export const DEFAULT_MAX_CONCURRENT = 64;

export const DEFAULT_PER_ACCOUNT = 16;

export const FINISHED_PHASES: ReadonlySet<ComputeContractPhase> = new Set(["Released", "Failed"]);

export const CONDITION_AT_CAPACITY = "AtCapacity";

export const CONTRACT_TTL_ANNOTATION = "socialweb.computer/ttl-after-finished";

function conditionType(c: Record<string, unknown>): string {
  return typeof c.type === "string" ? c.type : "";
}

export function withContractConditions(
  status: ComputeContractStatus,
  now: Date,
  atCapacity?: string,
): ComputeContractStatus {
  const kept = (status.conditions ?? []).filter((c) => conditionType(c) !== CONDITION_AT_CAPACITY);
  const next = [...kept];
  if (atCapacity !== undefined) {
    next.push({
      type: CONDITION_AT_CAPACITY,
      status: "True",
      reason: "AdmissionFull",
      message: atCapacity,
      lastTransitionTime: now.toISOString(),
    });
  }
  const finishedType = status.phase === "Failed" ? "Failed" : status.phase === "Released" ? "Complete" : undefined;
  if (finishedType && !next.some((c) => (conditionType(c) === "Complete" || conditionType(c) === "Failed") && c.status === "True")) {
    next.push({
      type: finishedType,
      status: "True",
      reason: status.phase,
      message: status.message ?? "",
      lastTransitionTime: now.toISOString(),
    });
  }
  return { ...status, conditions: next };
}

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

export const DEFAULT_RELEASE_RETRY_BOUND_MS = 24 * 60 * 60 * 1000;

export const RELEASE_RETRY_FIRST_MS = 5_000;

export const RELEASE_RETRY_MAX_MS = 5 * 60 * 1000;

const DURATION_UNIT_MS: Record<string, number> = { s: 1_000, m: 60_000, h: 3_600_000, d: 86_400_000 };

export function durationMs(text: string | undefined): number | undefined {
  const m = /^(\d+)([smhd])$/.exec((text ?? "").trim());
  return m ? Number(m[1]) * DURATION_UNIT_MS[m[2]] : undefined;
}

export function releaseRetryDeadline(createdAt: string | undefined, ttl: string | undefined, now: number): number {
  const created = createdAt ? Date.parse(createdAt) : NaN;
  const bound = durationMs(ttl) ?? DEFAULT_RELEASE_RETRY_BOUND_MS;
  return (Number.isFinite(created) ? created : now) + bound;
}

export function releaseRetryDelayMs(attempt: number): number {
  const n = Math.max(1, attempt);
  return Math.min(RELEASE_RETRY_FIRST_MS * 2 ** Math.min(n - 1, 16), RELEASE_RETRY_MAX_MS);
}

export function releaseAcknowledged(status: number | undefined): boolean {
  return status !== undefined && status >= 200 && status < 300;
}
