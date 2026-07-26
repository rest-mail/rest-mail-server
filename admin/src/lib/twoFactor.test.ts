import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  getTwoFactorStatus,
  enrollTwoFactor,
  confirmTwoFactor,
  disableTwoFactor,
} from './twoFactor'
import { setUnauthorizedHandler } from './api'

// VITE_API_URL is pinned to https://api.test by vitest.config.ts.
const BASE = 'https://api.test'

function jsonResponse(body: unknown, init: { status?: number } = {}): Response {
  const status = init.status ?? 200
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

// 204 has no body; json() must never be called for these.
function noContent(): Response {
  return {
    ok: true,
    status: 204,
    json: async () => { throw new Error('no body to parse') },
  } as unknown as Response
}

function mockFetch(response: Response) {
  const fn = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('twoFactor client', () => {
  beforeEach(() => {
    // Keep a stray global 401 handler from another test from firing here.
    setUnauthorizedHandler(() => {})
    document.cookie = 'restmail_csrf=csrf-xyz'
  })

  it('getTwoFactorStatus unwraps the {data} envelope', async () => {
    const fetchFn = mockFetch(jsonResponse({ data: { enabled: true, pending: false } }))
    const status = await getTwoFactorStatus()
    expect(fetchFn.mock.calls[0][0]).toBe(`${BASE}/auth/2fa`)
    expect(status).toEqual({ enabled: true, pending: false })
  })

  it('enrollTwoFactor POSTs and returns the enrollment', async () => {
    const enrollment = { secret: 'ABC123', otpauth_url: 'otpauth://totp/x', recovery_codes: ['a', 'b'] }
    const fetchFn = mockFetch(jsonResponse({ data: enrollment }))
    const result = await enrollTwoFactor()
    expect(fetchFn.mock.calls[0][0]).toBe(`${BASE}/auth/2fa/enroll`)
    expect((fetchFn.mock.calls[0][1] as RequestInit).method).toBe('POST')
    expect(result).toEqual(enrollment)
  })

  it('confirmTwoFactor posts the code with a CSRF header and resolves on 204', async () => {
    const fetchFn = mockFetch(noContent())
    await expect(confirmTwoFactor('123456')).resolves.toBeUndefined()
    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${BASE}/auth/2fa/confirm`)
    expect(init.credentials).toBe('include')
    expect(JSON.parse(init.body as string)).toEqual({ code: '123456' })
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('csrf-xyz')
  })

  it('disableTwoFactor sends a recovery code when given one', async () => {
    const fetchFn = mockFetch(noContent())
    await disableTwoFactor({ recovery_code: 'r-1' })
    const init = fetchFn.mock.calls[0][1] as RequestInit
    expect(fetchFn.mock.calls[0][0]).toBe(`${BASE}/auth/2fa/disable`)
    expect(JSON.parse(init.body as string)).toEqual({ recovery_code: 'r-1' })
  })

  it('surfaces the API error message on a failed confirm', async () => {
    mockFetch(jsonResponse({ error: { code: 'unauthorized', message: 'Invalid 2FA code' } }, { status: 401 }))
    await expect(confirmTwoFactor('000000')).rejects.toThrow('Invalid 2FA code')
  })
})
