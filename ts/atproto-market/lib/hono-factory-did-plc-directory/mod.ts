export {
  createPlcDirectoryFactory,
} from "./factory.ts";
export type {
  PlcDirectoryOptions,
  PlcDirectoryFactory,
  PlcStore,
} from "./factory.ts";

export { MemoryPlcStore } from "./storage/plc-store.ts";

export {
  validateOperationStructure,
  verifyOperationSignature,
  validatePrevChain,
  validateRotationKeyAuth,
  computeOperationCid,
} from "./validation.ts";

export { resolveDidDocument } from "./did-resolution.ts";
