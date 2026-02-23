import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { useAdminUserStore } from '../../lib/stores/adminUserStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/admin-users/$id')({
  component: AdminUserDetailsPage,
})

function AdminUserDetailsPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { adminUsers, roles, capabilities, fetchAdminUsers, fetchRoles, fetchCapabilities, updateAdminUser, isLoading, error, clearError } = useAdminUserStore()

  const [formData, setFormData] = useState({
    email: '',
    password: '',
    confirmPassword: '',
    active: true,
    password_change_required: false,
    role_ids: [] as number[],
  })

  const [validationErrors, setValidationErrors] = useState<Record<string, string>>({})
  const [showPasswordFields, setShowPasswordFields] = useState(false)

  const adminUser = adminUsers.find(u => u.id === parseInt(id))

  useEffect(() => {
    if (accessToken) {
      fetchAdminUsers(accessToken)
      fetchRoles(accessToken)
      fetchCapabilities(accessToken)
    }
    clearError()
  }, [accessToken, fetchAdminUsers, fetchRoles, fetchCapabilities, clearError])

  useEffect(() => {
    if (adminUser) {
      setFormData({
        email: adminUser.email || '',
        password: '',
        confirmPassword: '',
        active: adminUser.active,
        password_change_required: adminUser.password_change_required,
        role_ids: adminUser.roles?.map(r => r.id) || [],
      })
    }
  }, [adminUser])

  const validateForm = () => {
    const errors: Record<string, string> = {}

    if (formData.email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(formData.email)) {
      errors.email = 'Invalid email format'
    }

    if (showPasswordFields) {
      if (!formData.password) {
        errors.password = 'Password is required'
      } else if (formData.password.length < 8) {
        errors.password = 'Password must be at least 8 characters'
      }

      if (formData.password !== formData.confirmPassword) {
        errors.confirmPassword = 'Passwords do not match'
      }
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
      const updateData: any = {
        email: formData.email || undefined,
        active: formData.active,
        password_change_required: formData.password_change_required,
        role_ids: formData.role_ids.length > 0 ? formData.role_ids : undefined,
      }

      if (showPasswordFields && formData.password) {
        updateData.password = formData.password
      }

      await updateAdminUser(accessToken, parseInt(id), updateData)

      // Redirect to admin users list on success
      navigate({ to: '/admin-users' })
    } catch (err) {
      console.error('Failed to update admin user:', err)
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

  // Compute user capabilities from assigned roles
  const userCapabilities = new Set<string>()
  if (adminUser?.roles) {
    roles
      .filter(role => formData.role_ids.includes(role.id))
      .forEach(role => {
        capabilities.forEach(cap => {
          // This is a simplification - in reality, we'd need to fetch role capabilities
          userCapabilities.add(cap.name)
        })
      })
  }

  if (!adminUser) {
    return (
      <AppShell title="Edit Admin User">
        <div className="text-center py-12">
          <div className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
            {isLoading ? 'Loading...' : 'Admin user not found'}
          </div>
          <button
            onClick={() => navigate({ to: '/admin-users' })}
            className="text-sm font-medium hover:underline"
            style={{ color: 'var(--red-primary)' }}
          >
            Back to Admin Users
          </button>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title={`Edit Admin User: ${adminUser.username}`}>
      <div>
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
            {/* User Info Card */}
            <div
              className="p-4 border"
              style={{
                borderColor: 'var(--gray-border)',
                backgroundColor: 'var(--bg-surface)',
              }}
            >
              <div className="grid grid-cols-2 gap-4 text-sm">
                <div>
                  <div className="font-medium mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    Username
                  </div>
                  <div style={{ color: 'var(--black-soft)' }}>{adminUser.username}</div>
                </div>
                <div>
                  <div className="font-medium mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    Created
                  </div>
                  <div style={{ color: 'var(--black-soft)' }}>
                    {new Date(adminUser.created_at).toLocaleDateString()}
                  </div>
                </div>
                {adminUser.last_password_change && (
                  <div>
                    <div className="font-medium mb-1" style={{ color: 'var(--gray-secondary)' }}>
                      Last Password Change
                    </div>
                    <div style={{ color: 'var(--black-soft)' }}>
                      {new Date(adminUser.last_password_change).toLocaleDateString()}
                    </div>
                  </div>
                )}
              </div>
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

            {/* Password Reset Section */}
            <div
              className="p-4 border flex flex-col gap-4"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-sm font-medium mb-1" style={{ color: 'var(--black-soft)' }}>
                    Reset Password
                  </div>
                  <div className="text-[12px]" style={{ color: 'var(--gray-secondary)' }}>
                    Set a new password for this user
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => setShowPasswordFields(!showPasswordFields)}
                  className="text-sm font-medium hover:underline"
                  style={{ color: 'var(--red-primary)' }}
                >
                  {showPasswordFields ? 'Cancel' : 'Change Password'}
                </button>
              </div>

              {showPasswordFields && (
                <div className="flex flex-col gap-4 pt-4 border-t" style={{ borderColor: 'var(--gray-border)' }}>
                  <div className="flex flex-col gap-2">
                    <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                      New Password
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
                  </div>

                  <div className="flex flex-col gap-2">
                    <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                      Confirm New Password
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
                </div>
              )}
            </div>

            {/* Status Toggles */}
            <div className="flex flex-col gap-3">
              <label className="flex items-center gap-3 cursor-pointer">
                <input
                  type="checkbox"
                  checked={formData.active}
                  onChange={(e) => setFormData({ ...formData, active: e.target.checked })}
                />
                <div>
                  <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Active
                  </div>
                  <div className="text-[12px]" style={{ color: 'var(--gray-secondary)' }}>
                    Allow this user to log in
                  </div>
                </div>
              </label>

              <label className="flex items-center gap-3 cursor-pointer">
                <input
                  type="checkbox"
                  checked={formData.password_change_required}
                  onChange={(e) => setFormData({ ...formData, password_change_required: e.target.checked })}
                />
                <div>
                  <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Require Password Change
                  </div>
                  <div className="text-[12px]" style={{ color: 'var(--gray-secondary)' }}>
                    User must change password on next login
                  </div>
                </div>
              </label>
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
                {isLoading ? 'Saving...' : 'Save Changes'}
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
