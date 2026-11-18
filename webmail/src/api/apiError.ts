export class ApiError extends Error {
  status: number;
  code: string;
  /** On a validation failure, why each refused field was refused. `message` only says the body failed. */
  fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

/** The error a refused response's body describes. A proxy's reply may not be the API's JSON envelope. */
export function apiErrorFromBody(status: number, body: string): ApiError {
  try {
    const parsed = JSON.parse(body);
    if (parsed.error) {
      return new ApiError(status, parsed.error.code || 'unknown', parsed.error.message || body, parsed.error.details?.fields ?? {});
    }
  } catch {
    // Not JSON, so the body itself is all there is to report.
  }
  return new ApiError(status, 'unknown', `API error ${status}: ${body}`);
}
