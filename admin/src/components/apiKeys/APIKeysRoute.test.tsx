import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ComponentType, ReactNode } from 'react'
import { Route } from '../../routes/api-keys/index'
import { useAuthStore } from '../../lib/stores/authStore'

// This test sits outside src/routes because the router plugin takes every file there as a route.
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}))
vi.mock('../layout/AppShell', () => ({
  AppShell: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

const before = [{ capability: 'mail:read', scope: 'mailbox:a@example.org' }]
const after = [{ capability: 'mail:write', scope: 'domain:example.org' }]
const firstPage = [{ capability: 'domains:read', scope: '*' }]

function row(permissions: { capability: string; scope: string }[]) {
  return {
    id: 2,
    name: 'Backups',
    kind: 'api_key',
    public_id: 'rm_public',
    created_at: '2026-10-01T00:00:00Z',
    last_used_at: null,
    expires_at: null,
    permissions,
    account_id: 7,
    account_address: 'a@example.org',
  }
}

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status })
}

function page(keys: unknown[], total = keys.length, hasMore = false) {
  return json(200, { data: keys, pagination: { total, has_more: hasMore } })
}

// `list` answers each read of the list; everything else is a save, unless `save` says otherwise.
function stubServer(list: (url: string, read: number) => Response, save = () => json(200, { data: { permissions: after } })) {
  let reads = 0
  const fetcher = vi.fn(async (input: string) => {
    const url = String(input)
    if (url.includes('/admin/capabilities')) return json(200, { data: [] })
    if (url.includes('/admin/api-keys')) return list(url, reads++)
    return save()
  })
  vi.stubGlobal('fetch', fetcher)
  return fetcher
}

function mount() {
  const Page = Route.options.component as ComponentType
  render(<Page />)
}

async function editAndSave() {
  fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
  const dialog = screen.getByRole('dialog')
  fireEvent.change(within(dialog).getByLabelText('Capability 1'), { target: { value: 'mail:write' } })
  fireEvent.change(within(dialog).getByLabelText('Scope 1'), { target: { value: 'domain:example.org' } })
  fireEvent.change(within(dialog).getByLabelText('Your current password'), { target: { value: 'my password' } })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
}

beforeEach(() => {
  navigate.mockReset()
  useAuthStore.setState({ accessToken: 'cookie-session' })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('the API keys screen', () => {
  it('opens a key on its current permissions', async () => {
    stubServer(() => page([row(before)]))
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/a@example\.org/)).toBeTruthy()
    expect(within(dialog).getByLabelText<HTMLInputElement>('Capability 1').value).toBe('mail:read')
    expect(within(dialog).getByLabelText<HTMLInputElement>('Scope 1').value).toBe('mailbox:a@example.org')
  })

  it('shows the new permissions after a save, read back from the list', async () => {
    const fetcher = stubServer((_, read) => page([row(read === 0 ? before : after)]))
    mount()
    await editAndSave()

    expect((await screen.findByRole('status')).textContent).toContain('Its secret has not changed')
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => expect(screen.getByText('mail:write')).toBeTruthy())
    expect(screen.queryByText('mail:read')).toBeNull()
    await waitFor(() => expect(fetcher.mock.calls.filter(([url]) => String(url).includes('/admin/api-keys'))).toHaveLength(2))
  })

  it('shows what the server stored even when the list then fails to reload', async () => {
    stubServer((_, read) => (read === 0 ? page([row(before)]) : json(500, { error: { code: 'internal_error', message: 'Could not read the list' } })))
    mount()
    await editAndSave()

    expect(await screen.findByText('Could not read the list')).toBeTruthy()
    expect(screen.getByText('mail:write')).toBeTruthy()
    expect(screen.queryByText('mail:read')).toBeNull()
  })

  it('steps back a page when a save leaves it empty, as editing a key out of a permission filter does', async () => {
    let saved = false
    stubServer(
      (url) => {
        if (new URL(url).searchParams.get('offset') === '0') return page([row(firstPage)], saved ? 50 : 51, !saved)
        return saved ? page([], 50) : page([row(before)], 51)
      },
      () => {
        saved = true
        return json(200, { data: { permissions: after } })
      },
    )
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Next' }))
    await screen.findByText('mail:read')
    await editAndSave()

    expect(await screen.findByText('domains:read')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Previous' }).hasAttribute('disabled')).toBe(true)
  })

  it('reloads the list behind the form when the key has gone', async () => {
    const fetcher = stubServer(
      (_, read) => (read === 0 ? page([row(before)]) : page([])),
      () => json(404, { error: { code: 'not_found', message: 'API key not found' } }),
    )
    mount()
    await editAndSave()

    const dialog = await screen.findByRole('dialog')
    expect((await within(dialog).findByRole('alert')).textContent).toContain('Nothing was changed')
    await waitFor(() => expect(fetcher.mock.calls.filter(([url]) => String(url).includes('/admin/api-keys'))).toHaveLength(2))
    expect(await screen.findByText('No API keys.')).toBeTruthy()
  })

  it('signs out and goes to the sign-in page when a stale session needs a new sign-in', async () => {
    stubServer(
      () => page([row(before)]),
      () => json(403, { error: { code: 'recent_two_factor_required', message: 'Sign in again' } }),
    )
    mount()
    await editAndSave()

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in again' }))
    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(useAuthStore.getState().accessToken).toBeNull()
    expect(navigate).toHaveBeenCalledWith({ to: '/login' })
  })

  it('steps back a page after revoking the only key on it', async () => {
    vi.stubGlobal('confirm', () => true)
    let revoked = false
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') {
        revoked = true
        return new Response(null, { status: 204 })
      }
      if (new URL(String(input)).searchParams.get('offset') === '0') return page([row(firstPage)], revoked ? 50 : 51, !revoked)
      return revoked ? page([], 50) : page([row(before)], 51)
    }))
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Next' }))
    await screen.findByText('mail:read')
    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))

    expect(await screen.findByText('domains:read')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Previous' }).hasAttribute('disabled')).toBe(true)
  })
})
