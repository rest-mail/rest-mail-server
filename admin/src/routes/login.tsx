import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useAuthStore } from '../lib/stores/authStore'

export const Route = createFileRoute('/login')({
  component: LoginPage,
})

function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const { login, isLoading, error, clearError } = useAuthStore()
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    clearError()

    try {
      await login(username, password)
      // Redirect to dashboard on successful login
      navigate({ to: '/dashboard' })
    } catch (err) {
      // Error is handled by the store
      console.error('Login failed:', err)
    }
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
            Sign in to your account
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
            {isLoading ? 'Signing in...' : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  )
}
