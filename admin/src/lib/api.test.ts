import { describe, it, expect, beforeEach, vi } from 'vitest'
import { apiV1, apiUrl, apiRequest, setUnauthorizedHandler } from './api'

// VITE_API_URL is pinned to https://api.test by vitest.config.ts, so every
// assertion below is against that fixed base.
const BASE = 'https://api.test'

function mockFetch(response: Partial<Response> = { status: 200 }) {
  const fn = vi.fn().mockResolvedValue(response as Response)
  vi.stubGlobal('fetch', fn)
  return fn
}

function lastInit(fn: ReturnType<typeof vi.fn>): RequestInit {
  return fn.mock.calls[0][1] as RequestInit
}

function lastHeaders(fn: ReturnType<typeof vi.fn>): Headers {
  return new Headers(lastInit(fn).headers)
}

describe('apiV1.url', () => {
  it('joins the API base with the path', () => {
    expect(apiV1.url('/admin/domains')).toBe(`${BASE}/admin/domains`)
  })

  it('is exposed via the legacy apiUrl alias', () => {
    expect(apiUrl).toBe(apiV1.url)
    expect(apiUrl('/x')).toBe(`${BASE}/x`)
  })
})

describe('apiV1.request', () => {
  beforeEach(() => {
    // A registered 401 handler survives across tests; reset it to a no-op so a
    // stray handler from another test cannot fire here.
    setUnauthorizedHandler(() => {})
  })

  it('fetches the base-prefixed URL', async () => {
    const fetchFn = mockFetch()
    await apiRequest('/admin/queue')
    expect(fetchFn).toHaveBeenCalledTimes(1)
    expect(fetchFn.mock.calls[0][0]).toBe(`${BASE}/admin/queue`)
  })

  it('attaches a Bearer token when one is supplied', async () => {
    const fetchFn = mockFetch()
    await apiV1.request('/admin/domains', { method: 'GET' }, 'tok-123')
    expect(lastHeaders(fetchFn).get('Authorization')).toBe('Bearer tok-123')
  })

  it('omits Authorization when no token is supplied', async () => {
    const fetchFn = mockFetch()
    await apiV1.request('/admin/domains')
    expect(lastHeaders(fetchFn).has('Authorization')).toBe(false)
  })

  it('defaults Content-Type to application/json when a body is present', async () => {
    const fetchFn = mockFetch()
    await apiV1.request('/admin/domains', {
      method: 'POST',
      body: JSON.stringify({ name: 'example.com' }),
    })
    expect(lastHeaders(fetchFn).get('Content-Type')).toBe('application/json')
  })

  it('does not override a caller-supplied Content-Type', async () => {
    const fetchFn = mockFetch()
    await apiV1.request('/admin/import', {
      method: 'POST',
      body: 'a,b,c',
      headers: { 'Content-Type': 'text/csv' },
    })
    expect(lastHeaders(fetchFn).get('Content-Type')).toBe('text/csv')
  })

  it('does not set Content-Type for a bodyless request', async () => {
    const fetchFn = mockFetch()
    await apiV1.request('/admin/domains', { method: 'GET' })
    expect(lastHeaders(fetchFn).has('Content-Type')).toBe(false)
  })

  it('returns the fetch response unchanged', async () => {
    const response = { status: 204, ok: true } as Response
    mockFetch(response)
    const result = await apiV1.request('/admin/domains')
    expect(result).toBe(response)
  })
})

describe('401 handling', () => {
  it('invokes the registered handler on a 401 response', async () => {
    mockFetch({ status: 401 })
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    await apiV1.request('/admin/domains')
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('does not invoke the handler on a non-401 response', async () => {
    mockFetch({ status: 200 })
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    await apiV1.request('/admin/domains')
    expect(handler).not.toHaveBeenCalled()
  })
})

describe('legacy aliases', () => {
  it('apiRequest is the same function as apiV1.request', () => {
    expect(apiRequest).toBe(apiV1.request)
  })
})
