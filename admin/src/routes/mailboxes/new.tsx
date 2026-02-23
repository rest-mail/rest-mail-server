import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/mailboxes/new')({
  component: NewMailboxPage,
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
}

function NewMailboxPage() {
  const { accessToken } = useAuthStore()
  const { createMailbox } = useMailboxStore()
  const navigate = useNavigate()

  const [formData, setFormData] = useState({
    email: '',
    password: '',
    confirmPassword: '',
    display_name: '',
    quota_bytes: 1073741824, // 1GB default
  })
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  const validateForm = (): boolean => {
    const newErrors: Record<string, string> = {}

    // Email validation
    if (!formData.email) {
      newErrors.email = 'Email is required'
    } else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(formData.email)) {
      newErrors.email = 'Invalid email format'
    }

    // Password validation
    if (!formData.password) {
      newErrors.password = 'Password is required'
    } else if (formData.password.length < 8) {
      newErrors.password = 'Password must be at least 8 characters'
    }

    // Confirm password validation
    if (formData.password !== formData.confirmPassword) {
      newErrors.confirmPassword = 'Passwords do not match'
    }

    // Quota validation
    if (formData.quota_bytes <= 0) {
      newErrors.quota_bytes = 'Quota must be greater than 0'
    }

    setErrors(newErrors)
    return Object.keys(newErrors).length === 0
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setApiError(null)

    if (!validateForm()) {
      return
    }

    if (!accessToken) {
      setApiError('Not authenticated')
      return
    }

    setIsSubmitting(true)

    try {
      await createMailbox(accessToken, {
        email: formData.email,
        password: formData.password,
        display_name: formData.display_name || undefined,
        quota_bytes: formData.quota_bytes,
      })

      // Navigate to mailboxes list on success
      navigate({ to: '/mailboxes' })
    } catch (err) {
      setApiError(err instanceof Error ? err.message : 'Failed to create mailbox')
    } finally {
      setIsSubmitting(false)
    }
  }

  const handleQuotaPreset = (bytes: number) => {
    setFormData((prev) => ({ ...prev, quota_bytes: bytes }))
  }

  return (
    <AppShell title="Create Mailbox">
      <div>
        {/* Header */}
        <div className="mb-8">
          <Link
            to="/mailboxes"
            className="text-sm mb-4 inline-block hover:underline"
            style={{ color: 'var(--gray-secondary)' }}
          >
            ← Back to Mailboxes
          </Link>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Create a new email account
          </p>
        </div>

        {/* API Error Message */}
        {apiError && (
          <div
            className="p-4 mb-6 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {apiError}
          </div>
        )}

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-6">
          <div
            className="p-6 border"
            style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)' }}
          >
            <div className="space-y-5">
              {/* Email */}
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Email Address *
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{
                    borderColor: errors.email ? '#EF4444' : 'var(--gray-border)',
                    backgroundColor: 'white',
                  }}
                >
                  <input
                    type="email"
                    value={formData.email}
                    onChange={(e) => {
                      setFormData((prev) => ({ ...prev, email: e.target.value }))
                      setErrors((prev) => ({ ...prev, email: '' }))
                    }}
                    placeholder="user@example.com"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
                {errors.email && (
                  <div className="text-xs" style={{ color: '#DC2626' }}>
                    {errors.email}
                  </div>
                )}
              </div>

              {/* Display Name */}
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Display Name
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{ borderColor: 'var(--gray-border)', backgroundColor: 'white' }}
                >
                  <input
                    type="text"
                    value={formData.display_name}
                    onChange={(e) =>
                      setFormData((prev) => ({ ...prev, display_name: e.target.value }))
                    }
                    placeholder="John Doe"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
                <div className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Optional: The name that appears in emails
                </div>
              </div>

              {/* Password */}
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Password *
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{
                    borderColor: errors.password ? '#EF4444' : 'var(--gray-border)',
                    backgroundColor: 'white',
                  }}
                >
                  <input
                    type="password"
                    value={formData.password}
                    onChange={(e) => {
                      setFormData((prev) => ({ ...prev, password: e.target.value }))
                      setErrors((prev) => ({ ...prev, password: '' }))
                    }}
                    placeholder="••••••••"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
                {errors.password && (
                  <div className="text-xs" style={{ color: '#DC2626' }}>
                    {errors.password}
                  </div>
                )}
              </div>

              {/* Confirm Password */}
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Confirm Password *
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{
                    borderColor: errors.confirmPassword ? '#EF4444' : 'var(--gray-border)',
                    backgroundColor: 'white',
                  }}
                >
                  <input
                    type="password"
                    value={formData.confirmPassword}
                    onChange={(e) => {
                      setFormData((prev) => ({ ...prev, confirmPassword: e.target.value }))
                      setErrors((prev) => ({ ...prev, confirmPassword: '' }))
                    }}
                    placeholder="••••••••"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
                {errors.confirmPassword && (
                  <div className="text-xs" style={{ color: '#DC2626' }}>
                    {errors.confirmPassword}
                  </div>
                )}
              </div>

              {/* Quota */}
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Quota *
                </label>
                <div
                  className="h-11 px-4 flex items-center border"
                  style={{
                    borderColor: errors.quota_bytes ? '#EF4444' : 'var(--gray-border)',
                    backgroundColor: 'white',
                  }}
                >
                  <input
                    type="number"
                    value={formData.quota_bytes}
                    onChange={(e) => {
                      setFormData((prev) => ({ ...prev, quota_bytes: parseInt(e.target.value) || 0 }))
                      setErrors((prev) => ({ ...prev, quota_bytes: '' }))
                    }}
                    min="0"
                    className="w-full outline-none text-sm"
                    style={{ color: 'var(--black-soft)' }}
                  />
                </div>
                <div className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  {formatBytes(formData.quota_bytes)} - Storage limit in bytes
                </div>
                {errors.quota_bytes && (
                  <div className="text-xs" style={{ color: '#DC2626' }}>
                    {errors.quota_bytes}
                  </div>
                )}

                {/* Quota Presets */}
                <div className="flex flex-wrap gap-2 mt-2">
                  <button
                    type="button"
                    onClick={() => handleQuotaPreset(536870912)} // 512MB
                    className="px-3 py-1 text-xs border hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  >
                    512 MB
                  </button>
                  <button
                    type="button"
                    onClick={() => handleQuotaPreset(1073741824)} // 1GB
                    className="px-3 py-1 text-xs border hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  >
                    1 GB
                  </button>
                  <button
                    type="button"
                    onClick={() => handleQuotaPreset(2147483648)} // 2GB
                    className="px-3 py-1 text-xs border hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  >
                    2 GB
                  </button>
                  <button
                    type="button"
                    onClick={() => handleQuotaPreset(5368709120)} // 5GB
                    className="px-3 py-1 text-xs border hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  >
                    5 GB
                  </button>
                  <button
                    type="button"
                    onClick={() => handleQuotaPreset(10737418240)} // 10GB
                    className="px-3 py-1 text-xs border hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  >
                    10 GB
                  </button>
                </div>
              </div>
            </div>
          </div>

          {/* Actions */}
          <div className="flex gap-3">
            <button
              type="submit"
              disabled={isSubmitting}
              className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium"
              style={{
                backgroundColor: 'var(--red-primary)',
                fontFamily: 'Space Grotesk',
                opacity: isSubmitting ? 0.6 : 1,
                cursor: isSubmitting ? 'not-allowed' : 'pointer',
              }}
            >
              {isSubmitting ? 'Creating...' : 'Create Mailbox'}
            </button>
            <Link
              to="/mailboxes"
              className="h-11 px-6 flex items-center justify-center text-sm font-medium border"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--black-soft)',
                fontFamily: 'Space Grotesk',
              }}
            >
              Cancel
            </Link>
          </div>
        </form>
      </div>
    </AppShell>
  )
}
