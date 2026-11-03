import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  addInstallationSenderRule,
  holdsInstallationWide,
  listInstallationSenderRules,
  removeInstallationSenderRule,
} from './installationSenderRules'

afterEach(() => vi.unstubAllGlobals())

function mockFetch(status: number, body?: unknown) {
  const fn = vi.fn().mockResolvedValue(new Response(body === undefined ? null : JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fn)
  return fn
}

const rule = { id: 3, pattern: '@spam.example', list_type: 'block' as const, created_at: '2026-01-01T00:00:00Z' }

describe('who sees installation sender rules', () => {
  it('needs the capability at *, or *', () => {
    expect(holdsInstallationWide(['domains:read'], 'domains:read')).toBe(true)
    expect(holdsInstallationWide(['*'], 'domains:write')).toBe(true)
    expect(holdsInstallationWide(['domains:read'], 'domains:write')).toBe(false)
    expect(holdsInstallationWide([], 'domains:read')).toBe(false)
  })
})

describe('installation sender rules', () => {
  it('lists them', async () => {
    const fetch = mockFetch(200, { data: [rule] })
    await expect(listInstallationSenderRules()).resolves.toEqual([rule])
    expect(String(fetch.mock.calls[0][0])).toMatch(/\/admin\/sender-rules$/)
  })

  it('adds one', async () => {
    const fetch = mockFetch(201, { data: rule })
    await expect(addInstallationSenderRule({ pattern: '@spam.example', list_type: 'block' })).resolves.toEqual(rule)
    const init = fetch.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"pattern":"@spam.example","list_type":"block"}')
  })

  it('removes one', async () => {
    const fetch = mockFetch(204)
    await removeInstallationSenderRule(3)
    expect(String(fetch.mock.calls[0][0])).toMatch(/\/admin\/sender-rules\/3$/)
    expect((fetch.mock.calls[0][1] as RequestInit).method).toBe('DELETE')
  })

  it("shows the server's refusal", async () => {
    mockFetch(403, { error: { code: 'forbidden', message: 'not allowed' } })
    await expect(listInstallationSenderRules()).rejects.toThrow('not allowed')
  })
})
