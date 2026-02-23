import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/domains/')({
  component: DomainsPage,
})

function DomainsPage() {
  const navigate = useNavigate()
  const { domains, fetchDomains, deleteDomain, isLoading, error, clearError } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [searchTerm, setSearchTerm] = useState('')
  const [filterActive, setFilterActive] = useState<boolean | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState<number | null>(null)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDomains(accessToken).catch((err) => {
        console.error('Failed to fetch domains:', err)
      })
    }
  }, [isAuthenticated, accessToken, navigate, fetchDomains])

  const handleDelete = async (id: number) => {
    if (!accessToken) return

    try {
      await deleteDomain(id.toString(), accessToken)
      setDeleteConfirm(null)
    } catch (err) {
      console.error('Failed to delete domain:', err)
    }
  }

  const filteredDomains = domains.filter((domain) => {
    const matchesSearch = domain.name.toLowerCase().includes(searchTerm.toLowerCase())
    const matchesFilter = filterActive === null || domain.active === filterActive
    return matchesSearch && matchesFilter
  })

  return (
    <AppShell title="Domains">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage email domains and DNS configuration
          </p>
        </div>
        <Link
          to="/domains/new"
          className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
          style={{
            backgroundColor: 'var(--red-primary)',
            fontFamily: 'Space Grotesk',
          }}
        >
          Add Domain
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
              className="text-sm font-medium"
              style={{ color: '#DC2626' }}
            >
              Dismiss
            </button>
          </div>
        </div>
      )}

      {/* Content */}
      <div>
        {/* Search and Filters */}
        <div className="flex gap-4 mb-6">
          <div
            className="flex-1 h-11 px-4 flex items-center border"
            style={{ borderColor: 'var(--gray-border)' }}
          >
            <input
              type="text"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              placeholder="Search domains..."
              className="w-full outline-none text-sm"
              style={{ color: 'var(--black-soft)' }}
            />
          </div>

          <div className="flex gap-2">
            <button
              onClick={() => setFilterActive(null)}
              className="h-11 px-5 text-sm font-medium border"
              style={{
                borderColor: filterActive === null ? 'var(--red-primary)' : 'var(--gray-border)',
                color: filterActive === null ? 'var(--red-primary)' : 'var(--gray-secondary)',
                backgroundColor: filterActive === null ? '#FEF2F2' : 'white',
              }}
            >
              All
            </button>
            <button
              onClick={() => setFilterActive(true)}
              className="h-11 px-5 text-sm font-medium border"
              style={{
                borderColor: filterActive === true ? 'var(--red-primary)' : 'var(--gray-border)',
                color: filterActive === true ? 'var(--red-primary)' : 'var(--gray-secondary)',
                backgroundColor: filterActive === true ? '#FEF2F2' : 'white',
              }}
            >
              Active
            </button>
            <button
              onClick={() => setFilterActive(false)}
              className="h-11 px-5 text-sm font-medium border"
              style={{
                borderColor: filterActive === false ? 'var(--red-primary)' : 'var(--gray-border)',
                color: filterActive === false ? 'var(--red-primary)' : 'var(--gray-secondary)',
                backgroundColor: filterActive === false ? '#FEF2F2' : 'white',
              }}
            >
              Inactive
            </button>
          </div>
        </div>

        {/* Domains Table */}
        {isLoading ? (
          <div className="text-center py-12">
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading domains...
            </p>
          </div>
        ) : filteredDomains.length === 0 ? (
          <div className="text-center py-12">
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {searchTerm || filterActive !== null ? 'No domains match your filters' : 'No domains configured yet'}
            </p>
          </div>
        ) : (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
                <tr>
                  <th
                    className="text-left text-xs font-semibold py-3 px-4 border-b"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    DOMAIN
                  </th>
                  <th
                    className="text-left text-xs font-semibold py-3 px-4 border-b"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    TYPE
                  </th>
                  <th
                    className="text-left text-xs font-semibold py-3 px-4 border-b"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    STATUS
                  </th>
                  <th
                    className="text-left text-xs font-semibold py-3 px-4 border-b"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    CREATED
                  </th>
                  <th
                    className="text-right text-xs font-semibold py-3 px-4 border-b"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    ACTIONS
                  </th>
                </tr>
              </thead>
              <tbody>
                {filteredDomains.map((domain) => (
                  <tr
                    key={domain.id}
                    className="border-b hover:bg-gray-50 transition-colors"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="py-3 px-4">
                      <Link
                        to="/domains/$id"
                        params={{ id: domain.id.toString() }}
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--black-soft)' }}
                      >
                        {domain.name}
                      </Link>
                    </td>
                    <td className="py-3 px-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {domain.server_type}
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span
                        className="inline-flex items-center h-6 px-2 text-xs font-medium"
                        style={{
                          backgroundColor: domain.active ? '#ECFDF5' : '#F3F4F6',
                          color: domain.active ? '#10B981' : 'var(--gray-secondary)',
                        }}
                      >
                        {domain.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {new Date(domain.created_at).toLocaleDateString()}
                      </span>
                    </td>
                    <td className="py-3 px-4 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link
                          to="/domains/$id"
                          params={{ id: domain.id.toString() }}
                          className="text-sm font-medium hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          View
                        </Link>
                        {deleteConfirm === domain.id ? (
                          <>
                            <button
                              onClick={() => handleDelete(domain.id)}
                              className="text-sm font-medium"
                              style={{ color: '#DC2626' }}
                            >
                              Confirm
                            </button>
                            <button
                              onClick={() => setDeleteConfirm(null)}
                              className="text-sm font-medium"
                              style={{ color: 'var(--gray-secondary)' }}
                            >
                              Cancel
                            </button>
                          </>
                        ) : (
                          <button
                            onClick={() => setDeleteConfirm(domain.id)}
                            className="text-sm font-medium"
                            style={{ color: 'var(--gray-secondary)' }}
                          >
                            Delete
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </AppShell>
  )
}
