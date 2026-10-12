export class HTTPError extends Error {
  status: number;
  detail: string;
  constructor(status: number, detail: string) {
    super(detail);
    this.status = status;
    this.detail = detail;
  }
  toJSON(): { error: string; code: number; detail: string } {
    return { error: "http_error", code: this.status, detail: this.detail };
  }
}
