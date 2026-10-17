import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AuthenticatorSetup } from './AuthenticatorSetup'

const secret = 'JBSWY3DPEHPK3PXP'
const otpauthUrl = `otpauth://totp/REST%20Mail:admin%40example.com?secret=${secret}&issuer=REST%20Mail`

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('authenticator provisioning', () => {
  it('renders a local inline QR with an accessible name and a collapsed manual fallback', () => {
    const fetcher = vi.fn()
    vi.stubGlobal('fetch', fetcher)
    const { container } = render(<AuthenticatorSetup secret={secret} otpauthUrl={otpauthUrl} />)
    expect(screen.getByRole('img', { name: 'Authenticator setup QR code' }).tagName).toBe('svg')
    expect(container.querySelector('svg path')).not.toBeNull()
    expect(container.querySelector('details')?.open).toBe(false)
    expect(container.querySelector('img, image, canvas, a')).toBeNull()
    expect(container.innerHTML).not.toContain(otpauthUrl)
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('copies the exact manual key only when requested', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render(<AuthenticatorSetup secret={secret} otpauthUrl={otpauthUrl} />)
    expect(writeText).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('Can’t scan? Enter a setup key'))
    expect(screen.getByText(secret)).toBeTruthy()
    fireEvent.click(screen.getByText('Copy setup key'))
    await waitFor(() => expect(screen.getByRole('status').textContent).toBe('Setup key copied.'))
    expect(writeText).toHaveBeenCalledExactlyOnceWith(secret)
  })

  it('keeps manual entry available when clipboard access is denied', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    render(<AuthenticatorSetup secret={secret} otpauthUrl={otpauthUrl} />)
    fireEvent.click(screen.getByText('Copy setup key'))
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('copy it manually'))
    expect(screen.getByText(secret)).toBeTruthy()
  })

  it('opens manual entry if the server has no provisioning URI', () => {
    const { container } = render(<AuthenticatorSetup secret={secret} otpauthUrl="" />)
    expect(screen.queryByRole('img')).toBeNull()
    expect(container.querySelector('details')?.open).toBe(true)
    expect(screen.getByText(secret)).toBeTruthy()
  })
})
