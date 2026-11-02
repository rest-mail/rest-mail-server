import { afterEach, describe, expect, it, vi } from 'vitest'
import { type APIKeyRefusal, apiKeysQuery, listAdminAPIKeys, listCapabilityNames, revokeAPIKey, updateAPIKeyPermissions } from './apiKeys'

const BASE = 'https://api.test'

function mockFetch(status: number, body?: unknown) {
  const fn = vi.fn().mockResolvedValue(new Response(body === undefined ? null : JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fn)
  return fn
}

afterEach(() => vi.unstubAllGlobals())

describe('apiKeysQuery', () => {
  it('sends only the filters that are set', () => {
    expect(apiKeysQuery({ owner: '  ', capability: '' }, 50, 0)).toBe('limit=50&offset=0')
  })

  it('sends dates as RFC 3339 times, which is what the API accepts', () => {
    const query = new URLSearchParams(
      apiKeysQuery({ owner: ' a@example.org ', capability: 'mail:read', lastUsedBefore: '2026-01-02', expiresBefore: '2026-03-04' }, 10, 20),
    )
    expect(query.get('owner')).toBe('a@example.org')
    expect(query.get('capability')).toBe('mail:read')
    expect(query.get('last_used_before')).toBe('2026-01-02T00:00:00.000Z')
    expect(query.get('expires_before')).toBe('2026-03-04T00:00:00.000Z')
    expect(query.get('offset')).toBe('20')
  })
})

describe('listAdminAPIKeys', () => {
  it('reads the page and its paging', async () => {
    const fetch = mockFetch(200, { data: [{ id: 2 }], pagination: { total: 51, has_more: true } })
    const page = await listAdminAPIKeys({}, 50, 0)
    expect(fetch.mock.calls[0][0]).toBe(`${BASE}/admin/api-keys?limit=50&offset=0`)
    expect(page).toEqual({ keys: [{ id: 2 }], total: 51, hasMore: true })
  })

  it("shows the server's refusal", async () => {
    mockFetch(403, { error: { code: 'forbidden', message: 'Not allowed' } })
    await expect(listAdminAPIKeys({}, 50, 0)).rejects.toThrow('Not allowed')
  })
})

describe('revokeAPIKey', () => {
  it("deletes it through its own account's route", async () => {
    const fetch = mockFetch(204)
    await revokeAPIKey({ id: 2, account_id: 7 })
    expect(fetch.mock.calls[0][0]).toBe(`${BASE}/accounts/7/api-keys/2`)
    expect((fetch.mock.calls[0][1] as RequestInit).method).toBe('DELETE')
  })

  it("shows why it was refused, such as a session without a second factor", async () => {
    mockFetch(403, { error: { code: 'two_factor_required', message: 'Sign in again with your code' } })
    await expect(revokeAPIKey({ id: 2, account_id: 7 })).rejects.toThrow('Sign in again with your code')
  })
})

describe('listCapabilityNames', () => {
  it('reads the names', async () => {
    const fetch = mockFetch(200, { data: [{ id: 1, name: 'mail:read' }, { id: 2, name: 'mail:write' }] })
    await expect(listCapabilityNames()).resolves.toEqual(['mail:read', 'mail:write'])
    expect(fetch.mock.calls[0][0]).toBe(`${BASE}/admin/capabilities`)
  })
})

describe('updateAPIKeyPermissions', () => {
  const key = { id: 2, account_id: 7 }
  const permissions = [{ capability: 'mail:read', scope: 'mailbox:a@example.org' }]

  async function refusalFor(status: number, body?: unknown) {
    mockFetch(status, body)
    return updateAPIKeyPermissions(key, 'pw', permissions).then(
      () => { throw new Error('expected a refusal') },
      (err: APIKeyRefusal) => err,
    )
  }

  it("patches its own account's route with the password and the new permissions, and nothing else", async () => {
    const fetch = mockFetch(200, { data: { id: 2, permissions } })
    await expect(updateAPIKeyPermissions(key, 'pw', permissions)).resolves.toEqual(permissions)
    expect(fetch.mock.calls[0][0]).toBe(`${BASE}/accounts/7/api-keys/2`)
    const init = fetch.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(String(init.body))).toEqual({ password: 'pw', permissions })
  })

  it('says a stale two-factor session needs a new sign-in', async () => {
    const refusal = await refusalFor(403, { error: { code: 'recent_two_factor_required', message: 'Sign in again with two-factor authentication: a recent second factor is required' } })
    expect(refusal.code).toBe('recent_two_factor_required')
    expect(refusal.needsSignIn).toBe(true)
    expect(refusal.message).toContain('no longer recent enough')
  })

  it('says a session without a second factor needs a new sign-in', async () => {
    const refusal = await refusalFor(403, { error: { code: 'two_factor_required', message: 'This action needs a session signed in with two-factor authentication: sign in again with your code' } })
    expect(refusal.needsSignIn).toBe(true)
    expect(refusal.message).toContain('signed in with two-factor')
  })

  it("says a wrong password is the administrator's own, and a new sign-in does not help", async () => {
    const refusal = await refusalFor(403, { error: { code: 'invalid_password', message: 'The password is incorrect' } })
    expect(refusal.needsSignIn).toBe(false)
    expect(refusal.message).toContain("not the key owner's")
  })

  it('does not repeat the "create a credential" wording of a missing password', async () => {
    const refusal = await refusalFor(403, { error: { code: 'reauth_required', message: 'Enter your current password to create a credential' } })
    expect(refusal.message).toBe("Enter your current password to change this key's permissions.")
  })

  it("shows the server's own words for permissions over the ceiling", async () => {
    const message = "API key permissions must be within the account's and your current grants"
    const refusal = await refusalFor(403, { error: { code: 'forbidden', message } })
    expect(refusal.code).toBe('forbidden')
    expect(refusal.needsSignIn).toBe(false)
    expect(refusal.message).toBe(message)
  })

  it('names the field at fault, not the generic validation message', async () => {
    const refusal = await refusalFor(422, {
      error: { code: 'validation_failed', message: 'Request body failed validation', details: { fields: { permissions: 'contains an unknown capability' } } },
    })
    expect(refusal.code).toBe('validation_failed')
    expect(refusal.message).toBe('permissions: contains an unknown capability')
  })

  it('says a key that has gone was not changed', async () => {
    const refusal = await refusalFor(404, { error: { code: 'not_found', message: 'API key not found' } })
    expect(refusal.code).toBe('not_found')
    expect(refusal.message).toContain('Nothing was changed')
  })

  it("shows the server's message for a refusal it has no words of its own for", async () => {
    const refusal = await refusalFor(429, { error: { code: 'rate_limited', message: 'Too many requests' } })
    expect(refusal.message).toBe('Too many requests')
  })

  it('still explains itself when an error response is not JSON', async () => {
    const refusal = await refusalFor(502)
    expect(refusal.code).toBe('unknown')
    expect(refusal.message).toBe("Could not change the API key's permissions")
  })
})
