import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useAuthStore } from '../lib/stores/authStore'

export const Route = createFileRoute('/login')({
  component: LoginPage,
})

function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [useRecovery, setUseRecovery] = useState(false)
  const { login, isLoading, error, clearError, totpRequired, resetTotpChallenge } = useAuthStore()
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    clearError()

    try {
      if (totpRequired) {
        const trimmed = code.trim()
        if (!trimmed) return
        await login(username, password, useRecovery ? { recovery_code: trimmed } : { totp_code: trimmed })
      } else {
        await login(username, password)
      }
      // Only navigate once actually authenticated. A 2FA-active account returns
      // from the first attempt with a totp_required challenge (not authenticated
      // yet), so we stay on this page and collect the code.
      if (useAuthStore.getState().isAuthenticated) {
        navigate({ to: '/dashboard' })
      }
    } catch (err) {
      // Error is handled by the store
      console.error('Login failed:', err)
    }
  }

  const backToCredentials = () => {
    resetTotpChallenge()
    setCode('')
    setUseRecovery(false)
  }

  return (
    <div className="min-h-screen bg-white flex items-center justify-center">
      <div className="w-[300px] flex flex-col gap-8">
        {/* Logo and Title */}
        <div className="flex flex-col items-center gap-3">
          <div className="w-10 h-10" style={{ backgroundColor: 'var(--red-primary)' }} />
          <h1 className="text-2xl font-semibold" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
            REST Mail Admin
          </h1>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            {totpRequired ? 'Two-factor authentication' : 'Sign in to your account'}
          </p>
        </div>

        {/* Error Message */}
        {error && (
          <div
            className="p-3 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
        )}

        {/* Login Form */}
        <form onSubmit={handleSubmit} className="flex flex-col gap-5">
          {!totpRequired && (
            <>
              {/* Username Field */}
              <div className="flex flex-col gap-2">
                <label className="text-[13px]" style={{ color: 'var(--black-soft)' }}>
                  Username or Email
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <input
                    type="text"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder="admin"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
              </div>

              {/* Password Field */}
              <div className="flex flex-col gap-2">
                <label className="text-[13px]" style={{ color: 'var(--black-soft)' }}>
                  Password
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <input
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="••••••••"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
              </div>
            </>
          )}

          {totpRequired && (
            /* Second step: the account has 2FA active, so the API asked for a
               code. Credentials stay in state and are resubmitted with it. */
            <div className="flex flex-col gap-2">
              <label className="text-[13px]" style={{ color: 'var(--black-soft)' }}>
                {useRecovery ? 'Recovery code' : 'Authenticator code'}
              </label>
              <div
                className="h-11 px-4 flex items-center border"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                <input
                  type="text"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder={useRecovery ? 'one of your recovery codes' : '123456'}
                  inputMode={useRecovery ? 'text' : 'numeric'}
                  autoComplete="one-time-code"
                  autoFocus
                  className="w-full outline-none text-sm font-mono"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              <div className="flex items-center justify-between text-[12px]">
                <button
                  type="button"
                  onClick={() => { setUseRecovery((v) => !v); setCode('') }}
                  className="hover:underline"
                  style={{ color: 'var(--red-primary)' }}
                >
                  {useRecovery ? 'Use authenticator code' : 'Use a recovery code'}
                </button>
                <button
                  type="button"
                  onClick={backToCredentials}
                  className="hover:underline"
                  style={{ color: 'var(--gray-secondary)' }}
                >
                  ← Back
                </button>
              </div>
            </div>
          )}

          {/* Sign In Button */}
          <button
            type="submit"
            disabled={isLoading}
            className="h-11 flex items-center justify-center text-white text-[13px] font-medium"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
              opacity: isLoading ? 0.6 : 1,
              cursor: isLoading ? 'not-allowed' : 'pointer',
            }}
          >
            {isLoading
              ? (totpRequired ? 'Verifying...' : 'Signing in...')
              : (totpRequired ? 'Verify' : 'Sign In')}
          </button>
        </form>
      </div>
    </div>
  )
}
