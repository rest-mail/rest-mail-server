import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiKeysQuery, listAdminAPIKeys, revokeAPIKey } from './apiKeys'

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
