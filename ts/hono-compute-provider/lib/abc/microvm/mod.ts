export interface MicrovmSpec {
  name: string;
  workDir: string;
  userDataFile: string;
  vcpu?: number;
  memMib?: number;
  diskMib?: number;
}

export interface MicrovmResult {
  name: string;
  fingerprint: string;
  vmmPid: number;
  guestIp: string;
  socket: string;
  console: string;
  workDir: string;
  container: string;
}

export interface MicrovmExit {
  container: string;
  reason: "exited" | "removed";
  exitCode?: number;
  logs: string;
}

export interface MicrovmContainerState {
  exists: boolean;
  running: boolean;
  exitCode?: number;
}

export interface Microvm {
  readonly name: string;
  boot(spec: MicrovmSpec): Promise<MicrovmResult>;
  stop(workDir: string): Promise<void>;
  watch(container: string, onExit: (exit: MicrovmExit) => void): () => void;
  inspect?(container: string): Promise<MicrovmContainerState>;
  hostAddressForGuest(): Promise<string>;
}
