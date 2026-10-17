/**
 * Two-factor authentication (TOTP) client.
 *
 * The server (two_factor.go) fully supports per-account TOTP for admin tokens:
 * status → enroll → confirm → disable, all keyed on the caller's own session.
 */
import { apiV1 } from './api'

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

// A wrong code on confirm/disable is a 403 with its reason, never a 401, so
// they go through apiV1.request like everything else: its 401 handler only
// ever sees an expired session.

/** Confirm enrollment by verifying a first code; flips 2FA active. */
export async function confirmTwoFactor(code: string): Promise<void> {
  await expectNoContent(await post('/auth/2fa/confirm', { totp_code: code }), 'confirm 2FA')
}

/** Disable 2FA; requires a current TOTP code or an unused recovery code. */
export async function disableTwoFactor(proof: SecondFactor): Promise<void> {
  await expectNoContent(await post('/auth/2fa/disable', proof), 'disable 2FA')
}

/** A refused 2FA request: `code` is the API's error code, e.g. "invalid_totp". */
export class TwoFactorError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message)
    this.name = 'TwoFactorError'
  }
}

// What to tell the admin for each refusal the API names; anything else shows
// the API's own message.
const refusals: Record<string, string> = {
  invalid_totp: 'That code is not right: enter the current one from your authenticator',
  invalid_recovery_code: 'That recovery code is not right, or has been used already',
}

function post(path: string, body: unknown): Promise<Response> {
  return apiV1.request(path, { method: 'POST', body: JSON.stringify(body) })
}

async function unwrap<T>(res: Response, action: string): Promise<T> {
  if (!res.ok) throw await toError(res, action)
  const body = await res.json()
  return (body?.data ?? body) as T
}

async function expectNoContent(res: Response, action: string): Promise<void> {
  if (!res.ok) throw await toError(res, action)
}

async function toError(res: Response, action: string): Promise<TwoFactorError> {
  try {
    const body = await res.json()
    const code: string = body?.error?.code || 'unknown'
    return new TwoFactorError(code, refusals[code] || body?.error?.message || `Failed to ${action}`)
  } catch {
    return new TwoFactorError('unknown', `Failed to ${action}`)
  }
}
