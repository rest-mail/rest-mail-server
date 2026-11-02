import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { EditAPIKeyPermissions } from './EditAPIKeyPermissions'

const key = {
  id: 2,
  name: 'Backups',
  kind: 'api_key',
  public_id: 'rm_public',
  created_at: '2026-10-01T00:00:00Z',
  last_used_at: null,
  expires_at: null,
  permissions: [
    { capability: 'mail:read', scope: 'mailbox:a@example.org' },
    { capability: 'mail:write', scope: 'domain:example.org' },
  ],
  account_id: 7,
  account_address: 'a@example.org',
}

function respond(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status })
}

// The capability list is answered separately, so a test only says what the save gets.
function stubServer(save: (init: RequestInit) => Response) {
  const fetcher = vi.fn(async (url: string, init?: RequestInit) =>
    String(url).endsWith('/admin/capabilities')
      ? respond(200, { data: [{ id: 1, name: 'mail:read' }, { id: 2, name: 'domains:read' }] })
      : save(init ?? {}),
  )
  vi.stubGlobal('fetch', fetcher)
  return fetcher
}

function mount() {
  const props = { onSaved: vi.fn(), onClose: vi.fn(), onGone: vi.fn(), onSignInAgain: vi.fn() }
  render(<EditAPIKeyPermissions apiKey={key} {...props} />)
  return props
}

function submit(password = 'my password') {
  fireEvent.change(screen.getByLabelText('Your current password'), { target: { value: password } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('EditAPIKeyPermissions', () => {
  it('opens on the permissions the key holds now, and never offers a secret', () => {
    stubServer(() => respond(200, {}))
    mount()
    expect(screen.getByLabelText<HTMLInputElement>('Capability 1').value).toBe('mail:read')
    expect(screen.getByLabelText<HTMLInputElement>('Scope 1').value).toBe('mailbox:a@example.org')
    expect(screen.getByLabelText<HTMLInputElement>('Capability 2').value).toBe('mail:write')
    expect(screen.getByLabelText<HTMLInputElement>('Scope 2').value).toBe('domain:example.org')
    expect(screen.queryByLabelText(/secret|token/i)).toBeNull()
  })

  it('sends the edited permissions with the password, and reports what the server stored', async () => {
    const stored = [{ capability: 'mail:read', scope: 'domain:example.org' }, { capability: 'domains:read', scope: '*' }]
    const fetcher = stubServer(() => respond(200, { data: { id: 2, permissions: stored } }))
    const { onSaved } = mount()

    fireEvent.change(screen.getByLabelText('Scope 1'), { target: { value: ' Domain:Example.org ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Remove permission 2' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add a permission' }))
    fireEvent.change(screen.getByLabelText('Capability 2'), { target: { value: 'domains:read' } })
    fireEvent.change(screen.getByLabelText('Scope 2'), { target: { value: '*' } })
    submit()

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(stored))
    const call = fetcher.mock.calls.find(([url]) => String(url).endsWith('/accounts/7/api-keys/2'))
    expect(call?.[1]?.method).toBe('PATCH')
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      password: 'my password',
      permissions: [
        { capability: 'mail:read', scope: 'Domain:Example.org' },
        { capability: 'domains:read', scope: '*' },
      ],
    })
  })

  it('offers a new sign-in for a stale two-factor session, and keeps the form open', async () => {
    stubServer(() => respond(403, { error: { code: 'recent_two_factor_required', message: 'Sign in again' } }))
    const { onSaved, onSignInAgain } = mount()
    submit()

    expect((await screen.findByRole('alert')).textContent).toContain('no longer recent enough')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
    expect(onSignInAgain).toHaveBeenCalledOnce()
    expect(onSaved).not.toHaveBeenCalled()
  })

  it('clears a wrong password and keeps the edits, with no sign-in offered', async () => {
    stubServer(() => respond(403, { error: { code: 'invalid_password', message: 'The password is incorrect' } }))
    mount()
    fireEvent.change(screen.getByLabelText('Scope 1'), { target: { value: '*' } })
    submit('wrong')

    expect((await screen.findByRole('alert')).textContent).toContain('not your current password')
    expect(screen.getByLabelText<HTMLInputElement>('Your current password').value).toBe('')
    expect(screen.getByLabelText<HTMLInputElement>('Scope 1').value).toBe('*')
    expect(screen.queryByRole('button', { name: 'Sign in again' })).toBeNull()
  })

  it("shows permissions over the ceiling in the server's words, keeping the password to try again", async () => {
    const message = "API key permissions must be within the account's and your current grants"
    stubServer(() => respond(403, { error: { code: 'forbidden', message } }))
    const { onSaved } = mount()
    submit()

    expect((await screen.findByRole('alert')).textContent).toContain(message)
    expect(screen.getByLabelText<HTMLInputElement>('Your current password').value).toBe('my password')
    expect(screen.queryByRole('button', { name: 'Sign in again' })).toBeNull()
    expect(onSaved).not.toHaveBeenCalled()
  })

  it("names what is wrong when a key's last permission is removed", async () => {
    const reason = 'must contain between 1 and 64 scoped permissions'
    stubServer(() => respond(422, { error: { code: 'validation_failed', message: 'Request body failed validation', details: { fields: { permissions: reason } } } }))
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Remove permission 2' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove permission 1' }))
    submit()

    expect((await screen.findByRole('alert')).textContent).toContain(`permissions: ${reason}`)
  })

  it('tells the list when the key has gone', async () => {
    stubServer(() => respond(404, { error: { code: 'not_found', message: 'API key not found' } }))
    const { onGone } = mount()
    submit()

    expect((await screen.findByRole('alert')).textContent).toContain('Nothing was changed')
    expect(onGone).toHaveBeenCalledOnce()
  })

  it('still works when the capability suggestions cannot be read', async () => {
    const stored = [{ capability: 'mail:read', scope: '*' }]
    vi.stubGlobal('fetch', vi.fn(async (url: string) =>
      String(url).endsWith('/admin/capabilities')
        ? respond(403, { error: { code: 'forbidden', message: 'Insufficient permissions' } })
        : respond(200, { data: { permissions: stored } }),
    ))
    const { onSaved } = mount()
    submit()

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(stored))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('closes without sending anything', () => {
    const fetcher = stubServer(() => respond(200, {}))
    const { onClose } = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(fetcher.mock.calls.filter(([url]) => String(url).includes('/api-keys/'))).toEqual([])
  })
})
