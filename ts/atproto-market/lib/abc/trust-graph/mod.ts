export interface VouchResolver {
  getVouchedDids(did: string): Promise<Set<string>>;
  isVouched(voucher: string, vouchee: string): Promise<boolean>;
}

export interface OperatorDiscovery {
  discoverOperatorDids(atprotoDid: string): Promise<string[]>;
}

export interface DelegatedTrustResolver {
  getDelegatedTrustedDids(selfDid: string): Promise<Set<string>>;
}
