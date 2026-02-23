import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { usePipelineStore } from '../../lib/stores/pipelineStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/pipelines/')({
  component: PipelinesPage,
})

function PipelinesPage() {
  const navigate = useNavigate()
  const { pipelines, fetchPipelines, deletePipeline, isLoading, error, clearError } = usePipelineStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [selectedDomain, setSelectedDomain] = useState<number | null>(null)
  const [filterDirection, setFilterDirection] = useState<'all' | 'inbound' | 'outbound'>('all')
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
      fetchPipelines(accessToken, selectedDomain || undefined).catch((err) => {
        console.error('Failed to fetch pipelines:', err)
      })
    }
  }, [isAuthenticated, accessToken, selectedDomain, navigate, fetchPipelines, fetchDomains])

  const handleDelete = async (id: number) => {
    if (!accessToken) return

    try {
      await deletePipeline(id, accessToken)
      setDeleteConfirm(null)
    } catch (err) {
      console.error('Failed to delete pipeline:', err)
    }
  }

  const filteredPipelines = pipelines.filter((pipeline) => {
    const matchesDirection = filterDirection === 'all' || pipeline.direction === filterDirection
    return matchesDirection
  })

  return (
    <AppShell title="Pipelines">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage email processing pipelines and filters
          </p>
        </div>
        <div className="flex gap-3">
          <Link
            to="/pipelines/logs"
            className="h-10 px-6 flex items-center justify-center text-sm font-medium rounded border"
            style={{
              borderColor: 'var(--gray-border)',
              color: 'var(--black-soft)',
              fontFamily: 'Space Grotesk',
            }}
          >
            View Logs
          </Link>
          <Link
            to="/pipelines/new"
            className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Create Pipeline
          </Link>
        </div>
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
        </div>
      </div>

      {/* Pipelines Table */}
      {isLoading ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading pipelines...
          </p>
        </div>
      ) : filteredPipelines.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            {filterDirection !== 'all' || selectedDomain
              ? 'No pipelines match your filters'
              : 'No pipelines configured yet'}
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
                  FILTERS
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
              {filteredPipelines.map((pipeline) => {
                const enabledFilters = pipeline.filters.filter((f) => f.enabled).length
                const totalFilters = pipeline.filters.length

                return (
                  <tr
                    key={pipeline.id}
                    className="border-b hover:bg-gray-50 transition-colors"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="py-3 px-4">
                      <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                        {pipeline.domain?.name || `Domain ${pipeline.domain_id}`}
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span
                        className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                        style={{
                          backgroundColor: pipeline.direction === 'inbound' ? '#DBEAFE' : '#FEF3C7',
                          color: pipeline.direction === 'inbound' ? '#1E40AF' : '#92400E',
                        }}
                      >
                        {pipeline.direction}
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {enabledFilters}/{totalFilters} enabled
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span
                        className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                        style={{
                          backgroundColor: pipeline.active ? '#ECFDF5' : '#F3F4F6',
                          color: pipeline.active ? '#10B981' : 'var(--gray-secondary)',
                        }}
                      >
                        {pipeline.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="py-3 px-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {new Date(pipeline.updated_at).toLocaleDateString()}
                      </span>
                    </td>
                    <td className="py-3 px-4 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link
                          to="/pipelines/$id"
                          params={{ id: pipeline.id.toString() }}
                          className="text-sm font-medium hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          Edit
                        </Link>
                        {deleteConfirm === pipeline.id ? (
                          <>
                            <button
                              onClick={() => handleDelete(pipeline.id)}
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
                            onClick={() => setDeleteConfirm(pipeline.id)}
                            className="text-sm font-medium"
                            style={{ color: 'var(--gray-secondary)' }}
                          >
                            Delete
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </AppShell>
  )
}
