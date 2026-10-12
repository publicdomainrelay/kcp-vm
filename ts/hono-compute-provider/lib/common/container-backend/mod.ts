export interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

export const BACKEND_TYPE_DOCKER = "docker" as const;
