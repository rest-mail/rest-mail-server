import { afterEach, describe, expect, it, vi } from 'vitest'
import { bytesToMiB, getServerLimits, miBToBytes, updateServerLimits } from './serverLimits'

afterEach(() => vi.unstubAllGlobals())

function mockFetch(status: number, body: unknown) {
  const fn = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('sizes', () => {
  it('converts between MiB and bytes', () => {
    expect(miBToBytes('10')).toBe(10485760)
    expect(miBToBytes('0.5')).toBe(524288)
    expect(bytesToMiB(10485760)).toBe(10)
  })

  it('refuses what is not a usable size', () => {
    for (const text of ['', ' ', '0', '-1', 'ten', 'Infinity']) expect(miBToBytes(text)).toBeNull()
  })
})

describe('server limits', () => {
  it('reads them', async () => {
    mockFetch(200, { data: { contact_import_max_bytes: 10485760 } })
    await expect(getServerLimits()).resolves.toEqual({ contact_import_max_bytes: 10485760 })
  })

  it('sends a change', async () => {
    const fetch = mockFetch(200, { data: { contact_import_max_bytes: 5 } })
    await updateServerLimits({ contact_import_max_bytes: 5 })
    const init = fetch.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"contact_import_max_bytes":5}')
  })

  it("shows the server's refusal", async () => {
    mockFetch(403, { error: { code: 'superadmin_required', message: 'only a superadmin' } })
    await expect(getServerLimits()).rejects.toThrow('only a superadmin')
  })
})
