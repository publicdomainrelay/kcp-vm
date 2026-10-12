export const GROUP_NAME = "socialweb.computer";
export const VERSION = "v1alpha1";
export const FINALIZER_ATPROTO_IDENTITY = "atprotoidentity.socialweb.computer/revocation";
export const CONDITION_TEARDOWN_BLOCKED = "TeardownBlocked";
export const CONDITION_TENANT_PATH_SHUT = "TenantPathShut";
export const CONDITION_REACHABLE = "Reachable";
export const CONDITION_REVOCATION_PENDING = "RevocationPending";
export const CONDITION_IDENTITY_VERIFICATION_FAILED = "IdentityVerificationFailed";
export const FINALIZER_CLUSTER = "cluster.socialweb.computer/teardown";
export const CONDITION_SIGNED_IN = "SignedIn";
export const CONDITION_ASSOCIATED = "Associated";
export const CONDITION_NODE_JOINED = "NodeJoined";
export const FINALIZER_NODE = "node.socialweb.computer/teardown";
export const FINALIZER_NODE_CLAIM = "nodeclaim.socialweb.computer/release";

export const CLUSTER_PHASES = ["Initializing", "Ready", "Unavailable", "Draining"] as const;
export type ClusterPhase = typeof CLUSTER_PHASES[number];

export const NODE_PHASES = ["Pending", "Running", "Draining"] as const;
export type NodePhase = typeof NODE_PHASES[number];

export const NODE_CLAIM_PHASES = ["Pending", "Bidding", "Provisioning", "Joining", "Running", "Failed", "Released", "Lost", "Adopting"] as const;
export type NodeClaimPhase = typeof NODE_CLAIM_PHASES[number];

export const CONTRACT_PHASES = ["requested", "rfp_submitted", "winner_selected", "accepted", "receipt", "network", "released"] as const;
export type ContractPhase = typeof CONTRACT_PHASES[number];

export const CONTRACT_OUTCOMES = ["no_bids", "bids_denied", "policy_rejected", "returned", "receipt_invalid", "network_timeout", "complete"] as const;
export type ContractOutcome = typeof CONTRACT_OUTCOMES[number];

export const CLUSTER_LINK_SERVICES = ["apiserver", "openbao"] as const;
export type ClusterLinkService = typeof CLUSTER_LINK_SERVICES[number];

export const CLUSTER_LINK_PHASES = ["Pending", "Listening", "Failed"] as const;
export type ClusterLinkPhase = typeof CLUSTER_LINK_PHASES[number];

export const IDENTITY_SCOPES = ["Account", "Cluster"] as const;
export type IdentityScope = typeof IDENTITY_SCOPES[number];

export const IDENTITY_STATES = ["Active", "Revoked", "RevocationPending", "ColdDeferred"] as const;
export type IdentityState = typeof IDENTITY_STATES[number];

export const MARKET_IDENTITY_MODES = ["oauthQr", "oauthBrowser", "pdsAccount", "localRepo", "oauthDelegated"] as const;
export type MarketIdentityMode = typeof MARKET_IDENTITY_MODES[number];

export const ASSOCIATION_MODES = ["RequireApproval", "Skip"] as const;
export type AssociationMode = typeof ASSOCIATION_MODES[number];

export const MARKET_IDENTITY_PHASES = ["Pending", "AwaitingScan", "AwaitingAuthorization", "AwaitingApproval", "Ready", "SessionExpired", "Failed"] as const;
export type MarketIdentityPhase = typeof MARKET_IDENTITY_PHASES[number];

export const PENDING_ACTION_KINDS = ["scanQr", "visitUrl", "approveAssociation", "signInAgain"] as const;
export type PendingActionKind = typeof PENDING_ACTION_KINDS[number];

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

export interface Cluster {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: ClusterSpec;
  status?: ClusterStatus;
}

export interface ClusterSpec {
  account: string;
  running?: boolean;
}

export interface ClusterStatus {
  phase?: ClusterPhase;
  URL?: string;
  instance?: string;
  nodeExpiry?: number;
  conditions?: Condition[];
}

export interface Node {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: NodeSpec;
  status?: NodeStatus;
}

export interface NodeSpec {
  cluster: string;
}

export interface NodeStatus {
  phase?: NodePhase;
  nodeExpiry?: number;
  conditions?: Condition[];
}

export interface NodeClaim {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: NodeClaimSpec;
  status?: NodeClaimStatus;
}

export interface NodeClaimSpec {
  cluster: string;
  nodeName: string;
  ownerDid: string;
  apiserverPort: number;
  advertiseAddress: string;
  tokenFile: string;
  nodePasswordFile: string;
  serverCAFile: string;
  denyBidderDids?: string[];
}

export interface NodeClaimStatus {
  phase?: NodeClaimPhase;
  contract?: NodeClaimContract;
  fqdn?: string;
  message?: string;
  grant?: NodeClaimGrant;
  secrets?: NodeClaimSecrets;
}

export interface NodeClaimContract {
  vmUri?: string;
  rfpUri?: string;
  acceptUri?: string;
  receiptUri?: string;
  receiptCid?: string;
  submitEventRef?: string;
  version?: number;
  phase?: ContractPhase;
  vmName?: string;
  requesterDid?: string;
  marketDid?: string;
  payload?: RecordRef;
  rfp?: RecordRef;
  policy?: RecordRef;
  bid?: RecordRef;
  winner?: ContractWinner;
  acceptPending?: ContractAcceptPending;
  accept?: RecordRef;
  receipt?: RecordRef;
  receiptOk?: boolean;
  vmAddress?: string;
  bids?: number;
  release?: ContractRelease;
  termination?: ContractTermination;
  hostKeys?: ContractHostKeys;
  outcome?: ContractOutcome;
  error?: string;
  timestamps?: ContractTimestamps;
}

export interface RecordRef {
  uri: string;
  cid: string;
}

export interface ContractWinner {
  did: string;
  submitAccept?: string;
}

export interface ContractAcceptPending {
  bidUri: string;
}

export interface ContractRelease {
  event: RecordRef;
  payload: RecordRef;
  ok: boolean;
  status?: number;
}

export interface ContractTermination {
  reason: string;
  exitCode?: number;
  observedAt: string;
  event: RecordRef;
  payload: RecordRef;
  request?: RecordRef;
}

export interface ContractHostKeys {
  keys: ContractHostKey[];
  observedAt: string;
  event: RecordRef;
  payload: RecordRef;
}

export interface ContractHostKey {
  type: string;
  publicKey: string;
  fingerprint: string;
}

export interface ContractTimestamps {
  requested?: string;
  rfp_submitted?: string;
  winner_selected?: string;
  accepted?: string;
  receipt?: string;
  network?: string;
  released?: string;
}

export interface NodeClaimGrant {
  issuer: string;
  subject: string;
  audience: string;
  role?: string;
}

export interface NodeClaimSecrets {
  ingressDid: string;
  keyRef: string;
  url: string;
  servedAt?: string;
}

export interface ClusterLink {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: ClusterLinkSpec;
  status?: ClusterLinkStatus;
}

export interface ClusterLinkSpec {
  cluster: string;
  service: ClusterLinkService;
  targetPort: number;
}

export interface ClusterLinkStatus {
  phase?: ClusterLinkPhase;
  endpointId?: string;
  did?: string;
  ticket?: string;
  message?: string;
}

export interface AtprotoIdentity {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: AtprotoIdentitySpec;
  status?: AtprotoIdentityStatus;
}

export interface AtprotoIdentitySpec {
  did: string;
  scope: IdentityScope;
  cluster?: string;
  revoked?: boolean;
}

export interface AtprotoIdentityStatus {
  state?: IdentityState;
  conditions?: Condition[];
}

export interface MarketIdentity {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: MarketIdentitySpec;
  status?: MarketIdentityStatus;
}

export interface MarketIdentitySpec {
  mode: MarketIdentityMode;
  handle?: string;
  pdsUrl?: string;
  association?: AssociationMode;
  associateWith?: string;
  approvedAssociations?: string[];
  rejectedAssociations?: string[];
  keysRef?: VaultRef;
  sessionRef?: VaultRef;
}

export interface VaultRef {
  mount: string;
  path: string;
}

export interface MarketIdentityStatus {
  phase?: MarketIdentityPhase;
  observedGeneration?: number;
  did?: string;
  handle?: string;
  pdsUrl?: string;
  serviceDid?: string;
  pendingAction?: MarketIdentityPendingAction;
  associations?: string[];
  sessionRef?: VaultRef;
  message?: string;
  conditions?: Condition[];
}

export interface MarketIdentityPendingAction {
  kind: PendingActionKind;
  url?: string;
  callerDid?: string;
  handle?: string;
  expiresAt?: string;
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

export const CLUSTER_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "Cluster",
  listKind: "ClusterList",
  plural: "clusters",
  singular: "cluster",
  schema: "v1alpha1-1.clusters.socialweb.computer",
  export: "clusters",
};

export const NODE_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "Node",
  listKind: "NodeList",
  plural: "nodes",
  singular: "node",
  schema: "v1alpha1-1.nodes.socialweb.computer",
  export: "clusters",
};

export const NODE_CLAIM_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "NodeClaim",
  listKind: "NodeClaimList",
  plural: "nodeclaims",
  singular: "nodeclaim",
  schema: "v1alpha1-5.nodeclaims.socialweb.computer",
  export: "clusters",
};

export const CLUSTER_LINK_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "ClusterLink",
  listKind: "ClusterLinkList",
  plural: "clusterlinks",
  singular: "clusterlink",
  schema: "v1alpha1-1.clusterlinks.socialweb.computer",
  export: "clusters",
};

export const ATPROTO_IDENTITY_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "AtprotoIdentity",
  listKind: "AtprotoIdentityList",
  plural: "atprotoidentities",
  singular: "atprotoidentity",
  schema: "v1alpha1-1.atprotoidentities.socialweb.computer",
  export: "atprotoidentities",
};

export const MARKET_IDENTITY_RESOURCE: ApiResource = {
  group: "socialweb.computer",
  version: "v1alpha1",
  kind: "MarketIdentity",
  listKind: "MarketIdentityList",
  plural: "marketidentities",
  singular: "marketidentity",
  schema: "v1alpha1-1.marketidentities.socialweb.computer",
  export: "marketidentities",
};

export const API_RESOURCES: readonly ApiResource[] = [CLUSTER_RESOURCE, NODE_RESOURCE, NODE_CLAIM_RESOURCE, CLUSTER_LINK_RESOURCE, ATPROTO_IDENTITY_RESOURCE, MARKET_IDENTITY_RESOURCE];

export type ApiTypeName =
  | "AssociationMode"
  | "AtprotoIdentity"
  | "AtprotoIdentitySpec"
  | "AtprotoIdentityStatus"
  | "Cluster"
  | "ClusterLink"
  | "ClusterLinkPhase"
  | "ClusterLinkService"
  | "ClusterLinkSpec"
  | "ClusterLinkStatus"
  | "ClusterPhase"
  | "ClusterSpec"
  | "ClusterStatus"
  | "Condition"
  | "ContractAcceptPending"
  | "ContractHostKey"
  | "ContractHostKeys"
  | "ContractOutcome"
  | "ContractPhase"
  | "ContractRelease"
  | "ContractTermination"
  | "ContractTimestamps"
  | "ContractWinner"
  | "IdentityScope"
  | "IdentityState"
  | "MarketIdentity"
  | "MarketIdentityMode"
  | "MarketIdentityPendingAction"
  | "MarketIdentityPhase"
  | "MarketIdentitySpec"
  | "MarketIdentityStatus"
  | "Node"
  | "NodeClaim"
  | "NodeClaimContract"
  | "NodeClaimGrant"
  | "NodeClaimPhase"
  | "NodeClaimSecrets"
  | "NodeClaimSpec"
  | "NodeClaimStatus"
  | "NodePhase"
  | "NodeSpec"
  | "NodeStatus"
  | "ObjectMeta"
  | "PendingActionKind"
  | "RecordRef"
  | "VaultRef";

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
  "AssociationMode": {
    "enum": [
      "RequireApproval",
      "Skip"
    ],
    "type": "string"
  },
  "AtprotoIdentity": {
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
        "ref": "AtprotoIdentitySpec"
      },
      "status": {
        "ref": "AtprotoIdentityStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "AtprotoIdentitySpec": {
    "properties": {
      "cluster": {
        "type": "string"
      },
      "did": {
        "type": "string"
      },
      "revoked": {
        "type": "boolean"
      },
      "scope": {
        "ref": "IdentityScope"
      }
    },
    "required": [
      "did",
      "scope"
    ],
    "type": "object"
  },
  "AtprotoIdentityStatus": {
    "properties": {
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "state": {
        "ref": "IdentityState"
      }
    },
    "required": [],
    "type": "object"
  },
  "Cluster": {
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
        "ref": "ClusterSpec"
      },
      "status": {
        "ref": "ClusterStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "ClusterLink": {
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
        "ref": "ClusterLinkSpec"
      },
      "status": {
        "ref": "ClusterLinkStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "ClusterLinkPhase": {
    "enum": [
      "Pending",
      "Listening",
      "Failed"
    ],
    "type": "string"
  },
  "ClusterLinkService": {
    "enum": [
      "apiserver",
      "openbao"
    ],
    "type": "string"
  },
  "ClusterLinkSpec": {
    "properties": {
      "cluster": {
        "type": "string"
      },
      "service": {
        "ref": "ClusterLinkService"
      },
      "targetPort": {
        "type": "integer"
      }
    },
    "required": [
      "cluster",
      "service",
      "targetPort"
    ],
    "type": "object"
  },
  "ClusterLinkStatus": {
    "properties": {
      "did": {
        "type": "string"
      },
      "endpointId": {
        "type": "string"
      },
      "message": {
        "type": "string"
      },
      "phase": {
        "ref": "ClusterLinkPhase"
      },
      "ticket": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ClusterPhase": {
    "enum": [
      "Initializing",
      "Ready",
      "Unavailable",
      "Draining"
    ],
    "type": "string"
  },
  "ClusterSpec": {
    "properties": {
      "account": {
        "type": "string"
      },
      "running": {
        "type": "boolean"
      }
    },
    "required": [
      "account"
    ],
    "type": "object"
  },
  "ClusterStatus": {
    "properties": {
      "URL": {
        "type": "string"
      },
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "instance": {
        "type": "string"
      },
      "nodeExpiry": {
        "type": "integer"
      },
      "phase": {
        "ref": "ClusterPhase"
      }
    },
    "required": [],
    "type": "object"
  },
  "Condition": {
    "open": true,
    "type": "object"
  },
  "ContractAcceptPending": {
    "properties": {
      "bidUri": {
        "type": "string"
      }
    },
    "required": [
      "bidUri"
    ],
    "type": "object"
  },
  "ContractHostKey": {
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
      "publicKey",
      "fingerprint"
    ],
    "type": "object"
  },
  "ContractHostKeys": {
    "properties": {
      "event": {
        "ref": "RecordRef"
      },
      "keys": {
        "items": {
          "ref": "ContractHostKey"
        },
        "type": "array"
      },
      "observedAt": {
        "type": "string"
      },
      "payload": {
        "ref": "RecordRef"
      }
    },
    "required": [
      "keys",
      "observedAt",
      "event",
      "payload"
    ],
    "type": "object"
  },
  "ContractOutcome": {
    "enum": [
      "no_bids",
      "bids_denied",
      "policy_rejected",
      "returned",
      "receipt_invalid",
      "network_timeout",
      "complete"
    ],
    "type": "string"
  },
  "ContractPhase": {
    "enum": [
      "requested",
      "rfp_submitted",
      "winner_selected",
      "accepted",
      "receipt",
      "network",
      "released"
    ],
    "type": "string"
  },
  "ContractRelease": {
    "properties": {
      "event": {
        "ref": "RecordRef"
      },
      "ok": {
        "type": "boolean"
      },
      "payload": {
        "ref": "RecordRef"
      },
      "status": {
        "type": "integer"
      }
    },
    "required": [
      "event",
      "payload",
      "ok"
    ],
    "type": "object"
  },
  "ContractTermination": {
    "properties": {
      "event": {
        "ref": "RecordRef"
      },
      "exitCode": {
        "type": "integer"
      },
      "observedAt": {
        "type": "string"
      },
      "payload": {
        "ref": "RecordRef"
      },
      "reason": {
        "type": "string"
      },
      "request": {
        "ref": "RecordRef"
      }
    },
    "required": [
      "reason",
      "observedAt",
      "event",
      "payload"
    ],
    "type": "object"
  },
  "ContractTimestamps": {
    "properties": {
      "accepted": {
        "type": "string"
      },
      "network": {
        "type": "string"
      },
      "receipt": {
        "type": "string"
      },
      "released": {
        "type": "string"
      },
      "requested": {
        "type": "string"
      },
      "rfp_submitted": {
        "type": "string"
      },
      "winner_selected": {
        "type": "string"
      }
    },
    "required": [],
    "type": "object"
  },
  "ContractWinner": {
    "properties": {
      "did": {
        "type": "string"
      },
      "submitAccept": {
        "type": "string"
      }
    },
    "required": [
      "did"
    ],
    "type": "object"
  },
  "IdentityScope": {
    "enum": [
      "Account",
      "Cluster"
    ],
    "type": "string"
  },
  "IdentityState": {
    "enum": [
      "Active",
      "Revoked",
      "RevocationPending",
      "ColdDeferred"
    ],
    "type": "string"
  },
  "MarketIdentity": {
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
        "ref": "MarketIdentitySpec"
      },
      "status": {
        "ref": "MarketIdentityStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "MarketIdentityMode": {
    "enum": [
      "oauthQr",
      "oauthBrowser",
      "pdsAccount",
      "localRepo",
      "oauthDelegated"
    ],
    "type": "string"
  },
  "MarketIdentityPendingAction": {
    "properties": {
      "callerDid": {
        "type": "string"
      },
      "expiresAt": {
        "type": "string"
      },
      "handle": {
        "type": "string"
      },
      "kind": {
        "ref": "PendingActionKind"
      },
      "url": {
        "type": "string"
      }
    },
    "required": [
      "kind"
    ],
    "type": "object"
  },
  "MarketIdentityPhase": {
    "enum": [
      "Pending",
      "AwaitingScan",
      "AwaitingAuthorization",
      "AwaitingApproval",
      "Ready",
      "SessionExpired",
      "Failed"
    ],
    "type": "string"
  },
  "MarketIdentitySpec": {
    "properties": {
      "approvedAssociations": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "associateWith": {
        "type": "string"
      },
      "association": {
        "ref": "AssociationMode"
      },
      "handle": {
        "type": "string"
      },
      "keysRef": {
        "ref": "VaultRef"
      },
      "mode": {
        "ref": "MarketIdentityMode"
      },
      "pdsUrl": {
        "type": "string"
      },
      "rejectedAssociations": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "sessionRef": {
        "ref": "VaultRef"
      }
    },
    "required": [
      "mode"
    ],
    "type": "object"
  },
  "MarketIdentityStatus": {
    "properties": {
      "associations": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "did": {
        "type": "string"
      },
      "handle": {
        "type": "string"
      },
      "message": {
        "type": "string"
      },
      "observedGeneration": {
        "type": "integer"
      },
      "pdsUrl": {
        "type": "string"
      },
      "pendingAction": {
        "ref": "MarketIdentityPendingAction"
      },
      "phase": {
        "ref": "MarketIdentityPhase"
      },
      "serviceDid": {
        "type": "string"
      },
      "sessionRef": {
        "ref": "VaultRef"
      }
    },
    "required": [],
    "type": "object"
  },
  "Node": {
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
        "ref": "NodeSpec"
      },
      "status": {
        "ref": "NodeStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "NodeClaim": {
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
        "ref": "NodeClaimSpec"
      },
      "status": {
        "ref": "NodeClaimStatus"
      }
    },
    "required": [],
    "type": "object"
  },
  "NodeClaimContract": {
    "properties": {
      "accept": {
        "ref": "RecordRef"
      },
      "acceptPending": {
        "ref": "ContractAcceptPending"
      },
      "acceptUri": {
        "type": "string"
      },
      "bid": {
        "ref": "RecordRef"
      },
      "bids": {
        "type": "integer"
      },
      "error": {
        "type": "string"
      },
      "hostKeys": {
        "ref": "ContractHostKeys"
      },
      "marketDid": {
        "type": "string"
      },
      "outcome": {
        "ref": "ContractOutcome"
      },
      "payload": {
        "ref": "RecordRef"
      },
      "phase": {
        "ref": "ContractPhase"
      },
      "policy": {
        "ref": "RecordRef"
      },
      "receipt": {
        "ref": "RecordRef"
      },
      "receiptCid": {
        "type": "string"
      },
      "receiptOk": {
        "type": "boolean"
      },
      "receiptUri": {
        "type": "string"
      },
      "release": {
        "ref": "ContractRelease"
      },
      "requesterDid": {
        "type": "string"
      },
      "rfp": {
        "ref": "RecordRef"
      },
      "rfpUri": {
        "type": "string"
      },
      "submitEventRef": {
        "type": "string"
      },
      "termination": {
        "ref": "ContractTermination"
      },
      "timestamps": {
        "ref": "ContractTimestamps"
      },
      "version": {
        "type": "integer"
      },
      "vmAddress": {
        "type": "string"
      },
      "vmName": {
        "type": "string"
      },
      "vmUri": {
        "type": "string"
      },
      "winner": {
        "ref": "ContractWinner"
      }
    },
    "required": [],
    "type": "object"
  },
  "NodeClaimGrant": {
    "properties": {
      "audience": {
        "type": "string"
      },
      "issuer": {
        "type": "string"
      },
      "role": {
        "type": "string"
      },
      "subject": {
        "type": "string"
      }
    },
    "required": [
      "issuer",
      "subject",
      "audience"
    ],
    "type": "object"
  },
  "NodeClaimPhase": {
    "enum": [
      "Pending",
      "Bidding",
      "Provisioning",
      "Joining",
      "Running",
      "Failed",
      "Released",
      "Lost",
      "Adopting"
    ],
    "type": "string"
  },
  "NodeClaimSecrets": {
    "properties": {
      "ingressDid": {
        "type": "string"
      },
      "keyRef": {
        "type": "string"
      },
      "servedAt": {
        "type": "string"
      },
      "url": {
        "type": "string"
      }
    },
    "required": [
      "ingressDid",
      "keyRef",
      "url"
    ],
    "type": "object"
  },
  "NodeClaimSpec": {
    "properties": {
      "advertiseAddress": {
        "type": "string"
      },
      "apiserverPort": {
        "type": "integer"
      },
      "cluster": {
        "type": "string"
      },
      "denyBidderDids": {
        "items": {
          "type": "string"
        },
        "type": "array"
      },
      "nodeName": {
        "type": "string"
      },
      "nodePasswordFile": {
        "type": "string"
      },
      "ownerDid": {
        "type": "string"
      },
      "serverCAFile": {
        "type": "string"
      },
      "tokenFile": {
        "type": "string"
      }
    },
    "required": [
      "cluster",
      "nodeName",
      "ownerDid",
      "apiserverPort",
      "advertiseAddress",
      "tokenFile",
      "nodePasswordFile",
      "serverCAFile"
    ],
    "type": "object"
  },
  "NodeClaimStatus": {
    "properties": {
      "contract": {
        "ref": "NodeClaimContract"
      },
      "fqdn": {
        "type": "string"
      },
      "grant": {
        "ref": "NodeClaimGrant"
      },
      "message": {
        "type": "string"
      },
      "phase": {
        "ref": "NodeClaimPhase"
      },
      "secrets": {
        "ref": "NodeClaimSecrets"
      }
    },
    "required": [],
    "type": "object"
  },
  "NodePhase": {
    "enum": [
      "Pending",
      "Running",
      "Draining"
    ],
    "type": "string"
  },
  "NodeSpec": {
    "properties": {
      "cluster": {
        "type": "string"
      }
    },
    "required": [
      "cluster"
    ],
    "type": "object"
  },
  "NodeStatus": {
    "properties": {
      "conditions": {
        "items": {
          "ref": "Condition"
        },
        "type": "array"
      },
      "nodeExpiry": {
        "type": "integer"
      },
      "phase": {
        "ref": "NodePhase"
      }
    },
    "required": [],
    "type": "object"
  },
  "ObjectMeta": {
    "open": true,
    "type": "object"
  },
  "PendingActionKind": {
    "enum": [
      "scanQr",
      "visitUrl",
      "approveAssociation",
      "signInAgain"
    ],
    "type": "string"
  },
  "RecordRef": {
    "properties": {
      "cid": {
        "type": "string"
      },
      "uri": {
        "type": "string"
      }
    },
    "required": [
      "uri",
      "cid"
    ],
    "type": "object"
  },
  "VaultRef": {
    "properties": {
      "mount": {
        "type": "string"
      },
      "path": {
        "type": "string"
      }
    },
    "required": [
      "mount",
      "path"
    ],
    "type": "object"
  }
};

export function validateCluster(value: unknown): ApiIssue[] {
  return validate("Cluster", value);
}

export function isCluster(value: unknown): value is Cluster {
  return validate("Cluster", value).length === 0;
}

export function assertCluster(value: unknown): asserts value is Cluster {
  const issues = validate("Cluster", value);
  if (issues.length > 0) throw new ApiValidationError("Cluster", issues);
}

export function parseCluster(value: unknown): Cluster {
  assertCluster(value);
  return value;
}

export function validateClusterSpec(value: unknown): ApiIssue[] {
  return validate("ClusterSpec", value);
}

export function isClusterSpec(value: unknown): value is ClusterSpec {
  return validate("ClusterSpec", value).length === 0;
}

export function assertClusterSpec(value: unknown): asserts value is ClusterSpec {
  const issues = validate("ClusterSpec", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterSpec", issues);
}

export function parseClusterSpec(value: unknown): ClusterSpec {
  assertClusterSpec(value);
  return value;
}

export function validateClusterStatus(value: unknown): ApiIssue[] {
  return validate("ClusterStatus", value);
}

export function isClusterStatus(value: unknown): value is ClusterStatus {
  return validate("ClusterStatus", value).length === 0;
}

export function assertClusterStatus(value: unknown): asserts value is ClusterStatus {
  const issues = validate("ClusterStatus", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterStatus", issues);
}

export function parseClusterStatus(value: unknown): ClusterStatus {
  assertClusterStatus(value);
  return value;
}

export function validateClusterPhase(value: unknown): ApiIssue[] {
  return validate("ClusterPhase", value);
}

export function isClusterPhase(value: unknown): value is ClusterPhase {
  return validate("ClusterPhase", value).length === 0;
}

export function assertClusterPhase(value: unknown): asserts value is ClusterPhase {
  const issues = validate("ClusterPhase", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterPhase", issues);
}

export function parseClusterPhase(value: unknown): ClusterPhase {
  assertClusterPhase(value);
  return value;
}

export function validateNode(value: unknown): ApiIssue[] {
  return validate("Node", value);
}

export function isNode(value: unknown): value is Node {
  return validate("Node", value).length === 0;
}

export function assertNode(value: unknown): asserts value is Node {
  const issues = validate("Node", value);
  if (issues.length > 0) throw new ApiValidationError("Node", issues);
}

export function parseNode(value: unknown): Node {
  assertNode(value);
  return value;
}

export function validateNodeSpec(value: unknown): ApiIssue[] {
  return validate("NodeSpec", value);
}

export function isNodeSpec(value: unknown): value is NodeSpec {
  return validate("NodeSpec", value).length === 0;
}

export function assertNodeSpec(value: unknown): asserts value is NodeSpec {
  const issues = validate("NodeSpec", value);
  if (issues.length > 0) throw new ApiValidationError("NodeSpec", issues);
}

export function parseNodeSpec(value: unknown): NodeSpec {
  assertNodeSpec(value);
  return value;
}

export function validateNodeStatus(value: unknown): ApiIssue[] {
  return validate("NodeStatus", value);
}

export function isNodeStatus(value: unknown): value is NodeStatus {
  return validate("NodeStatus", value).length === 0;
}

export function assertNodeStatus(value: unknown): asserts value is NodeStatus {
  const issues = validate("NodeStatus", value);
  if (issues.length > 0) throw new ApiValidationError("NodeStatus", issues);
}

export function parseNodeStatus(value: unknown): NodeStatus {
  assertNodeStatus(value);
  return value;
}

export function validateNodePhase(value: unknown): ApiIssue[] {
  return validate("NodePhase", value);
}

export function isNodePhase(value: unknown): value is NodePhase {
  return validate("NodePhase", value).length === 0;
}

export function assertNodePhase(value: unknown): asserts value is NodePhase {
  const issues = validate("NodePhase", value);
  if (issues.length > 0) throw new ApiValidationError("NodePhase", issues);
}

export function parseNodePhase(value: unknown): NodePhase {
  assertNodePhase(value);
  return value;
}

export function validateNodeClaim(value: unknown): ApiIssue[] {
  return validate("NodeClaim", value);
}

export function isNodeClaim(value: unknown): value is NodeClaim {
  return validate("NodeClaim", value).length === 0;
}

export function assertNodeClaim(value: unknown): asserts value is NodeClaim {
  const issues = validate("NodeClaim", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaim", issues);
}

export function parseNodeClaim(value: unknown): NodeClaim {
  assertNodeClaim(value);
  return value;
}

export function validateNodeClaimSpec(value: unknown): ApiIssue[] {
  return validate("NodeClaimSpec", value);
}

export function isNodeClaimSpec(value: unknown): value is NodeClaimSpec {
  return validate("NodeClaimSpec", value).length === 0;
}

export function assertNodeClaimSpec(value: unknown): asserts value is NodeClaimSpec {
  const issues = validate("NodeClaimSpec", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimSpec", issues);
}

export function parseNodeClaimSpec(value: unknown): NodeClaimSpec {
  assertNodeClaimSpec(value);
  return value;
}

export function validateNodeClaimStatus(value: unknown): ApiIssue[] {
  return validate("NodeClaimStatus", value);
}

export function isNodeClaimStatus(value: unknown): value is NodeClaimStatus {
  return validate("NodeClaimStatus", value).length === 0;
}

export function assertNodeClaimStatus(value: unknown): asserts value is NodeClaimStatus {
  const issues = validate("NodeClaimStatus", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimStatus", issues);
}

export function parseNodeClaimStatus(value: unknown): NodeClaimStatus {
  assertNodeClaimStatus(value);
  return value;
}

export function validateNodeClaimPhase(value: unknown): ApiIssue[] {
  return validate("NodeClaimPhase", value);
}

export function isNodeClaimPhase(value: unknown): value is NodeClaimPhase {
  return validate("NodeClaimPhase", value).length === 0;
}

export function assertNodeClaimPhase(value: unknown): asserts value is NodeClaimPhase {
  const issues = validate("NodeClaimPhase", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimPhase", issues);
}

export function parseNodeClaimPhase(value: unknown): NodeClaimPhase {
  assertNodeClaimPhase(value);
  return value;
}

export function validateNodeClaimContract(value: unknown): ApiIssue[] {
  return validate("NodeClaimContract", value);
}

export function isNodeClaimContract(value: unknown): value is NodeClaimContract {
  return validate("NodeClaimContract", value).length === 0;
}

export function assertNodeClaimContract(value: unknown): asserts value is NodeClaimContract {
  const issues = validate("NodeClaimContract", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimContract", issues);
}

export function parseNodeClaimContract(value: unknown): NodeClaimContract {
  assertNodeClaimContract(value);
  return value;
}

export function validateContractPhase(value: unknown): ApiIssue[] {
  return validate("ContractPhase", value);
}

export function isContractPhase(value: unknown): value is ContractPhase {
  return validate("ContractPhase", value).length === 0;
}

export function assertContractPhase(value: unknown): asserts value is ContractPhase {
  const issues = validate("ContractPhase", value);
  if (issues.length > 0) throw new ApiValidationError("ContractPhase", issues);
}

export function parseContractPhase(value: unknown): ContractPhase {
  assertContractPhase(value);
  return value;
}

export function validateRecordRef(value: unknown): ApiIssue[] {
  return validate("RecordRef", value);
}

export function isRecordRef(value: unknown): value is RecordRef {
  return validate("RecordRef", value).length === 0;
}

export function assertRecordRef(value: unknown): asserts value is RecordRef {
  const issues = validate("RecordRef", value);
  if (issues.length > 0) throw new ApiValidationError("RecordRef", issues);
}

export function parseRecordRef(value: unknown): RecordRef {
  assertRecordRef(value);
  return value;
}

export function validateContractWinner(value: unknown): ApiIssue[] {
  return validate("ContractWinner", value);
}

export function isContractWinner(value: unknown): value is ContractWinner {
  return validate("ContractWinner", value).length === 0;
}

export function assertContractWinner(value: unknown): asserts value is ContractWinner {
  const issues = validate("ContractWinner", value);
  if (issues.length > 0) throw new ApiValidationError("ContractWinner", issues);
}

export function parseContractWinner(value: unknown): ContractWinner {
  assertContractWinner(value);
  return value;
}

export function validateContractAcceptPending(value: unknown): ApiIssue[] {
  return validate("ContractAcceptPending", value);
}

export function isContractAcceptPending(value: unknown): value is ContractAcceptPending {
  return validate("ContractAcceptPending", value).length === 0;
}

export function assertContractAcceptPending(value: unknown): asserts value is ContractAcceptPending {
  const issues = validate("ContractAcceptPending", value);
  if (issues.length > 0) throw new ApiValidationError("ContractAcceptPending", issues);
}

export function parseContractAcceptPending(value: unknown): ContractAcceptPending {
  assertContractAcceptPending(value);
  return value;
}

export function validateContractRelease(value: unknown): ApiIssue[] {
  return validate("ContractRelease", value);
}

export function isContractRelease(value: unknown): value is ContractRelease {
  return validate("ContractRelease", value).length === 0;
}

export function assertContractRelease(value: unknown): asserts value is ContractRelease {
  const issues = validate("ContractRelease", value);
  if (issues.length > 0) throw new ApiValidationError("ContractRelease", issues);
}

export function parseContractRelease(value: unknown): ContractRelease {
  assertContractRelease(value);
  return value;
}

export function validateContractTermination(value: unknown): ApiIssue[] {
  return validate("ContractTermination", value);
}

export function isContractTermination(value: unknown): value is ContractTermination {
  return validate("ContractTermination", value).length === 0;
}

export function assertContractTermination(value: unknown): asserts value is ContractTermination {
  const issues = validate("ContractTermination", value);
  if (issues.length > 0) throw new ApiValidationError("ContractTermination", issues);
}

export function parseContractTermination(value: unknown): ContractTermination {
  assertContractTermination(value);
  return value;
}

export function validateContractHostKeys(value: unknown): ApiIssue[] {
  return validate("ContractHostKeys", value);
}

export function isContractHostKeys(value: unknown): value is ContractHostKeys {
  return validate("ContractHostKeys", value).length === 0;
}

export function assertContractHostKeys(value: unknown): asserts value is ContractHostKeys {
  const issues = validate("ContractHostKeys", value);
  if (issues.length > 0) throw new ApiValidationError("ContractHostKeys", issues);
}

export function parseContractHostKeys(value: unknown): ContractHostKeys {
  assertContractHostKeys(value);
  return value;
}

export function validateContractHostKey(value: unknown): ApiIssue[] {
  return validate("ContractHostKey", value);
}

export function isContractHostKey(value: unknown): value is ContractHostKey {
  return validate("ContractHostKey", value).length === 0;
}

export function assertContractHostKey(value: unknown): asserts value is ContractHostKey {
  const issues = validate("ContractHostKey", value);
  if (issues.length > 0) throw new ApiValidationError("ContractHostKey", issues);
}

export function parseContractHostKey(value: unknown): ContractHostKey {
  assertContractHostKey(value);
  return value;
}

export function validateContractOutcome(value: unknown): ApiIssue[] {
  return validate("ContractOutcome", value);
}

export function isContractOutcome(value: unknown): value is ContractOutcome {
  return validate("ContractOutcome", value).length === 0;
}

export function assertContractOutcome(value: unknown): asserts value is ContractOutcome {
  const issues = validate("ContractOutcome", value);
  if (issues.length > 0) throw new ApiValidationError("ContractOutcome", issues);
}

export function parseContractOutcome(value: unknown): ContractOutcome {
  assertContractOutcome(value);
  return value;
}

export function validateContractTimestamps(value: unknown): ApiIssue[] {
  return validate("ContractTimestamps", value);
}

export function isContractTimestamps(value: unknown): value is ContractTimestamps {
  return validate("ContractTimestamps", value).length === 0;
}

export function assertContractTimestamps(value: unknown): asserts value is ContractTimestamps {
  const issues = validate("ContractTimestamps", value);
  if (issues.length > 0) throw new ApiValidationError("ContractTimestamps", issues);
}

export function parseContractTimestamps(value: unknown): ContractTimestamps {
  assertContractTimestamps(value);
  return value;
}

export function validateNodeClaimGrant(value: unknown): ApiIssue[] {
  return validate("NodeClaimGrant", value);
}

export function isNodeClaimGrant(value: unknown): value is NodeClaimGrant {
  return validate("NodeClaimGrant", value).length === 0;
}

export function assertNodeClaimGrant(value: unknown): asserts value is NodeClaimGrant {
  const issues = validate("NodeClaimGrant", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimGrant", issues);
}

export function parseNodeClaimGrant(value: unknown): NodeClaimGrant {
  assertNodeClaimGrant(value);
  return value;
}

export function validateNodeClaimSecrets(value: unknown): ApiIssue[] {
  return validate("NodeClaimSecrets", value);
}

export function isNodeClaimSecrets(value: unknown): value is NodeClaimSecrets {
  return validate("NodeClaimSecrets", value).length === 0;
}

export function assertNodeClaimSecrets(value: unknown): asserts value is NodeClaimSecrets {
  const issues = validate("NodeClaimSecrets", value);
  if (issues.length > 0) throw new ApiValidationError("NodeClaimSecrets", issues);
}

export function parseNodeClaimSecrets(value: unknown): NodeClaimSecrets {
  assertNodeClaimSecrets(value);
  return value;
}

export function validateClusterLink(value: unknown): ApiIssue[] {
  return validate("ClusterLink", value);
}

export function isClusterLink(value: unknown): value is ClusterLink {
  return validate("ClusterLink", value).length === 0;
}

export function assertClusterLink(value: unknown): asserts value is ClusterLink {
  const issues = validate("ClusterLink", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterLink", issues);
}

export function parseClusterLink(value: unknown): ClusterLink {
  assertClusterLink(value);
  return value;
}

export function validateClusterLinkSpec(value: unknown): ApiIssue[] {
  return validate("ClusterLinkSpec", value);
}

export function isClusterLinkSpec(value: unknown): value is ClusterLinkSpec {
  return validate("ClusterLinkSpec", value).length === 0;
}

export function assertClusterLinkSpec(value: unknown): asserts value is ClusterLinkSpec {
  const issues = validate("ClusterLinkSpec", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterLinkSpec", issues);
}

export function parseClusterLinkSpec(value: unknown): ClusterLinkSpec {
  assertClusterLinkSpec(value);
  return value;
}

export function validateClusterLinkService(value: unknown): ApiIssue[] {
  return validate("ClusterLinkService", value);
}

export function isClusterLinkService(value: unknown): value is ClusterLinkService {
  return validate("ClusterLinkService", value).length === 0;
}

export function assertClusterLinkService(value: unknown): asserts value is ClusterLinkService {
  const issues = validate("ClusterLinkService", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterLinkService", issues);
}

export function parseClusterLinkService(value: unknown): ClusterLinkService {
  assertClusterLinkService(value);
  return value;
}

export function validateClusterLinkStatus(value: unknown): ApiIssue[] {
  return validate("ClusterLinkStatus", value);
}

export function isClusterLinkStatus(value: unknown): value is ClusterLinkStatus {
  return validate("ClusterLinkStatus", value).length === 0;
}

export function assertClusterLinkStatus(value: unknown): asserts value is ClusterLinkStatus {
  const issues = validate("ClusterLinkStatus", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterLinkStatus", issues);
}

export function parseClusterLinkStatus(value: unknown): ClusterLinkStatus {
  assertClusterLinkStatus(value);
  return value;
}

export function validateClusterLinkPhase(value: unknown): ApiIssue[] {
  return validate("ClusterLinkPhase", value);
}

export function isClusterLinkPhase(value: unknown): value is ClusterLinkPhase {
  return validate("ClusterLinkPhase", value).length === 0;
}

export function assertClusterLinkPhase(value: unknown): asserts value is ClusterLinkPhase {
  const issues = validate("ClusterLinkPhase", value);
  if (issues.length > 0) throw new ApiValidationError("ClusterLinkPhase", issues);
}

export function parseClusterLinkPhase(value: unknown): ClusterLinkPhase {
  assertClusterLinkPhase(value);
  return value;
}

export function validateAtprotoIdentity(value: unknown): ApiIssue[] {
  return validate("AtprotoIdentity", value);
}

export function isAtprotoIdentity(value: unknown): value is AtprotoIdentity {
  return validate("AtprotoIdentity", value).length === 0;
}

export function assertAtprotoIdentity(value: unknown): asserts value is AtprotoIdentity {
  const issues = validate("AtprotoIdentity", value);
  if (issues.length > 0) throw new ApiValidationError("AtprotoIdentity", issues);
}

export function parseAtprotoIdentity(value: unknown): AtprotoIdentity {
  assertAtprotoIdentity(value);
  return value;
}

export function validateAtprotoIdentitySpec(value: unknown): ApiIssue[] {
  return validate("AtprotoIdentitySpec", value);
}

export function isAtprotoIdentitySpec(value: unknown): value is AtprotoIdentitySpec {
  return validate("AtprotoIdentitySpec", value).length === 0;
}

export function assertAtprotoIdentitySpec(value: unknown): asserts value is AtprotoIdentitySpec {
  const issues = validate("AtprotoIdentitySpec", value);
  if (issues.length > 0) throw new ApiValidationError("AtprotoIdentitySpec", issues);
}

export function parseAtprotoIdentitySpec(value: unknown): AtprotoIdentitySpec {
  assertAtprotoIdentitySpec(value);
  return value;
}

export function validateIdentityScope(value: unknown): ApiIssue[] {
  return validate("IdentityScope", value);
}

export function isIdentityScope(value: unknown): value is IdentityScope {
  return validate("IdentityScope", value).length === 0;
}

export function assertIdentityScope(value: unknown): asserts value is IdentityScope {
  const issues = validate("IdentityScope", value);
  if (issues.length > 0) throw new ApiValidationError("IdentityScope", issues);
}

export function parseIdentityScope(value: unknown): IdentityScope {
  assertIdentityScope(value);
  return value;
}

export function validateAtprotoIdentityStatus(value: unknown): ApiIssue[] {
  return validate("AtprotoIdentityStatus", value);
}

export function isAtprotoIdentityStatus(value: unknown): value is AtprotoIdentityStatus {
  return validate("AtprotoIdentityStatus", value).length === 0;
}

export function assertAtprotoIdentityStatus(value: unknown): asserts value is AtprotoIdentityStatus {
  const issues = validate("AtprotoIdentityStatus", value);
  if (issues.length > 0) throw new ApiValidationError("AtprotoIdentityStatus", issues);
}

export function parseAtprotoIdentityStatus(value: unknown): AtprotoIdentityStatus {
  assertAtprotoIdentityStatus(value);
  return value;
}

export function validateIdentityState(value: unknown): ApiIssue[] {
  return validate("IdentityState", value);
}

export function isIdentityState(value: unknown): value is IdentityState {
  return validate("IdentityState", value).length === 0;
}

export function assertIdentityState(value: unknown): asserts value is IdentityState {
  const issues = validate("IdentityState", value);
  if (issues.length > 0) throw new ApiValidationError("IdentityState", issues);
}

export function parseIdentityState(value: unknown): IdentityState {
  assertIdentityState(value);
  return value;
}

export function validateMarketIdentity(value: unknown): ApiIssue[] {
  return validate("MarketIdentity", value);
}

export function isMarketIdentity(value: unknown): value is MarketIdentity {
  return validate("MarketIdentity", value).length === 0;
}

export function assertMarketIdentity(value: unknown): asserts value is MarketIdentity {
  const issues = validate("MarketIdentity", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentity", issues);
}

export function parseMarketIdentity(value: unknown): MarketIdentity {
  assertMarketIdentity(value);
  return value;
}

export function validateMarketIdentitySpec(value: unknown): ApiIssue[] {
  return validate("MarketIdentitySpec", value);
}

export function isMarketIdentitySpec(value: unknown): value is MarketIdentitySpec {
  return validate("MarketIdentitySpec", value).length === 0;
}

export function assertMarketIdentitySpec(value: unknown): asserts value is MarketIdentitySpec {
  const issues = validate("MarketIdentitySpec", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentitySpec", issues);
}

export function parseMarketIdentitySpec(value: unknown): MarketIdentitySpec {
  assertMarketIdentitySpec(value);
  return value;
}

export function validateMarketIdentityMode(value: unknown): ApiIssue[] {
  return validate("MarketIdentityMode", value);
}

export function isMarketIdentityMode(value: unknown): value is MarketIdentityMode {
  return validate("MarketIdentityMode", value).length === 0;
}

export function assertMarketIdentityMode(value: unknown): asserts value is MarketIdentityMode {
  const issues = validate("MarketIdentityMode", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentityMode", issues);
}

export function parseMarketIdentityMode(value: unknown): MarketIdentityMode {
  assertMarketIdentityMode(value);
  return value;
}

export function validateAssociationMode(value: unknown): ApiIssue[] {
  return validate("AssociationMode", value);
}

export function isAssociationMode(value: unknown): value is AssociationMode {
  return validate("AssociationMode", value).length === 0;
}

export function assertAssociationMode(value: unknown): asserts value is AssociationMode {
  const issues = validate("AssociationMode", value);
  if (issues.length > 0) throw new ApiValidationError("AssociationMode", issues);
}

export function parseAssociationMode(value: unknown): AssociationMode {
  assertAssociationMode(value);
  return value;
}

export function validateVaultRef(value: unknown): ApiIssue[] {
  return validate("VaultRef", value);
}

export function isVaultRef(value: unknown): value is VaultRef {
  return validate("VaultRef", value).length === 0;
}

export function assertVaultRef(value: unknown): asserts value is VaultRef {
  const issues = validate("VaultRef", value);
  if (issues.length > 0) throw new ApiValidationError("VaultRef", issues);
}

export function parseVaultRef(value: unknown): VaultRef {
  assertVaultRef(value);
  return value;
}

export function validateMarketIdentityStatus(value: unknown): ApiIssue[] {
  return validate("MarketIdentityStatus", value);
}

export function isMarketIdentityStatus(value: unknown): value is MarketIdentityStatus {
  return validate("MarketIdentityStatus", value).length === 0;
}

export function assertMarketIdentityStatus(value: unknown): asserts value is MarketIdentityStatus {
  const issues = validate("MarketIdentityStatus", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentityStatus", issues);
}

export function parseMarketIdentityStatus(value: unknown): MarketIdentityStatus {
  assertMarketIdentityStatus(value);
  return value;
}

export function validateMarketIdentityPhase(value: unknown): ApiIssue[] {
  return validate("MarketIdentityPhase", value);
}

export function isMarketIdentityPhase(value: unknown): value is MarketIdentityPhase {
  return validate("MarketIdentityPhase", value).length === 0;
}

export function assertMarketIdentityPhase(value: unknown): asserts value is MarketIdentityPhase {
  const issues = validate("MarketIdentityPhase", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentityPhase", issues);
}

export function parseMarketIdentityPhase(value: unknown): MarketIdentityPhase {
  assertMarketIdentityPhase(value);
  return value;
}

export function validateMarketIdentityPendingAction(value: unknown): ApiIssue[] {
  return validate("MarketIdentityPendingAction", value);
}

export function isMarketIdentityPendingAction(value: unknown): value is MarketIdentityPendingAction {
  return validate("MarketIdentityPendingAction", value).length === 0;
}

export function assertMarketIdentityPendingAction(value: unknown): asserts value is MarketIdentityPendingAction {
  const issues = validate("MarketIdentityPendingAction", value);
  if (issues.length > 0) throw new ApiValidationError("MarketIdentityPendingAction", issues);
}

export function parseMarketIdentityPendingAction(value: unknown): MarketIdentityPendingAction {
  assertMarketIdentityPendingAction(value);
  return value;
}

export function validatePendingActionKind(value: unknown): ApiIssue[] {
  return validate("PendingActionKind", value);
}

export function isPendingActionKind(value: unknown): value is PendingActionKind {
  return validate("PendingActionKind", value).length === 0;
}

export function assertPendingActionKind(value: unknown): asserts value is PendingActionKind {
  const issues = validate("PendingActionKind", value);
  if (issues.length > 0) throw new ApiValidationError("PendingActionKind", issues);
}

export function parsePendingActionKind(value: unknown): PendingActionKind {
  assertPendingActionKind(value);
  return value;
}
