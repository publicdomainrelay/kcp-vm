export const GROUP_NAME = "socialweb.computer";
export const VERSION = "v1alpha1";
export const CONDITION_PREPARED = "Prepared";
export const CONDITION_STARTED = "Started";
export const CONDITION_RUNTIME_OBSERVED = "RuntimeObserved";
export const CONDITION_GUEST_REPORTED = "GuestReported";
export const FINALIZER_COMPUTE_VM = "computevm.socialweb.computer/release";
export const FIELD_MANAGER_COMPUTE_VM_CONTROLLER = "computevm-controller";
export const FIELD_MANAGER_COMPUTE_VM_PROVIDER = "compute-provider-kcp";
export const GUEST_HOST_ADDRESS = "10.0.2.2";
export const USER_DATA_SECRET_NAMESPACE = "default";
export const USER_DATA_SECRET_KEY = "user-data";

export const COMPUTE_VM_PHASES = ["Pending", "Preparing", "Booting", "Running", "Releasing", "Lost", "Failed"] as const;
export type ComputeVMPhase = typeof COMPUTE_VM_PHASES[number];

export const COMPUTE_VM_LOST_REASONS = ["HostRebooted", "UnitGone", "UnitFailed", "OOMKilled", "VMMExited", "IdentityMismatch"] as const;
export type ComputeVMLostReason = typeof COMPUTE_VM_LOST_REASONS[number];

export const HOST_PROCESS_RESTART_POLICYS = ["Never", "OnFailure", "Always"] as const;
export type HostProcessRestartPolicy = typeof HOST_PROCESS_RESTART_POLICYS[number];

export const HOST_PROCESS_PLACEMENTS = ["supervisor", "bootstrap"] as const;
export type HostProcessPlacement = typeof HOST_PROCESS_PLACEMENTS[number];

export const HOST_PROCESS_PHASES = ["Pending", "Starting", "Running", "Stopping", "Stopped", "Failed", "Lost"] as const;
export type HostProcessPhase = typeof HOST_PROCESS_PHASES[number];

export const HOST_PROCESS_LOST_REASONS = ["HostRebooted", "UnitGone", "UnitFailed", "OOMKilled", "IdentityMismatch", "ManagerRestarted"] as const;
export type HostProcessLostReason = typeof HOST_PROCESS_LOST_REASONS[number];

export interface ObjectMeta {
  name?: string;
  generateName?: string;
  namespace?: string;
  uid?: string;
  resourceVersion?: string;
  generation?: number;
  creationTimestamp?: string;
  deletionTimestamp?: string;
  deletionGracePeriodSeconds?: number;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  finalizers?: string[];
  [key: string]: unknown;
}

export type Condition = Record<string, unknown>;

export interface ComputeVM {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: ComputeVMSpec;
  status?: ComputeVMStatus;
}

export interface ComputeVMSpec {
  vm: ComputeVMShape;
  owner: ComputeVMOwner;
  userDataSecret: string;
  requester: ComputeVMRequester;
  contract?: ComputeVMContract;
  image?: ComputeVMImageRequest;
  stopGraceSeconds?: number;
}

export interface ComputeVMShape {
  cpus: number;
  mem: string;
  disk: string;
  role: string;
}

export interface ComputeVMOwner {
  handle: string;
}

export interface ComputeVMRequester {
  did: string;
  plc?: string;
}

export interface ComputeVMContract {
  receiptUri?: string;
  receiptCid?: string;
  acceptUri?: string;
  acceptCid?: string;
  submitEventUrl?: string;
  acceptedAt?: string;
}

export interface ComputeVMImageRequest {
  reuseStale?: boolean;
}

export interface ComputeVMStatus {
  phase?: ComputeVMPhase;
  lostReason?: ComputeVMLostReason;
  observedGeneration?: number;
  network?: ComputeVMNetwork;
  image?: ComputeVMImage;
  runtime?: ComputeVMRuntime;
  workDir?: string;
  bootedAt?: string;
  message?: string;
  conditions?: Condition[];
  market?: ComputeVMMarket;
}

export interface ComputeVMNetwork {
  guestIP?: string;
  hostAddress?: string;
  prefix?: number;
  mac?: string;
}

export interface ComputeVMImage {
  ref?: string;
  sha256?: string;
  hypervisor?: string;
}

export interface ComputeVMRuntime {
  backend?: string;
  ref?: string;
  instance?: string;
  pid?: number;
  startTime?: string;
  hostBootID?: string;
  vmmID?: string;
}

export interface ComputeVMMarket {
  onNetworkUri?: string;
  address?: string;
  hostKeys?: ComputeVMHostKey[];
  hostKeysUri?: string;
  reportedAt?: string;
}

export interface ComputeVMHostKey {
  type: string;
  publicKey: string;
  fingerprint?: string;
}

export interface HostProcess {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: HostProcessSpec;
  status?: HostProcessStatus;
}

export interface HostProcessSpec {
  unit: string;
  asset?: HostProcessAsset;
  exec?: string;
  argv?: string[];
  env?: HostProcessEnv[];
  workDir?: string;
  output?: HostProcessOutput;
  limits?: HostProcessLimits;
  restartPolicy?: HostProcessRestartPolicy;
  stop?: HostProcessStop;
  after?: string[];
  wants?: string[];
  slice?: string;
  privateHostname?: string;
  fdStore?: number;
  readiness?: HostProcessReadiness;
  placement: HostProcessPlacement;
}

export interface HostProcessAsset {
  name: string;
  sha256: string;
}

export interface HostProcessEnv {
  name: string;
  value: string;
}

export interface HostProcessOutput {
  stdout?: string;
  stderr?: string;
  capBytes?: number;
}

export interface HostProcessLimits {
  memoryMax?: string;
  cpuWeight?: number;
  cpuQuotaPercent?: number;
  tasksMax?: number;
}

export interface HostProcessStop {
  signal?: string;
  graceSeconds?: number;
}

export interface HostProcessReadiness {
  exec?: string[];
  tcp?: boolean;
  httpPath?: string;
  pgxSocketDir?: string;
  unixSocket?: string;
}

export interface HostProcessStatus {
  observedGeneration?: number;
  phase?: HostProcessPhase;
  lostReason?: HostProcessLostReason;
  message?: string;
  runtime?: HostProcessRuntime;
  lastResult?: HostProcessResult;
  listeners?: HostProcessListener[];
  conditions?: Condition[];
}

export interface HostProcessRuntime {
  backend: string;
  unit: string;
  invocationID?: string;
  pid?: number;
  startTicks?: number;
  hostBootID?: string;
  manager?: string;
  activeSince?: string;
}

export interface HostProcessResult {
  result?: string;
  exitCode?: number;
  signal?: string;
  at?: string;
}

export interface HostProcessListener {
  name: string;
  address: string;
}

export interface ApiResource {
  group: string;
  version: string;
  kind: string;
  listKind: string;
  plural: string;
  singular: string;
  schema: string;
  export: string;
}

export const COMPUTE_VM_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "ComputeVM",
  listKind: "ComputeVMList",
  plural: "computevms",
  singular: "computevm",
  schema: "v1alpha1-1.computevms.socialweb.computer",
  export: "computevms",
};

export const HOST_PROCESS_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "HostProcess",
  listKind: "HostProcessList",
  plural: "hostprocesses",
  singular: "hostprocess",
  schema: "v1alpha1-1.hostprocesses.socialweb.computer",
  export: "hostprocesses",
};

export const API_RESOURCES: readonly ApiResource[] = [COMPUTE_VM_RESOURCE, HOST_PROCESS_RESOURCE];

export type ApiTypeName =
  | "ComputeVM"
  | "ComputeVMContract"
  | "ComputeVMHostKey"
  | "ComputeVMImage"
  | "ComputeVMImageRequest"
  | "ComputeVMLostReason"
  | "ComputeVMMarket"
  | "ComputeVMNetwork"
  | "ComputeVMOwner"
  | "ComputeVMPhase"
  | "ComputeVMRequester"
  | "ComputeVMRuntime"
  | "ComputeVMShape"
  | "ComputeVMSpec"
  | "ComputeVMStatus"
  | "Condition"
  | "HostProcess"
  | "HostProcessAsset"
  | "HostProcessEnv"
  | "HostProcessLimits"
  | "HostProcessListener"
  | "HostProcessLostReason"
  | "HostProcessOutput"
  | "HostProcessPhase"
  | "HostProcessPlacement"
  | "HostProcessReadiness"
  | "HostProcessRestartPolicy"
  | "HostProcessResult"
  | "HostProcessRuntime"
  | "HostProcessSpec"
  | "HostProcessStatus"
  | "HostProcessStop"
  | "ObjectMeta";

type Shape =
  | { type: "string"; enum?: readonly string[] }
  | { type: "integer" }
  | { type: "boolean" }
  | { type: "array"; items: Shape }
  | { type: "object"; open: true }
  | { type: "object"; properties: Readonly<Record<string, Shape>>; required: readonly string[] }
  | { ref: ApiTypeName };

export interface ApiIssue {
  path: string;
  message: string;
}

export class ApiValidationError extends Error {
  constructor(readonly typeName: ApiTypeName, readonly issues: readonly ApiIssue[]) {
    super(`invalid ${typeName}: ${issues.map((i) => `${i.path || "(root)"}: ${i.message}`).join("; ")}`);
    this.name = "ApiValidationError";
  }
}

function joinPath(path: string, key: string): string {
  return path === "" ? key : `${path}.${key}`;
}

function describe(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "an array";
  switch (typeof value) {
    case "object":
      return "an object";
    case "string":
      return "a string";
    case "boolean":
      return "a boolean";
    case "number":
      return "a number";
    default:
      return typeof value;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function check(shape: Shape, value: unknown, path: string, out: ApiIssue[]): void {
  if ("ref" in shape) {
    check(SHAPES[shape.ref], value, path, out);
    return;
  }
  switch (shape.type) {
    case "object": {
      if (!isPlainObject(value)) {
        out.push({ path, message: `expected an object, got ${describe(value)}` });
        return;
      }
      if ("open" in shape) return;
      for (const [name, prop] of Object.entries(shape.properties)) {
        const v = value[name];
        if (v === undefined) {
          if (shape.required.includes(name)) out.push({ path: joinPath(path, name), message: "missing required field" });
          continue;
        }
        check(prop, v, joinPath(path, name), out);
      }
      for (const key of Object.keys(value).sort()) {
        if (!(key in shape.properties) && value[key] !== undefined) {
          out.push({ path: joinPath(path, key), message: "unknown field" });
        }
      }
      return;
    }
    case "array": {
      if (!Array.isArray(value)) {
        out.push({ path, message: `expected an array, got ${describe(value)}` });
        return;
      }
      value.forEach((item, i) => check(shape.items, item, `${path}[${i}]`, out));
      return;
    }
    case "string": {
      if (typeof value !== "string") {
        out.push({ path, message: `expected a string, got ${describe(value)}` });
        return;
      }
      if (shape.enum && !shape.enum.includes(value)) {
        out.push({ path, message: `${JSON.stringify(value)} is not one of ${shape.enum.join(", ")}` });
      }
      return;
    }
    case "integer": {
      if (typeof value !== "number" || !Number.isInteger(value)) {
        out.push({ path, message: `expected an integer, got ${describe(value)}` });
      }
      return;
    }
    case "boolean": {
      if (typeof value !== "boolean") {
        out.push({ path, message: `expected a boolean, got ${describe(value)}` });
      }
      return;
    }
  }
}

export function validate(typeName: ApiTypeName, value: unknown): ApiIssue[] {
  const out: ApiIssue[] = [];
  check(SHAPES[typeName], value, "", out);
  return out;
}

const SHAPES: Readonly<Record<ApiTypeName, Shape>> = {
  "ComputeVM": {
    "properties": {
      "apiVersion": {
        "type": "string"
      },
      "kind": {
        "type": "string"
      },
      "metadata": {
        "ref": "ObjectMeta"
      },
      "spec": {
        "ref": "ComputeVMSpec"
      },
      "status": {
        "ref": "ComputeVMStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMContract": {
    "properties": {
      "acceptCid": {
        "type": "string"
      },
      "acceptUri": {
        "type": "string"
      },
      "acceptedAt": {
        "type": "string"
      },
      "receiptCid": {
        "type": "string"
      },
      "receiptUri": {
        "type": "string"
      },
      "submitEventUrl": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMHostKey": {
    "properties": {
      "fingerprint": {
        "type": "string"
      },
      "publicKey": {
        "type": "string"
      },
      "type": {
        "type": "string"
      }
    },
    "required": [
      "type",
      "publicKey"
    ],
    "type": "object"
  },
  "ComputeVMImage": {
    "properties": {
      "hypervisor": {
        "type": "string"
      },
      "ref": {
        "type": "string"
      },
      "sha256": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMImageRequest": {
    "properties": {
      "reuseStale": {
        "type": "boolean"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMLostReason": {
    "enum": [
      "HostRebooted",
      "UnitGone",
      "UnitFailed",
      "OOMKilled",
      "VMMExited",
      "IdentityMismatch"
    ],
    "type": "string"
  },
  "ComputeVMMarket": {
    "properties": {
      "address": {
        "type": "string"
      },
      "hostKeys": {
        "items": {
          "ref": "ComputeVMHostKey"
        },
        "type": "array"
      },
      "hostKeysUri": {
        "type": "string"
      },
      "onNetworkUri": {
        "type": "string"
      },
      "reportedAt": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMNetwork": {
    "properties": {
      "guestIP": {
        "type": "string"
      },
      "hostAddress": {
        "type": "string"
      },
      "mac": {
        "type": "string"
      },
      "prefix": {
        "type": "integer"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMOwner": {
    "properties": {
      "handle": {
        "type": "string"
      }
    },
    "required": [
      "handle"
    ],
    "type": "object"
  },
  "ComputeVMPhase": {
    "enum": [
      "Pending",
      "Preparing",
      "Booting",
      "Running",
      "Releasing",
      "Lost",
      "Failed"
    ],
    "type": "string"
  },
  "ComputeVMRequester": {
    "properties": {
      "did": {
        "type": "string"
      },
      "plc": {
        "type": "string"
      }
    },
    "required": [
      "did"
    ],
    "type": "object"
  },
  "ComputeVMRuntime": {
    "properties": {
      "backend": {
        "type": "string"
      },
      "hostBootID": {
        "type": "string"
      },
      "instance": {
        "type": "string"
      },
      "pid": {
        "type": "integer"
      },
      "ref": {
        "type": "string"
      },
      "startTime": {
        "type": "string"
      },
      "vmmID": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ComputeVMShape": {
    "properties": {
      "cpus": {
        "type": "integer"
      },
      "disk": {
        "type": "string"
      },
      "mem": {
        "type": "string"
      },
      "role": {
        "type": "string"
      }
    },
    "required": [
      "cpus",
      "mem",
      "disk",
      "role"
    ],
    "type": "object"
  },
  "ComputeVMSpec": {
    "properties": {
      "contract": {
        "ref": "ComputeVMContract"
      },
      "image": {
        "ref": "ComputeVMImageRequest"
      },
      "owner": {
        "ref": "ComputeVMOwner"
      },
      "requester": {
        "ref": "ComputeVMRequester"
      },
      "stopGraceSeconds": {
        "type": "integer"
      },
      "userDataSecret": {
        "type": "string"
      },
      "vm": {
        "ref": "ComputeVMShape"
      }
    },
    "required": [
      "vm",
      "owner",
      "userDataSecret",
      "requester"
    ],
    "type": "object"
  },
  "ComputeVMStatus": {
    "properties": {
      "bootedAt": {
        "type": "string"
      },
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "image": {
        "ref": "ComputeVMImage"
      },
      "lostReason": {
        "ref": "ComputeVMLostReason"
      },
      "market": {
        "ref": "ComputeVMMarket"
      },
      "message": {
        "type": "string"
      },
      "network": {
        "ref": "ComputeVMNetwork"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "phase": {
        "ref": "ComputeVMPhase"
      },
      "runtime": {
        "ref": "ComputeVMRuntime"
      },
      "workDir": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "Condition": {
    "open": true,
    "type": "object"
  },
  "HostProcess": {
    "properties": {
      "apiVersion": {
        "type": "string"
      },
      "kind": {
        "type": "string"
      },
      "metadata": {
        "ref": "ObjectMeta"
      },
      "spec": {
        "ref": "HostProcessSpec"
      },
      "status": {
        "ref": "HostProcessStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessAsset": {
    "properties": {
      "name": {
        "type": "string"
      },
      "sha256": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "sha256"
    ],
    "type": "object"
  },
  "HostProcessEnv": {
    "properties": {
      "name": {
        "type": "string"
      },
      "value": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "value"
    ],
    "type": "object"
  },
  "HostProcessLimits": {
    "properties": {
      "cpuQuotaPercent": {
        "type": "integer"
      },
      "cpuWeight": {
        "type": "integer"
      },
      "memoryMax": {
        "type": "string"
      },
      "tasksMax": {
        "type": "integer"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessListener": {
    "properties": {
      "address": {
        "type": "string"
      },
      "name": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "address"
    ],
    "type": "object"
  },
  "HostProcessLostReason": {
    "enum": [
      "HostRebooted",
      "UnitGone",
      "UnitFailed",
      "OOMKilled",
      "IdentityMismatch",
      "ManagerRestarted"
    ],
    "type": "string"
  },
  "HostProcessOutput": {
    "properties": {
      "capBytes": {
        "type": "integer"
      },
      "stderr": {
        "type": "string"
      },
      "stdout": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessPhase": {
    "enum": [
      "Pending",
      "Starting",
      "Running",
      "Stopping",
      "Stopped",
      "Failed",
      "Lost"
    ],
    "type": "string"
  },
  "HostProcessPlacement": {
    "enum": [
      "supervisor",
      "bootstrap"
    ],
    "type": "string"
  },
  "HostProcessReadiness": {
    "properties": {
      "exec": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "httpPath": {
        "type": "string"
      },
      "pgxSocketDir": {
        "type": "string"
      },
      "tcp": {
        "type": "boolean"
      },
      "unixSocket": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessRestartPolicy": {
    "enum": [
      "Never",
      "OnFailure",
      "Always"
    ],
    "type": "string"
  },
  "HostProcessResult": {
    "properties": {
      "at": {
        "type": "string"
      },
      "exitCode": {
        "type": "integer"
      },
      "result": {
        "type": "string"
      },
      "signal": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessRuntime": {
    "properties": {
      "activeSince": {
        "type": "string"
      },
      "backend": {
        "type": "string"
      },
      "hostBootID": {
        "type": "string"
      },
      "invocationID": {
        "type": "string"
      },
      "manager": {
        "type": "string"
      },
      "pid": {
        "type": "integer"
      },
      "startTicks": {
        "type": "integer"
      },
      "unit": {
        "type": "string"
      }
    },
    "required": [
      "backend",
      "unit"
    ],
    "type": "object"
  },
  "HostProcessSpec": {
    "properties": {
      "after": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "argv": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "asset": {
        "ref": "HostProcessAsset"
      },
      "env": {
        "items": {
          "ref": "HostProcessEnv"
        },
        "type": "array"
      },
      "exec": {
        "type": "string"
      },
      "fdStore": {
        "type": "integer"
      },
      "limits": {
        "ref": "HostProcessLimits"
      },
      "output": {
        "ref": "HostProcessOutput"
      },
      "placement": {
        "ref": "HostProcessPlacement"
      },
      "privateHostname": {
        "type": "string"
      },
      "readiness": {
        "ref": "HostProcessReadiness"
      },
      "restartPolicy": {
        "ref": "HostProcessRestartPolicy"
      },
      "slice": {
        "type": "string"
      },
      "stop": {
        "ref": "HostProcessStop"
      },
      "unit": {
        "type": "string"
      },
      "wants": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "workDir": {
        "type": "string"
      }
    },
    "required": [
      "unit",
      "placement"
    ],
    "type": "object"
  },
  "HostProcessStatus": {
    "properties": {
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "lastResult": {
        "ref": "HostProcessResult"
      },
      "listeners": {
        "items": {
          "ref": "HostProcessListener"
        },
        "type": "array"
      },
      "lostReason": {
        "ref": "HostProcessLostReason"
      },
      "message": {
        "type": "string"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "phase": {
        "ref": "HostProcessPhase"
      },
      "runtime": {
        "ref": "HostProcessRuntime"
      }
    },
    "required": [],
    "type": "object"
  },
  "HostProcessStop": {
    "properties": {
      "graceSeconds": {
        "type": "integer"
      },
      "signal": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ObjectMeta": {
    "open": true,
    "type": "object"
  }
};

export function validateComputeVM(value: unknown): ApiIssue[] {
  return validate("ComputeVM", value);
}

export function isComputeVM(value: unknown): value is ComputeVM {
  return validate("ComputeVM", value).length === 0;
}

export function assertComputeVM(value: unknown): asserts value is ComputeVM {
  const issues = validate("ComputeVM", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVM", issues);
}

export function parseComputeVM(value: unknown): ComputeVM {
  assertComputeVM(value);
  return value;
}

export function validateComputeVMSpec(value: unknown): ApiIssue[] {
  return validate("ComputeVMSpec", value);
}

export function isComputeVMSpec(value: unknown): value is ComputeVMSpec {
  return validate("ComputeVMSpec", value).length === 0;
}

export function assertComputeVMSpec(value: unknown): asserts value is ComputeVMSpec {
  const issues = validate("ComputeVMSpec", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMSpec", issues);
}

export function parseComputeVMSpec(value: unknown): ComputeVMSpec {
  assertComputeVMSpec(value);
  return value;
}

export function validateComputeVMShape(value: unknown): ApiIssue[] {
  return validate("ComputeVMShape", value);
}

export function isComputeVMShape(value: unknown): value is ComputeVMShape {
  return validate("ComputeVMShape", value).length === 0;
}

export function assertComputeVMShape(value: unknown): asserts value is ComputeVMShape {
  const issues = validate("ComputeVMShape", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMShape", issues);
}

export function parseComputeVMShape(value: unknown): ComputeVMShape {
  assertComputeVMShape(value);
  return value;
}

export function validateComputeVMOwner(value: unknown): ApiIssue[] {
  return validate("ComputeVMOwner", value);
}

export function isComputeVMOwner(value: unknown): value is ComputeVMOwner {
  return validate("ComputeVMOwner", value).length === 0;
}

export function assertComputeVMOwner(value: unknown): asserts value is ComputeVMOwner {
  const issues = validate("ComputeVMOwner", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMOwner", issues);
}

export function parseComputeVMOwner(value: unknown): ComputeVMOwner {
  assertComputeVMOwner(value);
  return value;
}

export function validateComputeVMRequester(value: unknown): ApiIssue[] {
  return validate("ComputeVMRequester", value);
}

export function isComputeVMRequester(value: unknown): value is ComputeVMRequester {
  return validate("ComputeVMRequester", value).length === 0;
}

export function assertComputeVMRequester(value: unknown): asserts value is ComputeVMRequester {
  const issues = validate("ComputeVMRequester", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMRequester", issues);
}

export function parseComputeVMRequester(value: unknown): ComputeVMRequester {
  assertComputeVMRequester(value);
  return value;
}

export function validateComputeVMContract(value: unknown): ApiIssue[] {
  return validate("ComputeVMContract", value);
}

export function isComputeVMContract(value: unknown): value is ComputeVMContract {
  return validate("ComputeVMContract", value).length === 0;
}

export function assertComputeVMContract(value: unknown): asserts value is ComputeVMContract {
  const issues = validate("ComputeVMContract", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMContract", issues);
}

export function parseComputeVMContract(value: unknown): ComputeVMContract {
  assertComputeVMContract(value);
  return value;
}

export function validateComputeVMImageRequest(value: unknown): ApiIssue[] {
  return validate("ComputeVMImageRequest", value);
}

export function isComputeVMImageRequest(value: unknown): value is ComputeVMImageRequest {
  return validate("ComputeVMImageRequest", value).length === 0;
}

export function assertComputeVMImageRequest(value: unknown): asserts value is ComputeVMImageRequest {
  const issues = validate("ComputeVMImageRequest", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMImageRequest", issues);
}

export function parseComputeVMImageRequest(value: unknown): ComputeVMImageRequest {
  assertComputeVMImageRequest(value);
  return value;
}

export function validateComputeVMStatus(value: unknown): ApiIssue[] {
  return validate("ComputeVMStatus", value);
}

export function isComputeVMStatus(value: unknown): value is ComputeVMStatus {
  return validate("ComputeVMStatus", value).length === 0;
}

export function assertComputeVMStatus(value: unknown): asserts value is ComputeVMStatus {
  const issues = validate("ComputeVMStatus", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMStatus", issues);
}

export function parseComputeVMStatus(value: unknown): ComputeVMStatus {
  assertComputeVMStatus(value);
  return value;
}

export function validateComputeVMPhase(value: unknown): ApiIssue[] {
  return validate("ComputeVMPhase", value);
}

export function isComputeVMPhase(value: unknown): value is ComputeVMPhase {
  return validate("ComputeVMPhase", value).length === 0;
}

export function assertComputeVMPhase(value: unknown): asserts value is ComputeVMPhase {
  const issues = validate("ComputeVMPhase", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMPhase", issues);
}

export function parseComputeVMPhase(value: unknown): ComputeVMPhase {
  assertComputeVMPhase(value);
  return value;
}

export function validateComputeVMLostReason(value: unknown): ApiIssue[] {
  return validate("ComputeVMLostReason", value);
}

export function isComputeVMLostReason(value: unknown): value is ComputeVMLostReason {
  return validate("ComputeVMLostReason", value).length === 0;
}

export function assertComputeVMLostReason(value: unknown): asserts value is ComputeVMLostReason {
  const issues = validate("ComputeVMLostReason", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMLostReason", issues);
}

export function parseComputeVMLostReason(value: unknown): ComputeVMLostReason {
  assertComputeVMLostReason(value);
  return value;
}

export function validateComputeVMNetwork(value: unknown): ApiIssue[] {
  return validate("ComputeVMNetwork", value);
}

export function isComputeVMNetwork(value: unknown): value is ComputeVMNetwork {
  return validate("ComputeVMNetwork", value).length === 0;
}

export function assertComputeVMNetwork(value: unknown): asserts value is ComputeVMNetwork {
  const issues = validate("ComputeVMNetwork", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMNetwork", issues);
}

export function parseComputeVMNetwork(value: unknown): ComputeVMNetwork {
  assertComputeVMNetwork(value);
  return value;
}

export function validateComputeVMImage(value: unknown): ApiIssue[] {
  return validate("ComputeVMImage", value);
}

export function isComputeVMImage(value: unknown): value is ComputeVMImage {
  return validate("ComputeVMImage", value).length === 0;
}

export function assertComputeVMImage(value: unknown): asserts value is ComputeVMImage {
  const issues = validate("ComputeVMImage", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMImage", issues);
}

export function parseComputeVMImage(value: unknown): ComputeVMImage {
  assertComputeVMImage(value);
  return value;
}

export function validateComputeVMRuntime(value: unknown): ApiIssue[] {
  return validate("ComputeVMRuntime", value);
}

export function isComputeVMRuntime(value: unknown): value is ComputeVMRuntime {
  return validate("ComputeVMRuntime", value).length === 0;
}

export function assertComputeVMRuntime(value: unknown): asserts value is ComputeVMRuntime {
  const issues = validate("ComputeVMRuntime", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMRuntime", issues);
}

export function parseComputeVMRuntime(value: unknown): ComputeVMRuntime {
  assertComputeVMRuntime(value);
  return value;
}

export function validateComputeVMMarket(value: unknown): ApiIssue[] {
  return validate("ComputeVMMarket", value);
}

export function isComputeVMMarket(value: unknown): value is ComputeVMMarket {
  return validate("ComputeVMMarket", value).length === 0;
}

export function assertComputeVMMarket(value: unknown): asserts value is ComputeVMMarket {
  const issues = validate("ComputeVMMarket", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMMarket", issues);
}

export function parseComputeVMMarket(value: unknown): ComputeVMMarket {
  assertComputeVMMarket(value);
  return value;
}

export function validateComputeVMHostKey(value: unknown): ApiIssue[] {
  return validate("ComputeVMHostKey", value);
}

export function isComputeVMHostKey(value: unknown): value is ComputeVMHostKey {
  return validate("ComputeVMHostKey", value).length === 0;
}

export function assertComputeVMHostKey(value: unknown): asserts value is ComputeVMHostKey {
  const issues = validate("ComputeVMHostKey", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMHostKey", issues);
}

export function parseComputeVMHostKey(value: unknown): ComputeVMHostKey {
  assertComputeVMHostKey(value);
  return value;
}

export function validateHostProcess(value: unknown): ApiIssue[] {
  return validate("HostProcess", value);
}

export function isHostProcess(value: unknown): value is HostProcess {
  return validate("HostProcess", value).length === 0;
}

export function assertHostProcess(value: unknown): asserts value is HostProcess {
  const issues = validate("HostProcess", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcess", issues);
}

export function parseHostProcess(value: unknown): HostProcess {
  assertHostProcess(value);
  return value;
}

export function validateHostProcessSpec(value: unknown): ApiIssue[] {
  return validate("HostProcessSpec", value);
}

export function isHostProcessSpec(value: unknown): value is HostProcessSpec {
  return validate("HostProcessSpec", value).length === 0;
}

export function assertHostProcessSpec(value: unknown): asserts value is HostProcessSpec {
  const issues = validate("HostProcessSpec", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessSpec", issues);
}

export function parseHostProcessSpec(value: unknown): HostProcessSpec {
  assertHostProcessSpec(value);
  return value;
}

export function validateHostProcessAsset(value: unknown): ApiIssue[] {
  return validate("HostProcessAsset", value);
}

export function isHostProcessAsset(value: unknown): value is HostProcessAsset {
  return validate("HostProcessAsset", value).length === 0;
}

export function assertHostProcessAsset(value: unknown): asserts value is HostProcessAsset {
  const issues = validate("HostProcessAsset", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessAsset", issues);
}

export function parseHostProcessAsset(value: unknown): HostProcessAsset {
  assertHostProcessAsset(value);
  return value;
}

export function validateHostProcessEnv(value: unknown): ApiIssue[] {
  return validate("HostProcessEnv", value);
}

export function isHostProcessEnv(value: unknown): value is HostProcessEnv {
  return validate("HostProcessEnv", value).length === 0;
}

export function assertHostProcessEnv(value: unknown): asserts value is HostProcessEnv {
  const issues = validate("HostProcessEnv", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessEnv", issues);
}

export function parseHostProcessEnv(value: unknown): HostProcessEnv {
  assertHostProcessEnv(value);
  return value;
}

export function validateHostProcessOutput(value: unknown): ApiIssue[] {
  return validate("HostProcessOutput", value);
}

export function isHostProcessOutput(value: unknown): value is HostProcessOutput {
  return validate("HostProcessOutput", value).length === 0;
}

export function assertHostProcessOutput(value: unknown): asserts value is HostProcessOutput {
  const issues = validate("HostProcessOutput", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessOutput", issues);
}

export function parseHostProcessOutput(value: unknown): HostProcessOutput {
  assertHostProcessOutput(value);
  return value;
}

export function validateHostProcessLimits(value: unknown): ApiIssue[] {
  return validate("HostProcessLimits", value);
}

export function isHostProcessLimits(value: unknown): value is HostProcessLimits {
  return validate("HostProcessLimits", value).length === 0;
}

export function assertHostProcessLimits(value: unknown): asserts value is HostProcessLimits {
  const issues = validate("HostProcessLimits", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessLimits", issues);
}

export function parseHostProcessLimits(value: unknown): HostProcessLimits {
  assertHostProcessLimits(value);
  return value;
}

export function validateHostProcessRestartPolicy(value: unknown): ApiIssue[] {
  return validate("HostProcessRestartPolicy", value);
}

export function isHostProcessRestartPolicy(value: unknown): value is HostProcessRestartPolicy {
  return validate("HostProcessRestartPolicy", value).length === 0;
}

export function assertHostProcessRestartPolicy(value: unknown): asserts value is HostProcessRestartPolicy {
  const issues = validate("HostProcessRestartPolicy", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessRestartPolicy", issues);
}

export function parseHostProcessRestartPolicy(value: unknown): HostProcessRestartPolicy {
  assertHostProcessRestartPolicy(value);
  return value;
}

export function validateHostProcessStop(value: unknown): ApiIssue[] {
  return validate("HostProcessStop", value);
}

export function isHostProcessStop(value: unknown): value is HostProcessStop {
  return validate("HostProcessStop", value).length === 0;
}

export function assertHostProcessStop(value: unknown): asserts value is HostProcessStop {
  const issues = validate("HostProcessStop", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessStop", issues);
}

export function parseHostProcessStop(value: unknown): HostProcessStop {
  assertHostProcessStop(value);
  return value;
}

export function validateHostProcessReadiness(value: unknown): ApiIssue[] {
  return validate("HostProcessReadiness", value);
}

export function isHostProcessReadiness(value: unknown): value is HostProcessReadiness {
  return validate("HostProcessReadiness", value).length === 0;
}

export function assertHostProcessReadiness(value: unknown): asserts value is HostProcessReadiness {
  const issues = validate("HostProcessReadiness", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessReadiness", issues);
}

export function parseHostProcessReadiness(value: unknown): HostProcessReadiness {
  assertHostProcessReadiness(value);
  return value;
}

export function validateHostProcessPlacement(value: unknown): ApiIssue[] {
  return validate("HostProcessPlacement", value);
}

export function isHostProcessPlacement(value: unknown): value is HostProcessPlacement {
  return validate("HostProcessPlacement", value).length === 0;
}

export function assertHostProcessPlacement(value: unknown): asserts value is HostProcessPlacement {
  const issues = validate("HostProcessPlacement", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessPlacement", issues);
}

export function parseHostProcessPlacement(value: unknown): HostProcessPlacement {
  assertHostProcessPlacement(value);
  return value;
}

export function validateHostProcessStatus(value: unknown): ApiIssue[] {
  return validate("HostProcessStatus", value);
}

export function isHostProcessStatus(value: unknown): value is HostProcessStatus {
  return validate("HostProcessStatus", value).length === 0;
}

export function assertHostProcessStatus(value: unknown): asserts value is HostProcessStatus {
  const issues = validate("HostProcessStatus", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessStatus", issues);
}

export function parseHostProcessStatus(value: unknown): HostProcessStatus {
  assertHostProcessStatus(value);
  return value;
}

export function validateHostProcessPhase(value: unknown): ApiIssue[] {
  return validate("HostProcessPhase", value);
}

export function isHostProcessPhase(value: unknown): value is HostProcessPhase {
  return validate("HostProcessPhase", value).length === 0;
}

export function assertHostProcessPhase(value: unknown): asserts value is HostProcessPhase {
  const issues = validate("HostProcessPhase", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessPhase", issues);
}

export function parseHostProcessPhase(value: unknown): HostProcessPhase {
  assertHostProcessPhase(value);
  return value;
}

export function validateHostProcessLostReason(value: unknown): ApiIssue[] {
  return validate("HostProcessLostReason", value);
}

export function isHostProcessLostReason(value: unknown): value is HostProcessLostReason {
  return validate("HostProcessLostReason", value).length === 0;
}

export function assertHostProcessLostReason(value: unknown): asserts value is HostProcessLostReason {
  const issues = validate("HostProcessLostReason", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessLostReason", issues);
}

export function parseHostProcessLostReason(value: unknown): HostProcessLostReason {
  assertHostProcessLostReason(value);
  return value;
}

export function validateHostProcessRuntime(value: unknown): ApiIssue[] {
  return validate("HostProcessRuntime", value);
}

export function isHostProcessRuntime(value: unknown): value is HostProcessRuntime {
  return validate("HostProcessRuntime", value).length === 0;
}

export function assertHostProcessRuntime(value: unknown): asserts value is HostProcessRuntime {
  const issues = validate("HostProcessRuntime", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessRuntime", issues);
}

export function parseHostProcessRuntime(value: unknown): HostProcessRuntime {
  assertHostProcessRuntime(value);
  return value;
}

export function validateHostProcessResult(value: unknown): ApiIssue[] {
  return validate("HostProcessResult", value);
}

export function isHostProcessResult(value: unknown): value is HostProcessResult {
  return validate("HostProcessResult", value).length === 0;
}

export function assertHostProcessResult(value: unknown): asserts value is HostProcessResult {
  const issues = validate("HostProcessResult", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessResult", issues);
}

export function parseHostProcessResult(value: unknown): HostProcessResult {
  assertHostProcessResult(value);
  return value;
}

export function validateHostProcessListener(value: unknown): ApiIssue[] {
  return validate("HostProcessListener", value);
}

export function isHostProcessListener(value: unknown): value is HostProcessListener {
  return validate("HostProcessListener", value).length === 0;
}

export function assertHostProcessListener(value: unknown): asserts value is HostProcessListener {
  const issues = validate("HostProcessListener", value);
  if (issues.length > 0) throw new ApiValidationError("HostProcessListener", issues);
}

export function parseHostProcessListener(value: unknown): HostProcessListener {
  assertHostProcessListener(value);
  return value;
}
