// Pure interfaces for the market registry — registration storage and health
// checking. Zero I/O. No timers, no fetch, no crypto, no Deno.*.
//
// The RegistrationStore interface defines the contract for storing and querying
// bidder registrations. The HealthChecker interface defines the contract for
// periodically verifying bidder liveness. Concrete implementations live in
// lib/market-registry-atproto/.

export interface IndexEntry {
  bidderDid: string;
  appliesTo: string[];
  indexedAt: string;
}

export interface BidderDiscovery {
  endpointUrl: string;
  appliesTo: string[];
  updatedAt: string;
  createdAt: string;
}

export interface RegistrationStore {
  register(params: {
    bidderDid: string;
    appliesTo: string[];
  }): Promise<{ uri: string; cid: string }>;

  listBidders(params: {
    payloadNsid?: string;
    maxResults?: number;
    cursor?: string;
  }): Promise<{ bidders: IndexEntry[]; cursor?: string }>;

  removeBidder(bidderDid: string): Promise<void>;

  getAll(): Promise<Array<IndexEntry & { uri: string; rkey: string }>>;

  /**
   * Fetch a bidder's discovery record from their PDS. Returns null if not found
   * or if the record's updatedAt is older than staleThresholdMs (default 5 min).
   */
  fetchDiscovery(
    bidderDid: string,
    staleThresholdMs?: number,
  ): Promise<BidderDiscovery | null>;
}

export interface HealthCheckerOptions {
  intervalMs?: number;
  staleThresholdMs?: number;
}

export interface HealthChecker {
  start(): void;
  stop(): void;
}
