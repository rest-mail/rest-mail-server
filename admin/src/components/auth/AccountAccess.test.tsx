import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode, useState } from 'react'
import { AccountAccess, type SetupSession } from './AccountAccess'

function mount(steps: ('password' | 'two_factor')[] = []) {
  const callbacks = { onSignOut: vi.fn(), onSession: vi.fn() }
  render(<AccountAccess apiBase="/api/v1" email="admin@example.com" steps={steps} {...callbacks}>
    <p>Private mailbox</p></AccountAccess>)
  return callbacks
}
function passwords() {
  for (const label of ['New password', 'Confirm new password'])
    fireEvent.change(screen.getByLabelText(label), { target: { value: 'a password chosen by the owner' } })
}
function response(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status })
}
beforeEach(() => {
  window.history.replaceState(null, '', '/login')
  localStorage.clear(); sessionStorage.clear()
  document.cookie = 'restmail_csrf=test-csrf'
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('account setup and recovery', () => {
  it("accepts an invitation without exposing its token in history or storage", async () => {
    window.history.replaceState(
      null,
      "",
      "/login#invite_token=private-invitation-token",
    );
    const fetcher = vi.fn().mockResolvedValue(
      response({
        data: {
          user: { id: 12, email: "invited@example.com" },
          setup_required: ["two_factor"],
        },
      }),
    );
    vi.stubGlobal("fetch", fetcher);
    const { onSession } = mount();
    expect(location.hash).toBe("");
    expect(fetcher).not.toHaveBeenCalled();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    expect(screen.queryByText("Private mailbox")).toBeNull();
    passwords();
    fireEvent.click(screen.getByText("Accept invitation"));
    await waitFor(() => expect(onSession).toHaveBeenCalledOnce());
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/api/v1/auth/invitation");
    expect(init.credentials).toBe("include");
    expect(JSON.parse(init.body)).toEqual({
      token: "private-invitation-token",
      password: "a password chosen by the owner",
    });
    expect(onSession.mock.calls[0][0].setup_required).toEqual(["two_factor"]);
  });
  it("keeps an expired invitation visible without starting a session", async () => {
    window.history.replaceState(null, "", "/login#invite");
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          response(
            {
              error: {
                code: "invalid_token",
                message: "Invitation expired or already used",
              },
            },
            401,
          ),
        ),
    );
    const { onSession } = mount();
    fireEvent.change(screen.getByLabelText("Invitation token"), {
      target: { value: "expired-invitation" },
    });
    passwords();
    fireEvent.click(screen.getByText("Accept invitation"));
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "expired or already used",
      ),
    );
    expect(onSession).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Invitation token")).toBeTruthy();
  });
  it('retains a fragment token when StrictMode initializes twice', () => {
    window.history.replaceState(null, '', '/login#reset_token=strict-mode-test-token')
    render(<StrictMode><AccountAccess apiBase="/api/v1" steps={[]} onSession={() => {}} onSignOut={() => {}}>
      <p>Private mailbox</p></AccountAccess></StrictMode>)
    expect(location.hash).toBe('')
    expect((screen.getByLabelText('Recovery token') as HTMLInputElement).value).toBe('strict-mode-test-token')
  })
  it('hides app content until both required steps are complete', () => {
    mount(['password', 'two_factor'])
    expect(screen.queryByText('Private mailbox')).toBeNull()
    expect(screen.getByLabelText('Current password')).toBeTruthy()
    expect(screen.queryByText('Set up authenticator')).toBeNull()
  })
  it('scrubs fragment tokens, keeps them out of storage, and redeems only on submit', async () => {
    window.history.replaceState(null, '', '/login#reset_token=one-time-test-token')
    const fetcher = vi.fn().mockResolvedValue(response({ data: {
      user: { id: 1, email: 'admin@example.com' }, setup_required: ['two_factor'],
    } }))
    vi.stubGlobal('fetch', fetcher)
    const { onSession } = mount()
    expect(location.hash).toBe('')
    expect(fetcher).not.toHaveBeenCalled()
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0)
    passwords(); fireEvent.click(screen.getByText('Set new password'))
    await waitFor(() => expect(onSession).toHaveBeenCalled())
    const [url, init] = fetcher.mock.calls[0]
    expect(url).toBe('/api/v1/auth/reset'); expect(init.credentials).toBe('include')
    expect(JSON.parse(init.body)).toEqual({ token: 'one-time-test-token', password: 'a password chosen by the owner' })
    expect(onSession.mock.calls[0][0].setup_required).toEqual(['two_factor'])
  })
  it('keeps invalid tokens on the form without starting a session', async () => {
    window.history.replaceState(null, '', '/login#reset')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ error: { code: 'invalid_token', message: 'Token expired or already used' } }, 401)))
    const { onSession } = mount()
    fireEvent.change(screen.getByLabelText('Recovery token'), { target: { value: 'expired-token' } })
    passwords(); fireEvent.click(screen.getByText('Set new password'))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('expired or already used'))
    expect(onSession).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Recovery token')).toBeTruthy()
  })
  it('does not send mismatched passwords', () => {
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher)
    mount(['password']); passwords()
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'something different' } })
    fireEvent.submit(screen.getByText('Change password').closest('form')!)
    expect(screen.getByRole('alert').textContent).toContain('do not match')
    expect(fetcher).not.toHaveBeenCalled()
  })
  it('changes passwords with CSRF then signs out the revoked session', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ data: { message: 'Password changed' } }))
    vi.stubGlobal('fetch', fetcher)
    const { onSignOut } = mount(['password']); passwords()
    fireEvent.change(screen.getByLabelText('Current password'), { target: { value: 'temporary password' } })
    fireEvent.click(screen.getByText('Change password'))
    await waitFor(() => expect(onSignOut).toHaveBeenCalledOnce())
    expect(fetcher.mock.calls[0][0]).toBe('/api/v1/auth/password')
    expect(fetcher.mock.calls[0][1].headers['X-CSRF-Token']).toBe('test-csrf')
    expect(JSON.parse(fetcher.mock.calls[0][1].body).current_password).toBe('temporary password')
  })
  it('retains recovery codes on rejected TOTP and ends the setup session only after confirmation', async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ data: { secret: 'TESTSETUPKEY', otpauth_url: 'otpauth://totp/REST%20Mail:admin%40example.com?secret=TESTSETUPKEY&issuer=REST%20Mail', recovery_codes: ['recovery-one', 'recovery-two'] } }))
      .mockResolvedValueOnce(response({ error: { code: 'invalid_totp', message: 'Invalid code' } }, 403))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    const { onSignOut } = mount(['two_factor'])
    fireEvent.click(screen.getByText('Set up authenticator')); await screen.findByText('TESTSETUPKEY')
    expect(screen.getByRole('img', { name: 'Authenticator setup QR code' })).toBeTruthy()
    expect(screen.queryByText('Private mailbox')).toBeNull()
    expect((screen.getByText('Verify and continue') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByLabelText('I have saved my recovery codes'))
    fireEvent.change(screen.getByLabelText('Authenticator code'), { target: { value: '000000' } })
    fireEvent.click(screen.getByText('Verify and continue')); await screen.findByRole('alert')
    expect(onSignOut).not.toHaveBeenCalled(); expect(screen.getByText(/recovery-one/)).toBeTruthy()
    expect(screen.getByRole('img', { name: 'Authenticator setup QR code' })).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Authenticator code'), { target: { value: '123456' } })
    fireEvent.click(screen.getByText('Verify and continue'))
    await waitFor(() => expect(onSignOut).toHaveBeenCalledOnce())
    expect(screen.queryByRole('img', { name: 'Authenticator setup QR code' })).toBeNull()
    expect(fetcher.mock.calls[2][0]).toBe('/api/v1/auth/2fa/confirm')
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual({ totp_code: '123456' })
  })
  it('removes provisioning secrets and QR on cancellation without confirming enrollment', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ data: {
      secret: 'TESTSETUPKEY', otpauth_url: 'otpauth://totp/test?secret=TESTSETUPKEY', recovery_codes: ['recovery-one'],
    } }))
    vi.stubGlobal('fetch', fetcher)
    const { onSignOut } = mount(['two_factor'])
    fireEvent.click(screen.getByText('Set up authenticator'))
    await screen.findByRole('img', { name: 'Authenticator setup QR code' })
    fireEvent.click(screen.getByText('Back to sign in'))
    expect(onSignOut).toHaveBeenCalledOnce()
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.queryByText('TESTSETUPKEY')).toBeNull()
    expect(fetcher).toHaveBeenCalledOnce()
  })
  it('moves a redeemed account into enrollment before private content', async () => {
    window.history.replaceState(null, '', '/login#reset_token=test-token')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ data: {
      user: { id: 1, email: 'admin@example.com' }, setup_required: ['two_factor'],
    } })))
    function Harness() {
      const [session, setSession] = useState<SetupSession | null>(null)
      return <AccountAccess apiBase="/api/v1" steps={session?.setup_required || []} onSession={setSession}
        onSignOut={() => {}}><p>Private mailbox</p></AccountAccess>
    }
    render(<Harness />); passwords(); fireEvent.click(screen.getByText('Set new password'))
    await screen.findByText('Set up authenticator')
    expect(screen.queryByText('Private mailbox')).toBeNull()
    expect(screen.queryByLabelText('Recovery token')).toBeNull()
  })
})

describe('self-service password reset', () => {
  function recoveryForm() {
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'alice@example.com' } })
    fireEvent.change(screen.getByLabelText('Authenticator code'), { target: { value: '123456' } })
    fireEvent.change(screen.getByLabelText('Recovery code'), { target: { value: 'saved-recovery-code' } })
    passwords()
  }

  it('opens from the login link hash without sending a request', () => {
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher)
    mount()
    window.history.replaceState(null, '', '/login#recover')
    fireEvent(window, new HashChangeEvent('hashchange'))
    expect(screen.getByRole('heading').textContent).toBe('Reset your password')
    expect(screen.getByText(/contact your server administrator/)).toBeTruthy()
    expect(screen.queryByLabelText('Recovery token')).toBeNull()
    expect(screen.queryByLabelText('Current password')).toBeNull()
    expect(fetcher).not.toHaveBeenCalled()
    expect(location.hash).toBe('')
  })

  it('requires both pre-registered factors before submitting', () => {
    window.history.replaceState(null, '', '/login#recover')
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher)
    mount(); passwords()
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'alice@example.com' } })
    const form = screen.getByText('Set new password').closest('form')!
    expect(form.checkValidity()).toBe(false)
    fireEvent.click(screen.getByText('Set new password'))
    expect(fetcher).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Authenticator code'), { target: { value: '123456' } })
    expect(form.checkValidity()).toBe(false)
    fireEvent.click(screen.getByText('Set new password'))
    expect(fetcher).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Recovery code'), { target: { value: 'saved-recovery-code' } })
    expect(form.checkValidity()).toBe(true)
  })

  // A browser's type="email" input rejects a non-ASCII local part, which would
  // lock internationalized (RFC 6531) accounts out of recovery.
  it('accepts an internationalized address', () => {
    window.history.replaceState(null, '', '/login#recover')
    vi.stubGlobal('fetch', vi.fn())
    mount(); recoveryForm()
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'jörg@例え.jp' } })
    const form = screen.getByText('Set new password').closest('form')!
    expect(form.checkValidity()).toBe(true)
  })

  it('posts both factors and the new password, then accepts the cookie session', async () => {
    window.history.replaceState(null, '', '/login#recover')
    const session = { user: { id: 1, email: 'alice@example.com' }, capabilities: ['mail.read'] }
    const fetcher = vi.fn().mockResolvedValue(response({ data: session }))
    vi.stubGlobal('fetch', fetcher)
    const { onSession, onSignOut } = mount(); recoveryForm()
    fireEvent.click(screen.getByText('Set new password'))
    await waitFor(() => expect(onSession).toHaveBeenCalledWith(session))
    expect(fetcher).toHaveBeenCalledOnce()
    const [url, init] = fetcher.mock.calls[0]
    expect(url).toBe('/api/v1/auth/recover')
    expect(init.credentials).toBe('include')
    expect(init.headers['X-CSRF-Token']).toBe('test-csrf')
    expect(JSON.parse(init.body)).toEqual({
      email: 'alice@example.com', totp_code: '123456', recovery_code: 'saved-recovery-code',
      new_password: 'a password chosen by the owner',
    })
    expect(onSignOut).not.toHaveBeenCalled()
    expect(screen.queryByLabelText('Recovery code')).toBeNull()
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0)
  })

  it.each([
    [401, 'Invalid address, authenticator code or recovery code'],
    [429, 'Too many failed recoveries for this address; try again later'],
  ])('keeps a %i refusal on the form without a session or automatic retry', async (status, message) => {
    window.history.replaceState(null, '', '/login#recover')
    const fetcher = vi.fn().mockResolvedValue(response({ error: { message } }, status))
    vi.stubGlobal('fetch', fetcher)
    const { onSession, onSignOut } = mount(); recoveryForm()
    fireEvent.click(screen.getByText('Set new password'))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe(message))
    expect(onSession).not.toHaveBeenCalled(); expect(onSignOut).not.toHaveBeenCalled()
    expect(fetcher).toHaveBeenCalledOnce()
    expect((screen.getByLabelText('Email') as HTMLInputElement).value).toBe('alice@example.com')
  })

  it('rejects mismatched passwords before spending a recovery code', () => {
    window.history.replaceState(null, '', '/login#recover')
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher)
    mount(); recoveryForm()
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'a different long password' } })
    fireEvent.click(screen.getByText('Set new password'))
    expect(screen.getByRole('alert').textContent).toContain('do not match')
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('clears recovery secrets on cancellation and starts the next attempt empty', () => {
    window.history.replaceState(null, '', '/login#recover')
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher)
    const { onSignOut } = mount(); recoveryForm()
    fireEvent.click(screen.getByText('Back to sign in'))
    expect(onSignOut).toHaveBeenCalledOnce()
    expect(screen.getByText('Private mailbox')).toBeTruthy()
    window.history.replaceState(null, '', '/login#recover')
    fireEvent(window, new HashChangeEvent('hashchange'))
    for (const label of ['Email', 'Authenticator code', 'Recovery code', 'New password', 'Confirm new password'])
      expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe('')
    expect(fetcher).not.toHaveBeenCalled()
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0)
  })
})
