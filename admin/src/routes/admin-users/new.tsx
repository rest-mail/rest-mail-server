import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { useAdminUserStore } from '../../lib/stores/adminUserStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/admin-users/new')({
  component: NewAdminUserPage,
})

function NewAdminUserPage() {
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { createAdminUser, roles, fetchRoles, isLoading, error, clearError } = useAdminUserStore()

  const [formData, setFormData] = useState({
    username: '',
    email: '',
    password: '',
    confirmPassword: '',
    role_ids: [] as number[],
  })

  const [validationErrors, setValidationErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    if (accessToken) {
      fetchRoles(accessToken)
    }
    clearError()
  }, [accessToken, fetchRoles, clearError])

  const validateForm = () => {
    const errors: Record<string, string> = {}

    if (!formData.username.trim()) {
      errors.username = 'Username is required'
    } else if (formData.username.length < 3) {
      errors.username = 'Username must be at least 3 characters'
    }

    if (formData.email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(formData.email)) {
      errors.email = 'Invalid email format'
    }

    if (!formData.password) {
      errors.password = 'Password is required'
    } else if (formData.password.length < 8) {
      errors.password = 'Password must be at least 8 characters'
    }

    if (formData.password !== formData.confirmPassword) {
      errors.confirmPassword = 'Passwords do not match'
    }

    setValidationErrors(errors)
    return Object.keys(errors).length === 0
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    clearError()

    if (!validateForm()) {
      return
    }

    if (!accessToken) return

    try {
      await createAdminUser(accessToken, {
        username: formData.username,
        email: formData.email || undefined,
        password: formData.password,
        role_ids: formData.role_ids.length > 0 ? formData.role_ids : undefined,
      })

      // Redirect to admin users list on success
      navigate({ to: '/admin-users' })
    } catch (err) {
      console.error('Failed to create admin user:', err)
    }
  }

  const handleRoleToggle = (roleId: number) => {
    setFormData(prev => ({
      ...prev,
      role_ids: prev.role_ids.includes(roleId)
        ? prev.role_ids.filter(id => id !== roleId)
        : [...prev.role_ids, roleId],
    }))
  }

  return (
    <AppShell title="Create Admin User">
      <div>
        <div className="mb-6">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Add a new administrative user to the system
          </p>
        </div>

        {/* Error Message */}
        {error && (
          <div className="mb-6">
            <div
              className="p-4 border flex items-center justify-between rounded"
              style={{
                borderColor: '#EF4444',
                backgroundColor: '#FEF2F2',
                color: '#DC2626',
              }}
            >
              <span className="text-sm">{error}</span>
              <button
                onClick={clearError}
                className="text-sm font-medium hover:underline"
              >
                Dismiss
              </button>
            </div>
          </div>
        )}

        {/* Form */}
        <div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-6">
            {/* Username */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Username <span style={{ color: 'var(--red-primary)' }}>*</span>
              </label>
              <div
                className="h-11 px-4 border flex items-center"
                style={{
                  borderColor: validationErrors.username ? '#EF4444' : 'var(--gray-border)',
                }}
              >
                <input
                  type="text"
                  value={formData.username}
                  onChange={(e) => setFormData({ ...formData, username: e.target.value })}
                  placeholder="admin"
                  className="w-full outline-none text-sm"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              {validationErrors.username && (
                <p className="text-[12px]" style={{ color: '#DC2626' }}>
                  {validationErrors.username}
                </p>
              )}
            </div>

            {/* Email */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Email (Optional)
              </label>
              <div
                className="h-11 px-4 border flex items-center"
                style={{
                  borderColor: validationErrors.email ? '#EF4444' : 'var(--gray-border)',
                }}
              >
                <input
                  type="email"
                  value={formData.email}
                  onChange={(e) => setFormData({ ...formData, email: e.target.value })}
                  placeholder="admin@example.com"
                  className="w-full outline-none text-sm"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              {validationErrors.email && (
                <p className="text-[12px]" style={{ color: '#DC2626' }}>
                  {validationErrors.email}
                </p>
              )}
            </div>

            {/* Password */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Password <span style={{ color: 'var(--red-primary)' }}>*</span>
              </label>
              <div
                className="h-11 px-4 border flex items-center"
                style={{
                  borderColor: validationErrors.password ? '#EF4444' : 'var(--gray-border)',
                }}
              >
                <input
                  type="password"
                  value={formData.password}
                  onChange={(e) => setFormData({ ...formData, password: e.target.value })}
                  placeholder="••••••••"
                  className="w-full outline-none text-sm"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              {validationErrors.password && (
                <p className="text-[12px]" style={{ color: '#DC2626' }}>
                  {validationErrors.password}
                </p>
              )}
              <p className="text-[12px]" style={{ color: 'var(--gray-secondary)' }}>
                Minimum 8 characters
              </p>
            </div>

            {/* Confirm Password */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Confirm Password <span style={{ color: 'var(--red-primary)' }}>*</span>
              </label>
              <div
                className="h-11 px-4 border flex items-center"
                style={{
                  borderColor: validationErrors.confirmPassword ? '#EF4444' : 'var(--gray-border)',
                }}
              >
                <input
                  type="password"
                  value={formData.confirmPassword}
                  onChange={(e) => setFormData({ ...formData, confirmPassword: e.target.value })}
                  placeholder="••••••••"
                  className="w-full outline-none text-sm"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              {validationErrors.confirmPassword && (
                <p className="text-[12px]" style={{ color: '#DC2626' }}>
                  {validationErrors.confirmPassword}
                </p>
              )}
            </div>

            {/* Role Selection */}
            <div className="flex flex-col gap-3">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Roles
              </label>
              <div
                className="p-4 border flex flex-col gap-3"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                {roles.length === 0 ? (
                  <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    No roles available
                  </p>
                ) : (
                  roles.map((role) => (
                    <label
                      key={role.id}
                      className="flex items-start gap-3 cursor-pointer hover:bg-gray-50 p-2 -m-2"
                    >
                      <input
                        type="checkbox"
                        checked={formData.role_ids.includes(role.id)}
                        onChange={() => handleRoleToggle(role.id)}
                        className="mt-0.5"
                      />
                      <div className="flex-1">
                        <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                          {role.name}
                          {role.system_role && (
                            <span
                              className="ml-2 text-[11px] px-2 py-0.5"
                              style={{
                                backgroundColor: 'var(--bg-surface)',
                                color: 'var(--gray-secondary)',
                              }}
                            >
                              System
                            </span>
                          )}
                        </div>
                        {role.description && (
                          <div className="text-[12px] mt-1" style={{ color: 'var(--gray-secondary)' }}>
                            {role.description}
                          </div>
                        )}
                      </div>
                    </label>
                  ))
                )}
              </div>
            </div>

            {/* Actions */}
            <div
              className="flex gap-3 pt-4 border-t"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <button
                type="submit"
                disabled={isLoading}
                className="h-11 px-6 text-white text-sm font-medium"
                style={{
                  backgroundColor: 'var(--red-primary)',
                  fontFamily: 'Space Grotesk',
                  opacity: isLoading ? 0.6 : 1,
                  cursor: isLoading ? 'not-allowed' : 'pointer',
                }}
              >
                {isLoading ? 'Creating...' : 'Create Admin User'}
              </button>
              <button
                type="button"
                onClick={() => navigate({ to: '/admin-users' })}
                className="h-11 px-6 border text-sm font-medium"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--black-soft)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Cancel
              </button>
            </div>
          </form>
        </div>
      </div>
    </AppShell>
  )
}
