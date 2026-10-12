import type {
  ContractEvent,
  ContractStore,
  StoredContract,
  StoredContracts,
} from "@publicdomainrelay/market-bidder-abc";
import type { KcpComputeVMClient } from "@publicdomainrelay/compute-provider-kcp";
import { acceptKeyOf, type ComputeVMAccepts } from "@publicdomainrelay/compute-provider-kcp-abc";
import type { ComputeVM, ComputeVMContract } from "@publicdomainrelay/compute-provider-kcp-common";

export const DEFAULT_ACCEPT_APPLY_WAIT_MS = 60_000;
export const KCP_PROVIDER = "kcp";

export interface KcpContractStoreLogger {
  info(message: string, data?: Record<string, unknown>): void;
  warn(message: string, data?: Record<string, unknown>): void;
}

export interface KcpContractStoreOptions {
  client: KcpComputeVMClient;
  handle: string;
  accepts: ComputeVMAccepts;
  tombstones: Pick<ContractStore, "load" | "saveTombstone">;
  logger: KcpContractStoreLogger;
  acceptWaitMs?: number;
}

export function computeVMContractOf(event: ContractEvent): ComputeVMContract {
  const c: ComputeVMContract = { receiptUri: event.receiptUri, receiptCid: event.receiptCid, acceptedAt: event.acceptedAt };
  if (event.acceptUri) c.acceptUri = event.acceptUri;
  if (event.acceptCid) c.acceptCid = event.acceptCid;
  if (event.submitEventUrl) c.submitEventUrl = event.submitEventUrl;
  return c;
}

export function storedContractOfComputeVM(vm: ComputeVM): StoredContract | undefined {
  const name = vm.metadata?.name;
  const c = vm.spec?.contract;
  const requester = vm.spec?.requester?.did;
  if (!name || !c?.receiptUri || !c.receiptCid || !requester) return undefined;
  const stored: StoredContract = {
    key: `${c.receiptUri}#${c.receiptCid}`,
    receiptUri: c.receiptUri,
    receiptCid: c.receiptCid,
    acceptAuthor: requester,
    acceptedAt: c.acceptedAt || vm.metadata?.creationTimestamp || new Date(0).toISOString(),
    providerId: name,
    provider: KCP_PROVIDER,
  };
  if (c.acceptUri) stored.acceptUri = c.acceptUri;
  if (c.acceptCid) stored.acceptCid = c.acceptCid;
  if (c.submitEventUrl) stored.submitEventUrl = c.submitEventUrl;
  return stored;
}

export function createKcpContractStore(opts: KcpContractStoreOptions): ContractStore {
  const { client, handle, accepts, logger } = opts;
  const waitMs = opts.acceptWaitMs ?? DEFAULT_ACCEPT_APPLY_WAIT_MS;
  const owned = (vm: ComputeVM) => vm.spec?.owner?.handle === handle && !!vm.metadata?.name;

  async function write(name: string, event: ContractEvent): Promise<boolean> {
    const applied = await client.applyContract(name, computeVMContractOf(event));
    if (!applied) {
      logger.warn("bidder_contract_computevm_gone", { name, receiptUri: event.receiptUri, type: event.type });
      return false;
    }
    logger.info("bidder_contract_on_computevm", { name, receiptUri: event.receiptUri, type: event.type });
    return true;
  }

  async function nameOfReceipt(receiptUri: string): Promise<string | undefined> {
    const listed = await client.list();
    return listed.items.find((vm) => owned(vm) && vm.spec?.contract?.receiptUri === receiptUri)?.metadata?.name;
  }

  async function release(name: string, why: string, data: Record<string, unknown>): Promise<void> {
    if (await client.delete(name)) logger.info("computevm_released_by_contract_store", { name, why, ...data });
  }

  async function load(): Promise<StoredContracts> {
    const { tombstones } = await opts.tombstones.load();
    const dead = new Set(tombstones.map((t) => t.receiptUri));
    const listed = await client.list();
    const contracts: StoredContract[] = [];
    for (const vm of listed.items) {
      if (!owned(vm)) continue;
      const name = vm.metadata!.name!;
      const deleting = vm.metadata?.deletionTimestamp !== undefined;
      const stored = storedContractOfComputeVM(vm);
      if (!stored) {
        if (!deleting) await release(name, "no contract names it: the bidder stopped between creating it and recording its receipt", {});
        continue;
      }
      if (dead.has(stored.receiptUri)) {
        if (!deleting) await release(name, "its contract is already tombstoned", { receiptUri: stored.receiptUri });
        continue;
      }
      contracts.push(stored);
    }
    contracts.sort((a, b) => a.acceptedAt.localeCompare(b.acceptedAt));
    logger.info("bidder_contracts_from_computevms", { contracts: contracts.length, tombstones: tombstones.length, handle });
    return { contracts, tombstones };
  }

  async function save(event: ContractEvent): Promise<void> {
    if (event.provider !== undefined && event.provider !== KCP_PROVIDER) return;
    switch (event.type) {
      case "accepted": {
        const key = acceptKeyOf(event.acceptUri && event.acceptCid ? { uri: event.acceptUri, cid: event.acceptCid } : undefined);
        if (!key) return;
        const name = await accepts.wait(key, AbortSignal.timeout(waitMs));
        accepts.forget(key);
        if (!name) {
          logger.warn("bidder_contract_without_computevm", { receiptUri: event.receiptUri, acceptUri: event.acceptUri, waitMs });
          return;
        }
        await write(name, event);
        return;
      }
      case "provisioned": {
        if (event.providerId !== undefined) await write(String(event.providerId), event);
        return;
      }
      case "terminated": {
        const name = event.providerId !== undefined ? String(event.providerId) : await nameOfReceipt(event.receiptUri);
        if (name) await release(name, "its contract ended and its tombstone is kept", { receiptUri: event.receiptUri });
        return;
      }
    }
  }

  return { load, save, saveTombstone: (t) => opts.tombstones.saveTombstone(t) };
}
