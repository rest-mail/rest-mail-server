import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAdminUserStore } from '../../lib/stores/adminUserStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/admin-users/')({
  component: AdminUsersPage,
})

function AdminUsersPage() {
  const { accessToken } = useAuthStore()
  const { adminUsers, isLoading, error, fetchAdminUsers, deleteAdminUser, clearError } = useAdminUserStore()
  const [searchTerm, setSearchTerm] = useState('')
  const [deleteConfirmId, setDeleteConfirmId] = useState<number | null>(null)

  useEffect(() => {
    if (accessToken) {
      fetchAdminUsers(accessToken)
    }
  }, [accessToken, fetchAdminUsers])

  const filteredUsers = adminUsers.filter(user =>
    user.username.toLowerCase().includes(searchTerm.toLowerCase()) ||
    (user.email && user.email.toLowerCase().includes(searchTerm.toLowerCase()))
  )

  const handleDelete = async (id: number) => {
    if (!accessToken) return
    try {
      await deleteAdminUser(accessToken, id)
      setDeleteConfirmId(null)
    } catch (err) {
      console.error('Failed to delete admin user:', err)
    }
  }

  const getRoleBadgeColor = (roleName: string) => {
    switch (roleName.toLowerCase()) {
      case 'superadmin':
        return '#E42313' // Red
      case 'admin':
        return '#7A7A7A' // Gray
      case 'readonly':
        return '#22C55E' // Success
      default:
        return '#B0B0B0' // Muted
    }
  }

  return (
    <AppShell title="Admin Users">
      <div>
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Manage administrative users and their roles
            </p>
          </div>
          <Link
            to="/admin-users/new"
            className="h-10 px-5 flex items-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            + New Admin User
          </Link>
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

        {/* Content */}
        <div>
          {/* Search */}
          <div className="mb-6">
            <div
              className="h-11 px-4 border flex items-center"
              style={{ borderColor: 'var(--gray-border)', width: '400px' }}
            >
            <input
              type="text"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              placeholder="Search by username or email..."
              className="w-full outline-none text-sm"
              style={{ color: 'var(--black-soft)' }}
            />
          </div>
        </div>

        {/* Loading State */}
        {isLoading && (
          <div className="text-center py-12">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading admin users...
            </div>
          </div>
        )}

        {/* Admin Users Table */}
        {!isLoading && filteredUsers.length > 0 && (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead>
                <tr
                  className="border-b"
                  style={{
                    borderColor: 'var(--gray-border)',
                    backgroundColor: 'var(--bg-surface)',
                  }}
                >
                  <th
                    className="px-4 py-3 text-left text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Username
                  </th>
                  <th
                    className="px-4 py-3 text-left text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Email
                  </th>
                  <th
                    className="px-4 py-3 text-left text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Roles
                  </th>
                  <th
                    className="px-4 py-3 text-left text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Status
                  </th>
                  <th
                    className="px-4 py-3 text-left text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Created
                  </th>
                  <th
                    className="px-4 py-3 text-right text-[13px] font-medium"
                    style={{ color: 'var(--gray-secondary)' }}
                  >
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody>
                {filteredUsers.map((user) => (
                  <tr
                    key={user.id}
                    className="border-b hover:bg-gray-50 transition-colors"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="px-4 py-4">
                      <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                        {user.username}
                      </div>
                    </td>
                    <td className="px-4 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {user.email || '-'}
                      </div>
                    </td>
                    <td className="px-4 py-4">
                      <div className="flex gap-2 flex-wrap">
                        {user.roles && user.roles.length > 0 ? (
                          user.roles.map((role) => (
                            <span
                              key={role.id}
                              className="px-2 py-1 text-[11px] font-medium text-white"
                              style={{ backgroundColor: getRoleBadgeColor(role.name) }}
                            >
                              {role.name}
                            </span>
                          ))
                        ) : (
                          <span className="text-sm" style={{ color: 'var(--gray-muted)' }}>
                            No roles
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-4">
                      <span
                        className="px-2 py-1 text-[11px] font-medium"
                        style={{
                          backgroundColor: user.active ? '#ECFDF5' : '#FEF2F2',
                          color: user.active ? '#059669' : '#DC2626',
                        }}
                      >
                        {user.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="px-4 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {new Date(user.created_at).toLocaleDateString()}
                      </div>
                    </td>
                    <td className="px-4 py-4">
                      <div className="flex gap-3 justify-end">
                        <Link
                          to="/admin-users/$id"
                          params={{ id: user.id.toString() }}
                          className="text-sm font-medium hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          Edit
                        </Link>
                        <button
                          onClick={() => setDeleteConfirmId(user.id)}
                          className="text-sm font-medium hover:underline"
                          style={{ color: '#DC2626' }}
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Empty State */}
        {!isLoading && filteredUsers.length === 0 && (
          <div className="text-center py-12">
            <div className="text-sm mb-2" style={{ color: 'var(--gray-secondary)' }}>
              {searchTerm ? 'No admin users found matching your search' : 'No admin users yet'}
            </div>
            {!searchTerm && (
              <Link
                to="/admin-users/new"
                className="text-sm font-medium hover:underline"
                style={{ color: 'var(--red-primary)' }}
              >
                Create your first admin user
              </Link>
            )}
          </div>
        )}
      </div>
      </div>

      {/* Delete Confirmation Dialog */}
      {deleteConfirmId !== null && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
          <div className="bg-white p-6 w-[400px]" style={{ border: '1px solid var(--gray-border)' }}>
            <h2
              className="text-lg font-semibold mb-3"
              style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
            >
              Confirm Delete
            </h2>
            <p className="text-sm mb-6" style={{ color: 'var(--gray-secondary)' }}>
              Are you sure you want to delete this admin user? This action cannot be undone.
            </p>
            <div className="flex gap-3 justify-end">
              <button
                onClick={() => setDeleteConfirmId(null)}
                className="h-10 px-5 border text-sm font-medium"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--black-soft)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Cancel
              </button>
              <button
                onClick={() => handleDelete(deleteConfirmId)}
                className="h-10 px-5 text-white text-sm font-medium"
                style={{
                  backgroundColor: '#DC2626',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </AppShell>
  )
}
