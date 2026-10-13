export const GROUP_NAME = "socialweb.computer";
export const VERSION = "v1alpha1";
export const CONDITION_PREPARED = "Prepared";
export const CONDITION_STARTED = "Started";
export const CONDITION_RUNTIME_OBSERVED = "RuntimeObserved";
export const CONDITION_GUEST_REPORTED = "GuestReported";
export const FINALIZER_COMPUTE_VM = "computevm.socialweb.computer/release";
export const FIELD_MANAGER_COMPUTE_VM_CONTROLLER = "computevm-controller";
export const FIELD_MANAGER_COMPUTE_VM_PROVIDER = "compute-provider-kcp";
export const FIELD_MANAGER_COMPUTE_VM_CONTRACT = "compute-provider-kcp-contract";
export const FIELD_MANAGER_COMPUTE_VM_GUEST = "compute-provider-kcp-guest";
export const GUEST_HOST_ADDRESS = "192.0.2.1";
export const GUEST_VAULT_PREFIX = "guests";
export const GUEST_USER_DATA_ENTRY = "user-data";
export const GUEST_USER_DATA_KEY = "userData";
export const GUEST_PROVISIONING_ENTRY = "provisioning";

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

export const POSTGRES_PHASES = ["Pending", "Starting", "Ready", "Failed", "Deleting"] as const;
export type PostgresPhase = typeof POSTGRES_PHASES[number];

export const OPEN_BAO_SERVER_PHASES = ["Pending", "Starting", "Sealed", "Ready", "Failed"] as const;
export type OpenBaoServerPhase = typeof OPEN_BAO_SERVER_PHASES[number];

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
  userData: ComputeVMUserData;
  requester: ComputeVMRequester;
  contract?: ComputeVMContract;
  image?: ComputeVMImageRequest;
  stopGraceSeconds?: number;
  guestLogin?: ComputeVMGuestLogin;
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

export interface ComputeVMUserData {
  vault: ComputeVMVaultRef;
}

export interface ComputeVMVaultRef {
  server: string;
  mount: string;
  path: string;
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

export interface ComputeVMGuestLogin {
  mount: string;
  bindSecretSha256: string;
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
  envFiles?: string[];
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
  restartKey?: string;
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
  restartKey?: string;
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

export interface Postgres {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: PostgresSpec;
  status?: PostgresStatus;
}

export interface PostgresSpec {
  version?: string;
  dataDir?: string;
  databases?: string[];
  settings?: string[];
  deleteData?: boolean;
}

export interface PostgresStatus {
  observedGeneration?: number;
  phase?: PostgresPhase;
  message?: string;
  version?: string;
  dataDir?: string;
  socketDir?: string;
  user?: string;
  dsn?: string;
  hostProcess?: string;
  runtime?: HostProcessRuntime;
  readyAt?: string;
  conditions?: Condition[];
}

export interface OpenBaoServer {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: OpenBaoServerSpec;
  status?: OpenBaoServerStatus;
}

export interface OpenBaoServerSpec {
  asset?: HostProcessAsset;
  exec?: string;
  listen?: string;
  init?: OpenBaoServerInit;
  restartPolicy?: HostProcessRestartPolicy;
  clients?: OpenBaoServerClient[];
  unit?: string;
  slice?: string;
  after?: string[];
  wants?: string[];
  log?: string;
  storage?: OpenBaoServerStorage;
  tls?: OpenBaoServerTLS;
  declared?: OpenBaoServerDeclared;
  plugins?: OpenBaoPlugin[];
}

export interface OpenBaoServerInit {
  shares?: number;
  threshold?: number;
}

export interface OpenBaoServerClient {
  name: string;
  kvMount?: string;
  policies?: string[];
  tokenTTL?: string;
  tokenMaxTTL?: string;
}

export interface OpenBaoServerStorage {
  postgres?: OpenBaoServerPostgres;
}

export interface OpenBaoServerPostgres {
  socketDir: string;
  user: string;
  database: string;
}

export interface OpenBaoServerTLS {
  certFile: string;
  keyFile: string;
  caFile: string;
  clientCertOptional?: boolean;
}

export interface OpenBaoServerDeclared {
  secretMounts?: OpenBaoSecretMount[];
  authMounts?: OpenBaoAuthMount[];
  policies?: OpenBaoPolicy[];
  certRoles?: OpenBaoCertRole[];
  jwtRoles?: OpenBaoJWTRole[];
  appRoles?: OpenBaoAppRole[];
  pkiRoles?: OpenBaoPKIRole[];
  kv?: OpenBaoKVScope[];
}

export interface OpenBaoSecretMount {
  path: string;
  type: string;
  maxLeaseTTL?: string;
}

export interface OpenBaoAuthMount {
  path: string;
  type: string;
  jwt?: OpenBaoJWTConfig;
}

export interface OpenBaoJWTConfig {
  discoveryURL: string;
  boundIssuer?: string;
}

export interface OpenBaoPolicy {
  name: string;
  rules?: OpenBaoPolicyRule[];
  hcl?: string;
}

export interface OpenBaoPolicyRule {
  path: string;
  capabilities: string[];
}

export interface OpenBaoCertRole {
  mount: string;
  name: string;
  commonNames: string[];
  caFromPKI: string;
  policies: string[];
  tokenTTL?: string;
  tokenMaxTTL?: string;
  noDefaultPolicy?: boolean;
}

export interface OpenBaoJWTRole {
  mount: string;
  name: string;
  roleType?: string;
  boundSubject: string;
  boundAudiences: string[];
  userClaim: string;
  policies: string[];
  tokenTTL?: string;
}

export interface OpenBaoAppRole {
  mount?: string;
  name: string;
  policies: string[];
  tokenTTL?: string;
  tokenMaxTTL?: string;
  secretIDTTL?: string;
}

export interface OpenBaoPKIRole {
  mount: string;
  name: string;
  allowedDomains?: string[];
  allowSubdomains?: boolean;
  allowBareDomains?: boolean;
  allowAnyName?: boolean;
  allowIPSANs?: boolean;
  enforceHostnames?: boolean;
  requireCN?: boolean;
  clientFlag?: boolean;
  serverFlag?: boolean;
  keyType?: string;
  keyBits?: number;
  keyUsage?: string[];
  organization?: string[];
  ttl?: string;
  maxTTL?: string;
}

export interface OpenBaoKVScope {
  mount: string;
  prefix?: string;
  keys?: string[];
  grace?: string;
}

export interface OpenBaoPlugin {
  name: string;
  type: string;
  asset: HostProcessAsset;
  mountPath?: string;
}

export interface OpenBaoServerStatus {
  observedGeneration?: number;
  phase?: OpenBaoServerPhase;
  message?: string;
  address?: string;
  initialized: boolean;
  sealed: boolean;
  hostProcess?: string;
  runtime?: OpenBaoServerRuntime;
  unsealedInstance?: string;
  unsealedAt?: string;
  refs?: OpenBaoServerRefs;
  clients?: OpenBaoServerClientRefs[];
  sync?: OpenBaoServerSync;
  plugins?: OpenBaoServerPluginStatus[];
  conditions?: Condition[];
}

export interface OpenBaoServerRuntime {
  backend?: string;
  ref?: string;
  instance?: string;
  pid?: number;
  startTime?: string;
  hostBootID?: string;
}

export interface OpenBaoServerRefs {
  unsealKeys?: string;
  ca?: string;
  clientCert?: string;
  clientKey?: string;
  certAuthMount?: string;
  certAuthRole?: string;
  kvMount?: string;
}

export interface OpenBaoServerClientRefs {
  name: string;
  certAuthMount?: string;
  certAuthRole?: string;
  policy?: string;
  kvMount?: string;
  clientCert?: string;
  clientKey?: string;
}

export interface OpenBaoServerSync {
  observedGeneration?: number;
  syncedAt?: string;
  lastChangeAt?: string;
  lastChange?: string;
  declared?: string;
  pending?: string[];
  conflicts?: string[];
}

export interface OpenBaoServerPluginStatus {
  name: string;
  type: string;
  sha256?: string;
  command?: string;
  mountPath?: string;
  runningSha256?: string;
  message?: string;
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
  schema: "v1alpha1-3.computevms.socialweb.computer",
  export: "computevms",
};

export const HOST_PROCESS_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "HostProcess",
  listKind: "HostProcessList",
  plural: "hostprocesses",
  singular: "hostprocess",
  schema: "v1alpha1-3.hostprocesses.socialweb.computer",
  export: "hostprocesses",
};

export const POSTGRES_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "Postgres",
  listKind: "PostgresList",
  plural: "postgreses",
  singular: "postgres",
  schema: "v1alpha1-1.postgreses.socialweb.computer",
  export: "hostprocesses",
};

export const OPEN_BAO_SERVER_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "OpenBaoServer",
  listKind: "OpenBaoServerList",
  plural: "openbaoservers",
  singular: "openbaoserver",
  schema: "v1alpha1-5.openbaoservers.socialweb.computer",
  export: "hostprocesses",
};

export const API_RESOURCES: readonly ApiResource[] = [COMPUTE_VM_RESOURCE, HOST_PROCESS_RESOURCE, POSTGRES_RESOURCE, OPEN_BAO_SERVER_RESOURCE];

export type ApiTypeName =
  | "ComputeVM"
  | "ComputeVMContract"
  | "ComputeVMGuestLogin"
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
  | "ComputeVMUserData"
  | "ComputeVMVaultRef"
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
  | "ObjectMeta"
  | "OpenBaoAppRole"
  | "OpenBaoAuthMount"
  | "OpenBaoCertRole"
  | "OpenBaoJWTConfig"
  | "OpenBaoJWTRole"
  | "OpenBaoKVScope"
  | "OpenBaoPKIRole"
  | "OpenBaoPlugin"
  | "OpenBaoPolicy"
  | "OpenBaoPolicyRule"
  | "OpenBaoSecretMount"
  | "OpenBaoServer"
  | "OpenBaoServerClient"
  | "OpenBaoServerClientRefs"
  | "OpenBaoServerDeclared"
  | "OpenBaoServerInit"
  | "OpenBaoServerPhase"
  | "OpenBaoServerPluginStatus"
  | "OpenBaoServerPostgres"
  | "OpenBaoServerRefs"
  | "OpenBaoServerRuntime"
  | "OpenBaoServerSpec"
  | "OpenBaoServerStatus"
  | "OpenBaoServerStorage"
  | "OpenBaoServerSync"
  | "OpenBaoServerTLS"
  | "Postgres"
  | "PostgresPhase"
  | "PostgresSpec"
  | "PostgresStatus";

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
  "ComputeVMGuestLogin": {
    "properties": {
      "bindSecretSha256": {
        "type": "string"
      },
      "mount": {
        "type": "string"
      }
    },
    "required": [
      "mount",
      "bindSecretSha256"
    ],
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
      "guestLogin": {
        "ref": "ComputeVMGuestLogin"
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
      "userData": {
        "ref": "ComputeVMUserData"
      },
      "vm": {
        "ref": "ComputeVMShape"
      }
    },
    "required": [
      "vm",
      "owner",
      "userData",
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
  "ComputeVMUserData": {
    "properties": {
      "vault": {
        "ref": "ComputeVMVaultRef"
      }
    },
    "required": [
      "vault"
    ],
    "type": "object"
  },
  "ComputeVMVaultRef": {
    "properties": {
      "mount": {
        "type": "string"
      },
      "path": {
        "type": "string"
      },
      "server": {
        "type": "string"
      }
    },
    "required": [
      "server",
      "mount",
      "path"
    ],
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
      "envFiles": {
        "items": {
          "type": "string"
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
      "restartKey": {
        "type": "string"
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
      "restartKey": {
        "type": "string"
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
  },
  "OpenBaoAppRole": {
    "properties": {
      "mount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "policies": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "secretIDTTL": {
        "type": "string"
      },
      "tokenMaxTTL": {
        "type": "string"
      },
      "tokenTTL": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "policies"
    ],
    "type": "object"
  },
  "OpenBaoAuthMount": {
    "properties": {
      "jwt": {
        "ref": "OpenBaoJWTConfig"
      },
      "path": {
        "type": "string"
      },
      "type": {
        "type": "string"
      }
    },
    "required": [
      "path",
      "type"
    ],
    "type": "object"
  },
  "OpenBaoCertRole": {
    "properties": {
      "caFromPKI": {
        "type": "string"
      },
      "commonNames": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "mount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "noDefaultPolicy": {
        "type": "boolean"
      },
      "policies": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "tokenMaxTTL": {
        "type": "string"
      },
      "tokenTTL": {
        "type": "string"
      }
    },
    "required": [
      "mount",
      "name",
      "commonNames",
      "caFromPKI",
      "policies"
    ],
    "type": "object"
  },
  "OpenBaoJWTConfig": {
    "properties": {
      "boundIssuer": {
        "type": "string"
      },
      "discoveryURL": {
        "type": "string"
      }
    },
    "required": [
      "discoveryURL"
    ],
    "type": "object"
  },
  "OpenBaoJWTRole": {
    "properties": {
      "boundAudiences": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "boundSubject": {
        "type": "string"
      },
      "mount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "policies": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "roleType": {
        "type": "string"
      },
      "tokenTTL": {
        "type": "string"
      },
      "userClaim": {
        "type": "string"
      }
    },
    "required": [
      "mount",
      "name",
      "boundSubject",
      "boundAudiences",
      "userClaim",
      "policies"
    ],
    "type": "object"
  },
  "OpenBaoKVScope": {
    "properties": {
      "grace": {
        "type": "string"
      },
      "keys": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "mount": {
        "type": "string"
      },
      "prefix": {
        "type": "string"
      }
    },
    "required": [
      "mount"
    ],
    "type": "object"
  },
  "OpenBaoPKIRole": {
    "properties": {
      "allowAnyName": {
        "type": "boolean"
      },
      "allowBareDomains": {
        "type": "boolean"
      },
      "allowIPSANs": {
        "type": "boolean"
      },
      "allowSubdomains": {
        "type": "boolean"
      },
      "allowedDomains": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "clientFlag": {
        "type": "boolean"
      },
      "enforceHostnames": {
        "type": "boolean"
      },
      "keyBits": {
        "type": "integer"
      },
      "keyType": {
        "type": "string"
      },
      "keyUsage": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "maxTTL": {
        "type": "string"
      },
      "mount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "organization": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "requireCN": {
        "type": "boolean"
      },
      "serverFlag": {
        "type": "boolean"
      },
      "ttl": {
        "type": "string"
      }
    },
    "required": [
      "mount",
      "name"
    ],
    "type": "object"
  },
  "OpenBaoPlugin": {
    "properties": {
      "asset": {
        "ref": "HostProcessAsset"
      },
      "mountPath": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "type": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "type",
      "asset"
    ],
    "type": "object"
  },
  "OpenBaoPolicy": {
    "properties": {
      "hcl": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "rules": {
        "items": {
          "ref": "OpenBaoPolicyRule"
        },
        "type": "array"
      }
    },
    "required": [
      "name"
    ],
    "type": "object"
  },
  "OpenBaoPolicyRule": {
    "properties": {
      "capabilities": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "path": {
        "type": "string"
      }
    },
    "required": [
      "path",
      "capabilities"
    ],
    "type": "object"
  },
  "OpenBaoSecretMount": {
    "properties": {
      "maxLeaseTTL": {
        "type": "string"
      },
      "path": {
        "type": "string"
      },
      "type": {
        "type": "string"
      }
    },
    "required": [
      "path",
      "type"
    ],
    "type": "object"
  },
  "OpenBaoServer": {
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
        "ref": "OpenBaoServerSpec"
      },
      "status": {
        "ref": "OpenBaoServerStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerClient": {
    "properties": {
      "kvMount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "policies": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "tokenMaxTTL": {
        "type": "string"
      },
      "tokenTTL": {
        "type": "string"
      }
    },
    "required": [
      "name"
    ],
    "type": "object"
  },
  "OpenBaoServerClientRefs": {
    "properties": {
      "certAuthMount": {
        "type": "string"
      },
      "certAuthRole": {
        "type": "string"
      },
      "clientCert": {
        "type": "string"
      },
      "clientKey": {
        "type": "string"
      },
      "kvMount": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "policy": {
        "type": "string"
      }
    },
    "required": [
      "name"
    ],
    "type": "object"
  },
  "OpenBaoServerDeclared": {
    "properties": {
      "appRoles": {
        "items": {
          "ref": "OpenBaoAppRole"
        },
        "type": "array"
      },
      "authMounts": {
        "items": {
          "ref": "OpenBaoAuthMount"
        },
        "type": "array"
      },
      "certRoles": {
        "items": {
          "ref": "OpenBaoCertRole"
        },
        "type": "array"
      },
      "jwtRoles": {
        "items": {
          "ref": "OpenBaoJWTRole"
        },
        "type": "array"
      },
      "kv": {
        "items": {
          "ref": "OpenBaoKVScope"
        },
        "type": "array"
      },
      "pkiRoles": {
        "items": {
          "ref": "OpenBaoPKIRole"
        },
        "type": "array"
      },
      "policies": {
        "items": {
          "ref": "OpenBaoPolicy"
        },
        "type": "array"
      },
      "secretMounts": {
        "items": {
          "ref": "OpenBaoSecretMount"
        },
        "type": "array"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerInit": {
    "properties": {
      "shares": {
        "type": "integer"
      },
      "threshold": {
        "type": "integer"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerPhase": {
    "enum": [
      "Pending",
      "Starting",
      "Sealed",
      "Ready",
      "Failed"
    ],
    "type": "string"
  },
  "OpenBaoServerPluginStatus": {
    "properties": {
      "command": {
        "type": "string"
      },
      "message": {
        "type": "string"
      },
      "mountPath": {
        "type": "string"
      },
      "name": {
        "type": "string"
      },
      "runningSha256": {
        "type": "string"
      },
      "sha256": {
        "type": "string"
      },
      "type": {
        "type": "string"
      }
    },
    "required": [
      "name",
      "type"
    ],
    "type": "object"
  },
  "OpenBaoServerPostgres": {
    "properties": {
      "database": {
        "type": "string"
      },
      "socketDir": {
        "type": "string"
      },
      "user": {
        "type": "string"
      }
    },
    "required": [
      "socketDir",
      "user",
      "database"
    ],
    "type": "object"
  },
  "OpenBaoServerRefs": {
    "properties": {
      "ca": {
        "type": "string"
      },
      "certAuthMount": {
        "type": "string"
      },
      "certAuthRole": {
        "type": "string"
      },
      "clientCert": {
        "type": "string"
      },
      "clientKey": {
        "type": "string"
      },
      "kvMount": {
        "type": "string"
      },
      "unsealKeys": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerRuntime": {
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
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerSpec": {
    "properties": {
      "after": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "asset": {
        "ref": "HostProcessAsset"
      },
      "clients": {
        "items": {
          "ref": "OpenBaoServerClient"
        },
        "type": "array"
      },
      "declared": {
        "ref": "OpenBaoServerDeclared"
      },
      "exec": {
        "type": "string"
      },
      "init": {
        "ref": "OpenBaoServerInit"
      },
      "listen": {
        "type": "string"
      },
      "log": {
        "type": "string"
      },
      "plugins": {
        "items": {
          "ref": "OpenBaoPlugin"
        },
        "type": "array"
      },
      "restartPolicy": {
        "ref": "HostProcessRestartPolicy"
      },
      "slice": {
        "type": "string"
      },
      "storage": {
        "ref": "OpenBaoServerStorage"
      },
      "tls": {
        "ref": "OpenBaoServerTLS"
      },
      "unit": {
        "type": "string"
      },
      "wants": {
        "items": {
          "type": "string"
        },
        "type": "array"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerStatus": {
    "properties": {
      "address": {
        "type": "string"
      },
      "clients": {
        "items": {
          "ref": "OpenBaoServerClientRefs"
        },
        "type": "array"
      },
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "hostProcess": {
        "type": "string"
      },
      "initialized": {
        "type": "boolean"
      },
      "message": {
        "type": "string"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "phase": {
        "ref": "OpenBaoServerPhase"
      },
      "plugins": {
        "items": {
          "ref": "OpenBaoServerPluginStatus"
        },
        "type": "array"
      },
      "refs": {
        "ref": "OpenBaoServerRefs"
      },
      "runtime": {
        "ref": "OpenBaoServerRuntime"
      },
      "sealed": {
        "type": "boolean"
      },
      "sync": {
        "ref": "OpenBaoServerSync"
      },
      "unsealedAt": {
        "type": "string"
      },
      "unsealedInstance": {
        "type": "string"
      }
    },
    "required": [
      "initialized",
      "sealed"
    ],
    "type": "object"
  },
  "OpenBaoServerStorage": {
    "properties": {
      "postgres": {
        "ref": "OpenBaoServerPostgres"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerSync": {
    "properties": {
      "conflicts": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "declared": {
        "type": "string"
      },
      "lastChange": {
        "type": "string"
      },
      "lastChangeAt": {
        "type": "string"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "pending": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "syncedAt": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "OpenBaoServerTLS": {
    "properties": {
      "caFile": {
        "type": "string"
      },
      "certFile": {
        "type": "string"
      },
      "clientCertOptional": {
        "type": "boolean"
      },
      "keyFile": {
        "type": "string"
      }
    },
    "required": [
      "certFile",
      "keyFile",
      "caFile"
    ],
    "type": "object"
  },
  "Postgres": {
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
        "ref": "PostgresSpec"
      },
      "status": {
        "ref": "PostgresStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "PostgresPhase": {
    "enum": [
      "Pending",
      "Starting",
      "Ready",
      "Failed",
      "Deleting"
    ],
    "type": "string"
  },
  "PostgresSpec": {
    "properties": {
      "dataDir": {
        "type": "string"
      },
      "databases": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "deleteData": {
        "type": "boolean"
      },
      "settings": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "version": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "PostgresStatus": {
    "properties": {
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "dataDir": {
        "type": "string"
      },
      "dsn": {
        "type": "string"
      },
      "hostProcess": {
        "type": "string"
      },
      "message": {
        "type": "string"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "phase": {
        "ref": "PostgresPhase"
      },
      "readyAt": {
        "type": "string"
      },
      "runtime": {
        "ref": "HostProcessRuntime"
      },
      "socketDir": {
        "type": "string"
      },
      "user": {
        "type": "string"
      },
      "version": {
        "type": "string"
      }
    },
    "required": [],
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

export function validateComputeVMUserData(value: unknown): ApiIssue[] {
  return validate("ComputeVMUserData", value);
}

export function isComputeVMUserData(value: unknown): value is ComputeVMUserData {
  return validate("ComputeVMUserData", value).length === 0;
}

export function assertComputeVMUserData(value: unknown): asserts value is ComputeVMUserData {
  const issues = validate("ComputeVMUserData", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMUserData", issues);
}

export function parseComputeVMUserData(value: unknown): ComputeVMUserData {
  assertComputeVMUserData(value);
  return value;
}

export function validateComputeVMVaultRef(value: unknown): ApiIssue[] {
  return validate("ComputeVMVaultRef", value);
}

export function isComputeVMVaultRef(value: unknown): value is ComputeVMVaultRef {
  return validate("ComputeVMVaultRef", value).length === 0;
}

export function assertComputeVMVaultRef(value: unknown): asserts value is ComputeVMVaultRef {
  const issues = validate("ComputeVMVaultRef", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMVaultRef", issues);
}

export function parseComputeVMVaultRef(value: unknown): ComputeVMVaultRef {
  assertComputeVMVaultRef(value);
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

export function validateComputeVMGuestLogin(value: unknown): ApiIssue[] {
  return validate("ComputeVMGuestLogin", value);
}

export function isComputeVMGuestLogin(value: unknown): value is ComputeVMGuestLogin {
  return validate("ComputeVMGuestLogin", value).length === 0;
}

export function assertComputeVMGuestLogin(value: unknown): asserts value is ComputeVMGuestLogin {
  const issues = validate("ComputeVMGuestLogin", value);
  if (issues.length > 0) throw new ApiValidationError("ComputeVMGuestLogin", issues);
}

export function parseComputeVMGuestLogin(value: unknown): ComputeVMGuestLogin {
  assertComputeVMGuestLogin(value);
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

export function validatePostgres(value: unknown): ApiIssue[] {
  return validate("Postgres", value);
}

export function isPostgres(value: unknown): value is Postgres {
  return validate("Postgres", value).length === 0;
}

export function assertPostgres(value: unknown): asserts value is Postgres {
  const issues = validate("Postgres", value);
  if (issues.length > 0) throw new ApiValidationError("Postgres", issues);
}

export function parsePostgres(value: unknown): Postgres {
  assertPostgres(value);
  return value;
}

export function validatePostgresSpec(value: unknown): ApiIssue[] {
  return validate("PostgresSpec", value);
}

export function isPostgresSpec(value: unknown): value is PostgresSpec {
  return validate("PostgresSpec", value).length === 0;
}

export function assertPostgresSpec(value: unknown): asserts value is PostgresSpec {
  const issues = validate("PostgresSpec", value);
  if (issues.length > 0) throw new ApiValidationError("PostgresSpec", issues);
}

export function parsePostgresSpec(value: unknown): PostgresSpec {
  assertPostgresSpec(value);
  return value;
}

export function validatePostgresStatus(value: unknown): ApiIssue[] {
  return validate("PostgresStatus", value);
}

export function isPostgresStatus(value: unknown): value is PostgresStatus {
  return validate("PostgresStatus", value).length === 0;
}

export function assertPostgresStatus(value: unknown): asserts value is PostgresStatus {
  const issues = validate("PostgresStatus", value);
  if (issues.length > 0) throw new ApiValidationError("PostgresStatus", issues);
}

export function parsePostgresStatus(value: unknown): PostgresStatus {
  assertPostgresStatus(value);
  return value;
}

export function validatePostgresPhase(value: unknown): ApiIssue[] {
  return validate("PostgresPhase", value);
}

export function isPostgresPhase(value: unknown): value is PostgresPhase {
  return validate("PostgresPhase", value).length === 0;
}

export function assertPostgresPhase(value: unknown): asserts value is PostgresPhase {
  const issues = validate("PostgresPhase", value);
  if (issues.length > 0) throw new ApiValidationError("PostgresPhase", issues);
}

export function parsePostgresPhase(value: unknown): PostgresPhase {
  assertPostgresPhase(value);
  return value;
}

export function validateOpenBaoServer(value: unknown): ApiIssue[] {
  return validate("OpenBaoServer", value);
}

export function isOpenBaoServer(value: unknown): value is OpenBaoServer {
  return validate("OpenBaoServer", value).length === 0;
}

export function assertOpenBaoServer(value: unknown): asserts value is OpenBaoServer {
  const issues = validate("OpenBaoServer", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServer", issues);
}

export function parseOpenBaoServer(value: unknown): OpenBaoServer {
  assertOpenBaoServer(value);
  return value;
}

export function validateOpenBaoServerSpec(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerSpec", value);
}

export function isOpenBaoServerSpec(value: unknown): value is OpenBaoServerSpec {
  return validate("OpenBaoServerSpec", value).length === 0;
}

export function assertOpenBaoServerSpec(value: unknown): asserts value is OpenBaoServerSpec {
  const issues = validate("OpenBaoServerSpec", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerSpec", issues);
}

export function parseOpenBaoServerSpec(value: unknown): OpenBaoServerSpec {
  assertOpenBaoServerSpec(value);
  return value;
}

export function validateOpenBaoServerInit(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerInit", value);
}

export function isOpenBaoServerInit(value: unknown): value is OpenBaoServerInit {
  return validate("OpenBaoServerInit", value).length === 0;
}

export function assertOpenBaoServerInit(value: unknown): asserts value is OpenBaoServerInit {
  const issues = validate("OpenBaoServerInit", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerInit", issues);
}

export function parseOpenBaoServerInit(value: unknown): OpenBaoServerInit {
  assertOpenBaoServerInit(value);
  return value;
}

export function validateOpenBaoServerClient(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerClient", value);
}

export function isOpenBaoServerClient(value: unknown): value is OpenBaoServerClient {
  return validate("OpenBaoServerClient", value).length === 0;
}

export function assertOpenBaoServerClient(value: unknown): asserts value is OpenBaoServerClient {
  const issues = validate("OpenBaoServerClient", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerClient", issues);
}

export function parseOpenBaoServerClient(value: unknown): OpenBaoServerClient {
  assertOpenBaoServerClient(value);
  return value;
}

export function validateOpenBaoServerStorage(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerStorage", value);
}

export function isOpenBaoServerStorage(value: unknown): value is OpenBaoServerStorage {
  return validate("OpenBaoServerStorage", value).length === 0;
}

export function assertOpenBaoServerStorage(value: unknown): asserts value is OpenBaoServerStorage {
  const issues = validate("OpenBaoServerStorage", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerStorage", issues);
}

export function parseOpenBaoServerStorage(value: unknown): OpenBaoServerStorage {
  assertOpenBaoServerStorage(value);
  return value;
}

export function validateOpenBaoServerPostgres(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerPostgres", value);
}

export function isOpenBaoServerPostgres(value: unknown): value is OpenBaoServerPostgres {
  return validate("OpenBaoServerPostgres", value).length === 0;
}

export function assertOpenBaoServerPostgres(value: unknown): asserts value is OpenBaoServerPostgres {
  const issues = validate("OpenBaoServerPostgres", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerPostgres", issues);
}

export function parseOpenBaoServerPostgres(value: unknown): OpenBaoServerPostgres {
  assertOpenBaoServerPostgres(value);
  return value;
}

export function validateOpenBaoServerTLS(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerTLS", value);
}

export function isOpenBaoServerTLS(value: unknown): value is OpenBaoServerTLS {
  return validate("OpenBaoServerTLS", value).length === 0;
}

export function assertOpenBaoServerTLS(value: unknown): asserts value is OpenBaoServerTLS {
  const issues = validate("OpenBaoServerTLS", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerTLS", issues);
}

export function parseOpenBaoServerTLS(value: unknown): OpenBaoServerTLS {
  assertOpenBaoServerTLS(value);
  return value;
}

export function validateOpenBaoServerDeclared(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerDeclared", value);
}

export function isOpenBaoServerDeclared(value: unknown): value is OpenBaoServerDeclared {
  return validate("OpenBaoServerDeclared", value).length === 0;
}

export function assertOpenBaoServerDeclared(value: unknown): asserts value is OpenBaoServerDeclared {
  const issues = validate("OpenBaoServerDeclared", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerDeclared", issues);
}

export function parseOpenBaoServerDeclared(value: unknown): OpenBaoServerDeclared {
  assertOpenBaoServerDeclared(value);
  return value;
}

export function validateOpenBaoSecretMount(value: unknown): ApiIssue[] {
  return validate("OpenBaoSecretMount", value);
}

export function isOpenBaoSecretMount(value: unknown): value is OpenBaoSecretMount {
  return validate("OpenBaoSecretMount", value).length === 0;
}

export function assertOpenBaoSecretMount(value: unknown): asserts value is OpenBaoSecretMount {
  const issues = validate("OpenBaoSecretMount", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoSecretMount", issues);
}

export function parseOpenBaoSecretMount(value: unknown): OpenBaoSecretMount {
  assertOpenBaoSecretMount(value);
  return value;
}

export function validateOpenBaoAuthMount(value: unknown): ApiIssue[] {
  return validate("OpenBaoAuthMount", value);
}

export function isOpenBaoAuthMount(value: unknown): value is OpenBaoAuthMount {
  return validate("OpenBaoAuthMount", value).length === 0;
}

export function assertOpenBaoAuthMount(value: unknown): asserts value is OpenBaoAuthMount {
  const issues = validate("OpenBaoAuthMount", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoAuthMount", issues);
}

export function parseOpenBaoAuthMount(value: unknown): OpenBaoAuthMount {
  assertOpenBaoAuthMount(value);
  return value;
}

export function validateOpenBaoJWTConfig(value: unknown): ApiIssue[] {
  return validate("OpenBaoJWTConfig", value);
}

export function isOpenBaoJWTConfig(value: unknown): value is OpenBaoJWTConfig {
  return validate("OpenBaoJWTConfig", value).length === 0;
}

export function assertOpenBaoJWTConfig(value: unknown): asserts value is OpenBaoJWTConfig {
  const issues = validate("OpenBaoJWTConfig", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoJWTConfig", issues);
}

export function parseOpenBaoJWTConfig(value: unknown): OpenBaoJWTConfig {
  assertOpenBaoJWTConfig(value);
  return value;
}

export function validateOpenBaoPolicy(value: unknown): ApiIssue[] {
  return validate("OpenBaoPolicy", value);
}

export function isOpenBaoPolicy(value: unknown): value is OpenBaoPolicy {
  return validate("OpenBaoPolicy", value).length === 0;
}

export function assertOpenBaoPolicy(value: unknown): asserts value is OpenBaoPolicy {
  const issues = validate("OpenBaoPolicy", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoPolicy", issues);
}

export function parseOpenBaoPolicy(value: unknown): OpenBaoPolicy {
  assertOpenBaoPolicy(value);
  return value;
}

export function validateOpenBaoPolicyRule(value: unknown): ApiIssue[] {
  return validate("OpenBaoPolicyRule", value);
}

export function isOpenBaoPolicyRule(value: unknown): value is OpenBaoPolicyRule {
  return validate("OpenBaoPolicyRule", value).length === 0;
}

export function assertOpenBaoPolicyRule(value: unknown): asserts value is OpenBaoPolicyRule {
  const issues = validate("OpenBaoPolicyRule", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoPolicyRule", issues);
}

export function parseOpenBaoPolicyRule(value: unknown): OpenBaoPolicyRule {
  assertOpenBaoPolicyRule(value);
  return value;
}

export function validateOpenBaoCertRole(value: unknown): ApiIssue[] {
  return validate("OpenBaoCertRole", value);
}

export function isOpenBaoCertRole(value: unknown): value is OpenBaoCertRole {
  return validate("OpenBaoCertRole", value).length === 0;
}

export function assertOpenBaoCertRole(value: unknown): asserts value is OpenBaoCertRole {
  const issues = validate("OpenBaoCertRole", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoCertRole", issues);
}

export function parseOpenBaoCertRole(value: unknown): OpenBaoCertRole {
  assertOpenBaoCertRole(value);
  return value;
}

export function validateOpenBaoJWTRole(value: unknown): ApiIssue[] {
  return validate("OpenBaoJWTRole", value);
}

export function isOpenBaoJWTRole(value: unknown): value is OpenBaoJWTRole {
  return validate("OpenBaoJWTRole", value).length === 0;
}

export function assertOpenBaoJWTRole(value: unknown): asserts value is OpenBaoJWTRole {
  const issues = validate("OpenBaoJWTRole", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoJWTRole", issues);
}

export function parseOpenBaoJWTRole(value: unknown): OpenBaoJWTRole {
  assertOpenBaoJWTRole(value);
  return value;
}

export function validateOpenBaoAppRole(value: unknown): ApiIssue[] {
  return validate("OpenBaoAppRole", value);
}

export function isOpenBaoAppRole(value: unknown): value is OpenBaoAppRole {
  return validate("OpenBaoAppRole", value).length === 0;
}

export function assertOpenBaoAppRole(value: unknown): asserts value is OpenBaoAppRole {
  const issues = validate("OpenBaoAppRole", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoAppRole", issues);
}

export function parseOpenBaoAppRole(value: unknown): OpenBaoAppRole {
  assertOpenBaoAppRole(value);
  return value;
}

export function validateOpenBaoPKIRole(value: unknown): ApiIssue[] {
  return validate("OpenBaoPKIRole", value);
}

export function isOpenBaoPKIRole(value: unknown): value is OpenBaoPKIRole {
  return validate("OpenBaoPKIRole", value).length === 0;
}

export function assertOpenBaoPKIRole(value: unknown): asserts value is OpenBaoPKIRole {
  const issues = validate("OpenBaoPKIRole", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoPKIRole", issues);
}

export function parseOpenBaoPKIRole(value: unknown): OpenBaoPKIRole {
  assertOpenBaoPKIRole(value);
  return value;
}

export function validateOpenBaoKVScope(value: unknown): ApiIssue[] {
  return validate("OpenBaoKVScope", value);
}

export function isOpenBaoKVScope(value: unknown): value is OpenBaoKVScope {
  return validate("OpenBaoKVScope", value).length === 0;
}

export function assertOpenBaoKVScope(value: unknown): asserts value is OpenBaoKVScope {
  const issues = validate("OpenBaoKVScope", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoKVScope", issues);
}

export function parseOpenBaoKVScope(value: unknown): OpenBaoKVScope {
  assertOpenBaoKVScope(value);
  return value;
}

export function validateOpenBaoPlugin(value: unknown): ApiIssue[] {
  return validate("OpenBaoPlugin", value);
}

export function isOpenBaoPlugin(value: unknown): value is OpenBaoPlugin {
  return validate("OpenBaoPlugin", value).length === 0;
}

export function assertOpenBaoPlugin(value: unknown): asserts value is OpenBaoPlugin {
  const issues = validate("OpenBaoPlugin", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoPlugin", issues);
}

export function parseOpenBaoPlugin(value: unknown): OpenBaoPlugin {
  assertOpenBaoPlugin(value);
  return value;
}

export function validateOpenBaoServerStatus(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerStatus", value);
}

export function isOpenBaoServerStatus(value: unknown): value is OpenBaoServerStatus {
  return validate("OpenBaoServerStatus", value).length === 0;
}

export function assertOpenBaoServerStatus(value: unknown): asserts value is OpenBaoServerStatus {
  const issues = validate("OpenBaoServerStatus", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerStatus", issues);
}

export function parseOpenBaoServerStatus(value: unknown): OpenBaoServerStatus {
  assertOpenBaoServerStatus(value);
  return value;
}

export function validateOpenBaoServerPhase(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerPhase", value);
}

export function isOpenBaoServerPhase(value: unknown): value is OpenBaoServerPhase {
  return validate("OpenBaoServerPhase", value).length === 0;
}

export function assertOpenBaoServerPhase(value: unknown): asserts value is OpenBaoServerPhase {
  const issues = validate("OpenBaoServerPhase", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerPhase", issues);
}

export function parseOpenBaoServerPhase(value: unknown): OpenBaoServerPhase {
  assertOpenBaoServerPhase(value);
  return value;
}

export function validateOpenBaoServerRuntime(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerRuntime", value);
}

export function isOpenBaoServerRuntime(value: unknown): value is OpenBaoServerRuntime {
  return validate("OpenBaoServerRuntime", value).length === 0;
}

export function assertOpenBaoServerRuntime(value: unknown): asserts value is OpenBaoServerRuntime {
  const issues = validate("OpenBaoServerRuntime", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerRuntime", issues);
}

export function parseOpenBaoServerRuntime(value: unknown): OpenBaoServerRuntime {
  assertOpenBaoServerRuntime(value);
  return value;
}

export function validateOpenBaoServerRefs(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerRefs", value);
}

export function isOpenBaoServerRefs(value: unknown): value is OpenBaoServerRefs {
  return validate("OpenBaoServerRefs", value).length === 0;
}

export function assertOpenBaoServerRefs(value: unknown): asserts value is OpenBaoServerRefs {
  const issues = validate("OpenBaoServerRefs", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerRefs", issues);
}

export function parseOpenBaoServerRefs(value: unknown): OpenBaoServerRefs {
  assertOpenBaoServerRefs(value);
  return value;
}

export function validateOpenBaoServerClientRefs(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerClientRefs", value);
}

export function isOpenBaoServerClientRefs(value: unknown): value is OpenBaoServerClientRefs {
  return validate("OpenBaoServerClientRefs", value).length === 0;
}

export function assertOpenBaoServerClientRefs(value: unknown): asserts value is OpenBaoServerClientRefs {
  const issues = validate("OpenBaoServerClientRefs", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerClientRefs", issues);
}

export function parseOpenBaoServerClientRefs(value: unknown): OpenBaoServerClientRefs {
  assertOpenBaoServerClientRefs(value);
  return value;
}

export function validateOpenBaoServerSync(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerSync", value);
}

export function isOpenBaoServerSync(value: unknown): value is OpenBaoServerSync {
  return validate("OpenBaoServerSync", value).length === 0;
}

export function assertOpenBaoServerSync(value: unknown): asserts value is OpenBaoServerSync {
  const issues = validate("OpenBaoServerSync", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerSync", issues);
}

export function parseOpenBaoServerSync(value: unknown): OpenBaoServerSync {
  assertOpenBaoServerSync(value);
  return value;
}

export function validateOpenBaoServerPluginStatus(value: unknown): ApiIssue[] {
  return validate("OpenBaoServerPluginStatus", value);
}

export function isOpenBaoServerPluginStatus(value: unknown): value is OpenBaoServerPluginStatus {
  return validate("OpenBaoServerPluginStatus", value).length === 0;
}

export function assertOpenBaoServerPluginStatus(value: unknown): asserts value is OpenBaoServerPluginStatus {
  const issues = validate("OpenBaoServerPluginStatus", value);
  if (issues.length > 0) throw new ApiValidationError("OpenBaoServerPluginStatus", issues);
}

export function parseOpenBaoServerPluginStatus(value: unknown): OpenBaoServerPluginStatus {
  assertOpenBaoServerPluginStatus(value);
  return value;
}
