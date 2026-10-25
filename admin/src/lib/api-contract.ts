import type { components, operations } from "./api-types";

/** Wire types come from the generated document, including the response envelope. */
export type RequestBody<Operation extends keyof operations> =
  NonNullable<operations[Operation]["requestBody"]> extends {
    content: { "application/json": infer Body };
  }
    ? Body
    : never;

export type JsonResponse<
  Operation extends keyof operations,
  Status extends keyof operations[Operation]["responses"],
> = operations[Operation]["responses"][Status] extends {
  content: { "application/json": infer Body };
}
  ? Body
  : never;

export async function expectSuccess(
  response: Response,
  fallback: string,
): Promise<void> {
  if (response.ok) return;

  let message = fallback;
  try {
    const body: components["schemas"]["ErrorResponse"] = await response.json();
    message = body.error?.message || fallback;
  } catch {
    // A proxy or an empty error response may not carry the API's JSON envelope.
  }
  throw new Error(message);
}

export async function readJSON<Body>(
  response: Response,
  fallback: string,
): Promise<Body> {
  await expectSuccess(response, fallback);
  return response.json();
}
