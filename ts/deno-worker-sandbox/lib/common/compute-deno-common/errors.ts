export class DenoComputeError extends Error {
  readonly status: number;
  readonly errorName: string;

  constructor(message: string, status = 500, errorName = "InternalError") {
    super(message);
    this.name = "DenoComputeError";
    this.status = status;
    this.errorName = errorName;
  }

  toJSON(): Record<string, unknown> {
    return { error: this.errorName, message: this.message, status: this.status };
  }
}
