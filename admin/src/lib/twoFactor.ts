/**
 * Two-factor authentication (TOTP) client.
 *
 * The server (two_factor.go) fully supports per-account TOTP for admin tokens:
 * status → enroll → confirm → disable, all keyed on the caller's own session.
 */
import { apiV1 } from "./api";

import type { paths } from "./api-types";
import type { JsonResponse } from "./api-contract";

export type TwoFactorStatus =
  paths["/api/v1/auth/2fa"]["get"]["responses"][200]["content"]["application/json"]["data"];
export type TwoFactorEnrollment =
  paths["/api/v1/auth/2fa/enroll"]["post"]["responses"][200]["content"]["application/json"]["data"];
export type SecondFactor = NonNullable<
  paths["/api/v1/auth/2fa/disable"]["post"]["requestBody"]
>["content"]["application/json"];
type ConfirmBody = NonNullable<
  paths["/api/v1/auth/2fa/confirm"]["post"]["requestBody"]
>["content"]["application/json"];

export async function getTwoFactorStatus(): Promise<TwoFactorStatus> {
  return (
    await unwrap<JsonResponse<"getTwoFactorStatus", 200>>(
      await apiV1.request("/auth/2fa"),
      "load 2FA status",
    )
  ).data;
}

/** Begin enrollment: mints a pending TOTP secret + recovery codes. */
export async function enrollTwoFactor(): Promise<TwoFactorEnrollment> {
  return (
    await unwrap<JsonResponse<"enrollTwoFactor", 200>>(
      await apiV1.request("/auth/2fa/enroll", { method: "POST" }),
      "start enrollment",
    )
  ).data;
}

// A wrong code on confirm/disable is a 403 with its reason, never a 401, so
// they go through apiV1.request like everything else: its 401 handler only
// ever sees an expired session.

/** Confirm enrollment by verifying a first code; flips 2FA active. */
export async function confirmTwoFactor(code: string): Promise<void> {
  const body: ConfirmBody = { totp_code: code };
  await expectNoContent(await post("/auth/2fa/confirm", body), "confirm 2FA");
}

/** Disable 2FA; requires a current TOTP code or an unused recovery code. */
export async function disableTwoFactor(proof: SecondFactor): Promise<void> {
  await expectNoContent(await post("/auth/2fa/disable", proof), "disable 2FA");
}

/** A refused 2FA request: `code` is the API's error code, e.g. "invalid_totp". */
export class TwoFactorError extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "TwoFactorError";
  }
}

// What to tell the admin for each refusal the API names; anything else shows
// the API's own message.
const refusals: Record<string, string> = {
  invalid_totp:
    "That code is not right: enter the current one from your authenticator",
  invalid_recovery_code:
    "That recovery code is not right, or has been used already",
};

function post(path: string, body: unknown): Promise<Response> {
  return apiV1.request(path, { method: "POST", body: JSON.stringify(body) });
}

async function unwrap<T>(res: Response, action: string): Promise<T> {
  if (!res.ok) throw await toError(res, action);
  const body: T = await res.json();
  return body;
}

async function expectNoContent(res: Response, action: string): Promise<void> {
  if (!res.ok) throw await toError(res, action);
}

async function toError(res: Response, action: string): Promise<TwoFactorError> {
  try {
    const body = await res.json();
    const code: string = body?.error?.code || "unknown";
    return new TwoFactorError(
      code,
      refusals[code] || body?.error?.message || `Failed to ${action}`,
    );
  } catch {
    return new TwoFactorError("unknown", `Failed to ${action}`);
  }
}
