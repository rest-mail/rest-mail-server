import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useAuthStore } from './authStore'
import { setUnauthorizedHandler } from '../api'

beforeEach(() => {
  useAuthStore.setState({ user: null, accessToken: null, isAuthenticated: false, sessionChecked: false, setupRequired: [] })
})
afterEach(() => vi.unstubAllGlobals())

it('keeps setup restrictions through login/refresh and accepts completion', async () => {
  const session = (steps: string[]) => new Response(JSON.stringify({ data: {
    user: { id: 1, email: 'admin@example.com' }, capabilities: steps.length ? [] : ['domains:write'], setup_required: steps,
  } }), { status: 200 })
  vi.stubGlobal('fetch', vi.fn()
    .mockResolvedValueOnce(session(['password', 'two_factor']))
    .mockResolvedValueOnce(session(['two_factor']))
    .mockResolvedValueOnce(session([])))
  await useAuthStore.getState().login('admin@example.com', 'temporary password')
  expect(useAuthStore.getState().setupRequired).toEqual(['password', 'two_factor'])
  await useAuthStore.getState().checkSession()
  expect(useAuthStore.getState().setupRequired).toEqual(['two_factor'])
  await useAuthStore.getState().checkSession()
  expect(useAuthStore.getState().setupRequired).toEqual([])
  expect(useAuthStore.getState().user?.capabilities).toEqual(['domains:write'])
  expect(localStorage.getItem('rest-mail-admin-auth')).not.toContain('cookie-session')
})
it('finishes anonymous boot without redirecting a recovery fragment', async () => {
  const unauthorized = vi.fn(); setUnauthorizedHandler(unauthorized)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 401 })))
  await useAuthStore.getState().checkSession()
  expect(unauthorized).not.toHaveBeenCalled()
  expect(useAuthStore.getState().sessionChecked).toBe(true)
  expect(useAuthStore.getState().isAuthenticated).toBe(false)
})
