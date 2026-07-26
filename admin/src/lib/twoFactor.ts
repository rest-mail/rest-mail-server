/**
 * Two-factor authentication (TOTP) client.
 *
 * The server (two_factor.go) fully supports per-account TOTP for admin tokens:
 * status → enroll → confirm → disable, all keyed on the caller's own session.
 */
import { apiV1, getCsrfToken } from './api'

export interface TwoFactorStatus {
  /** True once a confirmed enrollment gates login. */
  enabled: boolean
  /** True when an enrollment exists but has not been confirmed yet. */
  pending: boolean
}

export interface TwoFactorEnrollment {
  /** base32 TOTP secret, for manual entry into an authenticator app. */
  secret: string
  /** otpauth:// provisioning URI (the QR-code payload). */
  otpauth_url: string
  /** One-time recovery codes, returned ONCE at enrollment. */
  recovery_codes: string[]
}

/** A TOTP code or a one-time recovery code. */
export interface SecondFactor {
  totp_code?: string
  recovery_code?: string
}

export async function getTwoFactorStatus(): Promise<TwoFactorStatus> {
  return unwrap(await apiV1.request('/auth/2fa'), 'load 2FA status')
}

/** Begin enrollment: mints a pending TOTP secret + recovery codes. */
export async function enrollTwoFactor(): Promise<TwoFactorEnrollment> {
  return unwrap(await apiV1.request('/auth/2fa/enroll', { method: 'POST' }), 'start enrollment')
}

// confirm/disable reply 204 No Content and their 401 means "invalid code", NOT
// an expired session — so they bypass apiV1.request (whose global 401 handler
// would log the admin out) and post directly with the session cookie + CSRF.

/** Confirm enrollment by verifying a first code; flips 2FA active. */
export async function confirmTwoFactor(code: string): Promise<void> {
  await expectNoContent(await postDirect('/auth/2fa/confirm', { code }), 'confirm 2FA')
}

/** Disable 2FA; requires a current TOTP code or an unused recovery code. */
export async function disableTwoFactor(proof: SecondFactor): Promise<void> {
  await expectNoContent(await postDirect('/auth/2fa/disable', proof), 'disable 2FA')
}

function postDirect(path: string, body: unknown): Promise<Response> {
  return fetch(apiV1.url(path), {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCsrfToken() },
    body: JSON.stringify(body),
  })
}

async function unwrap<T>(res: Response, action: string): Promise<T> {
  if (!res.ok) throw await toError(res, action)
  const body = await res.json()
  return (body?.data ?? body) as T
}

async function expectNoContent(res: Response, action: string): Promise<void> {
  if (!res.ok) throw await toError(res, action)
}

async function toError(res: Response, action: string): Promise<Error> {
  try {
    const body = await res.json()
    return new Error(body?.error?.message || `Failed to ${action}`)
  } catch {
    return new Error(`Failed to ${action}`)
  }
}
