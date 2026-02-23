import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useCustomFilterStore } from '../../lib/stores/customFilterStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/custom-filters/')({
  component: CustomFiltersPage,
})

function CustomFiltersPage() {
  const navigate = useNavigate()
  const { filters, fetchFilters, deleteFilter, isLoading, error, clearError } = useCustomFilterStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [selectedDomain, setSelectedDomain] = useState<number | null>(null)
  const [filterDirection, setFilterDirection] = useState<'all' | 'inbound' | 'outbound' | 'both'>('all')
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
      fetchFilters(accessToken, selectedDomain || undefined).catch((err) => {
        console.error('Failed to fetch custom filters:', err)
      })
    }
  }, [isAuthenticated, accessToken, selectedDomain, navigate, fetchFilters, fetchDomains])

  const handleDelete = async (id: number) => {
    if (!accessToken) return

    try {
      await deleteFilter(id, accessToken)
      setDeleteConfirm(null)
    } catch (err) {
      console.error('Failed to delete custom filter:', err)
    }
  }

  const filteredFilters = filters.filter((filter) => {
    const matchesDirection = filterDirection === 'all' || filter.direction === filterDirection || filter.direction === 'both'
    return matchesDirection
  })

  return (
    <AppShell title="Custom Filters">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage custom JavaScript email filters
          </p>
        </div>
        <Link
          to="/custom-filters/new"
          className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
          style={{
            backgroundColor: 'var(--red-primary)',
            fontFamily: 'Space Grotesk',
          }}
        >
          Create Custom Filter
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

      {/* Filters */}
      <div className="flex gap-4 mb-6">
        <div className="flex-1">
          <select
            value={selectedDomain || ''}
            onChange={(e) => setSelectedDomain(e.target.value ? parseInt(e.target.value) : null)}
            className="w-full h-11 px-4 border rounded text-sm"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
          >
            <option value="">All Domains</option>
            {domains.map((domain) => (
              <option key={domain.id} value={domain.id}>
                {domain.name}
              </option>
            ))}
          </select>
        </div>

        <div className="flex gap-2">
          <button
            onClick={() => setFilterDirection('all')}
            className="h-11 px-5 text-sm font-medium border rounded"
            style={{
              borderColor: filterDirection === 'all' ? 'var(--red-primary)' : 'var(--gray-border)',
              color: filterDirection === 'all' ? 'var(--red-primary)' : 'var(--gray-secondary)',
              backgroundColor: filterDirection === 'all' ? '#FEF2F2' : 'white',
            }}
          >
            All
          </button>
          <button
            onClick={() => setFilterDirection('inbound')}
            className="h-11 px-5 text-sm font-medium border rounded"
            style={{
              borderColor: filterDirection === 'inbound' ? 'var(--red-primary)' : 'var(--gray-border)',
              color: filterDirection === 'inbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
              backgroundColor: filterDirection === 'inbound' ? '#FEF2F2' : 'white',
            }}
          >
            Inbound
          </button>
          <button
            onClick={() => setFilterDirection('outbound')}
            className="h-11 px-5 text-sm font-medium border rounded"
            style={{
              borderColor: filterDirection === 'outbound' ? 'var(--red-primary)' : 'var(--gray-border)',
              color: filterDirection === 'outbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
              backgroundColor: filterDirection === 'outbound' ? '#FEF2F2' : 'white',
            }}
          >
            Outbound
          </button>
          <button
            onClick={() => setFilterDirection('both')}
            className="h-11 px-5 text-sm font-medium border rounded"
            style={{
              borderColor: filterDirection === 'both' ? 'var(--red-primary)' : 'var(--gray-border)',
              color: filterDirection === 'both' ? 'var(--red-primary)' : 'var(--gray-secondary)',
              backgroundColor: filterDirection === 'both' ? '#FEF2F2' : 'white',
            }}
          >
            Both
          </button>
        </div>
      </div>

      {/* Custom Filters Table */}
      {isLoading ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading custom filters...
          </p>
        </div>
      ) : filteredFilters.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            {filterDirection !== 'all' || selectedDomain
              ? 'No custom filters match your filters'
              : 'No custom filters created yet'}
          </p>
        </div>
      ) : (
        <div className="border rounded" style={{ borderColor: 'var(--gray-border)' }}>
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
                  NAME
                </th>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  DESCRIPTION
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
                  DIRECTION
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
                  UPDATED
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
              {filteredFilters.map((filter) => (
                <tr
                  key={filter.id}
                  className="border-b hover:bg-gray-50 transition-colors"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <td className="py-3 px-4">
                    <Link
                      to="/custom-filters/$id"
                      params={{ id: filter.id.toString() }}
                      className="text-sm font-medium hover:underline"
                      style={{ color: 'var(--black-soft)' }}
                    >
                      {filter.name}
                    </Link>
                  </td>
                  <td className="py-3 px-4">
                    <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                      {filter.description || 'No description'}
                    </span>
                  </td>
                  <td className="py-3 px-4">
                    <span
                      className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                      style={{
                        backgroundColor: filter.filter_type === 'action' ? '#DBEAFE' : '#FEF3C7',
                        color: filter.filter_type === 'action' ? '#1E40AF' : '#92400E',
                      }}
                    >
                      {filter.filter_type}
                    </span>
                  </td>
                  <td className="py-3 px-4">
                    <span
                      className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                      style={{
                        backgroundColor: filter.direction === 'both' ? '#F3E8FF' : filter.direction === 'inbound' ? '#DBEAFE' : '#FEF3C7',
                        color: filter.direction === 'both' ? '#7C3AED' : filter.direction === 'inbound' ? '#1E40AF' : '#92400E',
                      }}
                    >
                      {filter.direction}
                    </span>
                  </td>
                  <td className="py-3 px-4">
                    <span
                      className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                      style={{
                        backgroundColor: filter.enabled ? '#ECFDF5' : '#F3F4F6',
                        color: filter.enabled ? '#10B981' : 'var(--gray-secondary)',
                      }}
                    >
                      {filter.enabled ? 'Enabled' : 'Disabled'}
                    </span>
                  </td>
                  <td className="py-3 px-4">
                    <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                      {new Date(filter.updated_at).toLocaleDateString()}
                    </span>
                  </td>
                  <td className="py-3 px-4 text-right">
                    <div className="flex items-center justify-end gap-2">
                      <Link
                        to="/custom-filters/$id"
                        params={{ id: filter.id.toString() }}
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--red-primary)' }}
                      >
                        Edit
                      </Link>
                      {deleteConfirm === filter.id ? (
                        <>
                          <button
                            onClick={() => handleDelete(filter.id)}
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
                          onClick={() => setDeleteConfirm(filter.id)}
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
    </AppShell>
  )
}
