import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { AppShell } from '../../components/layout/AppShell'
import { useAuthStore } from '../../lib/stores/authStore'
import {
  getTwoFactorStatus,
  enrollTwoFactor,
  confirmTwoFactor,
  disableTwoFactor,
  type TwoFactorStatus,
  type TwoFactorEnrollment,
} from '../../lib/twoFactor'

export const Route = createFileRoute('/settings/two-factor')({
  component: TwoFactorPage,
})

/**
 * TwoFactorPage surfaces the server's TOTP 2FA (two_factor.go) for the signed-in
 * admin account: enabled/disabled status plus the enable (enroll → show secret +
 * recovery codes → confirm a code) and disable (prove a code) flows against the
 * /auth/2fa endpoints. A QR image is intentionally omitted; the base32 secret
 * and otpauth URL are shown for manual entry into an authenticator app.
 */
function TwoFactorPage() {
  const navigate = useNavigate()
  const { isAuthenticated } = useAuthStore()

  const [status, setStatus] = useState<TwoFactorStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // Enable flow
  const [enrollment, setEnrollment] = useState<TwoFactorEnrollment | null>(null)
  const [confirmCode, setConfirmCode] = useState('')

  // Disable flow
  const [disabling, setDisabling] = useState(false)
  const [disableCode, setDisableCode] = useState('')
  const [disableUseRecovery, setDisableUseRecovery] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setStatus(await getTwoFactorStatus())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load 2FA status')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }
    void refresh()
  }, [isAuthenticated, navigate, refresh])

  async function startEnroll() {
    setBusy(true)
    setError(null)
    try {
      setEnrollment(await enrollTwoFactor())
      setConfirmCode('')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to start enrollment')
    } finally {
      setBusy(false)
    }
  }

  async function confirmEnroll() {
    const code = confirmCode.trim()
    if (!code) return
    setBusy(true)
    setError(null)
    try {
      await confirmTwoFactor(code)
      setEnrollment(null)
      setConfirmCode('')
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Invalid code')
    } finally {
      setBusy(false)
    }
  }

  async function disable() {
    const code = disableCode.trim()
    if (!code) return
    setBusy(true)
    setError(null)
    try {
      await disableTwoFactor(disableUseRecovery ? { recovery_code: code } : { totp_code: code })
      setDisabling(false)
      setDisableCode('')
      setDisableUseRecovery(false)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Invalid code')
    } finally {
      setBusy(false)
    }
  }

  const enabled = status?.enabled ?? false

  return (
    <AppShell title="Two-Factor Authentication" backLink="/settings">
      <div className="max-w-2xl">
        <p className="text-sm mb-6" style={{ color: 'var(--gray-secondary)' }}>
          Require a time-based one-time code from an authenticator app when signing in to your admin
          account.
        </p>

        {error && (
          <div
            className="p-4 border mb-6 text-sm rounded"
            style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2', color: '#DC2626' }}
          >
            {error}
          </div>
        )}

        <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="flex items-center justify-between mb-4">
            <h2
              className="text-lg font-semibold"
              style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
            >
              Status
            </h2>
            <span
              className="inline-block px-2 py-1 text-xs font-medium rounded"
              style={
                enabled
                  ? { backgroundColor: '#DCFCE7', color: '#166534' }
                  : { backgroundColor: 'var(--bg-surface)', color: 'var(--gray-secondary)' }
              }
            >
              {loading ? 'Checking…' : enabled ? 'Enabled' : 'Disabled'}
            </span>
          </div>

          {loading ? (
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>Loading…</p>
          ) : enabled ? (
            /* ── Enabled: offer disable ─────────────────────────────── */
            !disabling ? (
              <div className="flex items-center justify-between gap-4">
                <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                  Two-factor authentication is protecting your account.
                </p>
                <button
                  onClick={() => { setDisabling(true); setError(null) }}
                  className="h-10 px-6 text-sm font-medium border rounded"
                  style={{ borderColor: '#EF4444', color: '#DC2626' }}
                >
                  Disable
                </button>
              </div>
            ) : (
              <div className="space-y-3">
                <label className="block text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  {disableUseRecovery ? 'Recovery code' : 'Authenticator code'}
                </label>
                <input
                  value={disableCode}
                  onChange={(e) => setDisableCode(e.target.value)}
                  placeholder={disableUseRecovery ? 'one of your recovery codes' : '123456'}
                  inputMode={disableUseRecovery ? 'text' : 'numeric'}
                  autoComplete="one-time-code"
                  className="w-full h-11 px-4 border rounded font-mono"
                  style={{ borderColor: 'var(--gray-border)' }}
                />
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Confirm with a current code to turn 2FA off.{' '}
                  <button
                    type="button"
                    className="hover:underline"
                    style={{ color: 'var(--red-primary)' }}
                    onClick={() => { setDisableUseRecovery((v) => !v); setDisableCode('') }}
                  >
                    {disableUseRecovery ? 'Use an authenticator code' : 'Use a recovery code instead'}
                  </button>
                </p>
                <div className="flex gap-3">
                  <button
                    onClick={disable}
                    disabled={busy || !disableCode.trim()}
                    className="h-10 px-6 text-sm font-medium text-white rounded"
                    style={{ backgroundColor: busy ? 'var(--gray-muted)' : '#DC2626' }}
                  >
                    {busy ? 'Disabling…' : 'Confirm disable'}
                  </button>
                  <button
                    onClick={() => { setDisabling(false); setDisableCode(''); setDisableUseRecovery(false); setError(null) }}
                    className="h-10 px-6 text-sm font-medium border rounded"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            )
          ) : enrollment ? (
            /* ── Enrolling: show secret + recovery codes, confirm a code ── */
            <div className="space-y-5">
              <div className="space-y-2">
                <p className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  1. Add this secret to your authenticator app
                </p>
                <CopyRow value={enrollment.secret} mono />
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Or use the provisioning URL:
                </p>
                <CopyRow value={enrollment.otpauth_url} mono small />
              </div>

              <div className="space-y-2">
                <p className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  2. Save your recovery codes
                </p>
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Store these somewhere safe. Each can be used once if you lose your authenticator.
                  They are shown only now.
                </p>
                <div
                  className="grid grid-cols-2 gap-2 border p-3 rounded"
                  style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)' }}
                >
                  {enrollment.recovery_codes.map((c) => (
                    <code key={c} className="text-xs font-mono" style={{ color: 'var(--black-soft)' }}>{c}</code>
                  ))}
                </div>
                <CopyRow value={enrollment.recovery_codes.join('\n')} label="Copy all recovery codes" />
              </div>

              <div className="space-y-2">
                <p className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  3. Enter a code from the app to finish
                </p>
                <input
                  value={confirmCode}
                  onChange={(e) => setConfirmCode(e.target.value)}
                  placeholder="123456"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  className="w-full h-11 px-4 border rounded font-mono"
                  style={{ borderColor: 'var(--gray-border)' }}
                />
                <div className="flex gap-3">
                  <button
                    onClick={confirmEnroll}
                    disabled={busy || !confirmCode.trim()}
                    className="h-10 px-6 text-sm font-medium text-white rounded"
                    style={{ backgroundColor: busy ? 'var(--gray-muted)' : 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
                  >
                    {busy ? 'Verifying…' : 'Enable 2FA'}
                  </button>
                  <button
                    onClick={() => { setEnrollment(null); setConfirmCode(''); setError(null) }}
                    className="h-10 px-6 text-sm font-medium border rounded"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            </div>
          ) : (
            /* ── Disabled: offer enable ─────────────────────────────── */
            <div className="flex items-center justify-between gap-4">
              <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                Add an extra layer of security to your account.
              </p>
              <button
                onClick={startEnroll}
                disabled={busy}
                className="h-10 px-6 text-sm font-medium text-white rounded"
                style={{ backgroundColor: busy ? 'var(--gray-muted)' : 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
              >
                {busy ? 'Starting…' : 'Enable 2FA'}
              </button>
            </div>
          )}
        </div>
      </div>
    </AppShell>
  )
}

// ── Copy-to-clipboard row ────────────────────────────────────────────

function CopyRow({ value, label, mono, small }: {
  value: string
  label?: string
  mono?: boolean
  small?: boolean
}) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard unavailable — the value is still visible to select manually */
    }
  }

  return (
    <div className="flex items-center gap-2">
      {!label && (
        <code
          className={
            'flex-1 min-w-0 truncate border px-3 py-2 rounded ' +
            (mono ? 'font-mono ' : '') + (small ? 'text-[11px] ' : 'text-xs ')
          }
          style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)', color: 'var(--black-soft)' }}
          title={value}
        >
          {value}
        </code>
      )}
      <button
        onClick={copy}
        className="h-9 px-4 text-xs font-medium border rounded shrink-0"
        style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
      >
        {copied ? 'Copied' : label ?? 'Copy'}
      </button>
    </div>
  )
}
