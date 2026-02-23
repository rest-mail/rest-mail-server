import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { usePipelineStore } from '../../lib/stores/pipelineStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'
import { ChevronDown, ChevronRight } from 'lucide-react'

export const Route = createFileRoute('/pipelines/logs')({
  component: PipelineLogsPage,
})

function PipelineLogsPage() {
  const navigate = useNavigate()
  const { logs, pipelines, fetchLogs, fetchPipelines, isLoading, error, clearError } = usePipelineStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [selectedPipeline, setSelectedPipeline] = useState<number | null>(null)
  const [filterDirection, setFilterDirection] = useState<'all' | 'inbound' | 'outbound'>('all')
  const [filterAction, setFilterAction] = useState<'all' | 'continue' | 'reject' | 'quarantine' | 'discard'>('all')
  const [expandedLogs, setExpandedLogs] = useState<Set<number>>(new Set())
  const [autoRefresh, setAutoRefresh] = useState(false)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchPipelines(accessToken).catch((err) => {
        console.error('Failed to fetch pipelines:', err)
      })
      loadLogs()
    }
  }, [isAuthenticated, accessToken, navigate, fetchPipelines])

  useEffect(() => {
    if (autoRefresh && accessToken) {
      const interval = setInterval(() => {
        loadLogs()
      }, 10000) // 10 seconds

      return () => clearInterval(interval)
    }
  }, [autoRefresh, accessToken, selectedPipeline, filterDirection, filterAction])

  const loadLogs = () => {
    if (!accessToken) return

    const params: any = {
      limit: 50,
    }

    if (selectedPipeline) params.pipeline_id = selectedPipeline
    if (filterDirection !== 'all') params.direction = filterDirection
    if (filterAction !== 'all') params.action = filterAction

    fetchLogs(params, accessToken).catch((err) => {
      console.error('Failed to fetch logs:', err)
    })
  }

  const toggleExpanded = (logId: number) => {
    const newExpanded = new Set(expandedLogs)
    if (newExpanded.has(logId)) {
      newExpanded.delete(logId)
    } else {
      newExpanded.add(logId)
    }
    setExpandedLogs(newExpanded)
  }

  const getActionBadgeStyle = (action: string) => {
    switch (action) {
      case 'continue':
        return { backgroundColor: '#ECFDF5', color: '#10B981' }
      case 'reject':
        return { backgroundColor: '#FEE2E2', color: '#DC2626' }
      case 'quarantine':
        return { backgroundColor: '#FEF3C7', color: '#D97706' }
      case 'discard':
        return { backgroundColor: '#F3F4F6', color: '#6B7280' }
      default:
        return { backgroundColor: '#F3F4F6', color: '#6B7280' }
    }
  }

  return (
    <AppShell title="Pipeline Logs">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            View pipeline execution logs and performance metrics
          </p>
        </div>
        <div className="flex gap-3">
          <label className="flex items-center gap-2 h-10 px-4 border rounded cursor-pointer" style={{ borderColor: 'var(--gray-border)' }}>
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
              className="w-4 h-4"
            />
            <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
              Auto-refresh (10s)
            </span>
          </label>
          <button
            onClick={loadLogs}
            className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Refresh
          </button>
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
            value={selectedPipeline || ''}
            onChange={(e) => setSelectedPipeline(e.target.value ? parseInt(e.target.value) : null)}
            className="w-full h-11 px-4 border rounded text-sm"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
          >
            <option value="">All Pipelines</option>
            {pipelines.map((pipeline) => (
              <option key={pipeline.id} value={pipeline.id}>
                {pipeline.domain?.name || `Domain ${pipeline.domain_id}`} - {pipeline.direction}
              </option>
            ))}
          </select>
        </div>

        <div className="flex gap-2">
          <select
            value={filterDirection}
            onChange={(e) => setFilterDirection(e.target.value as any)}
            className="h-11 px-4 border rounded text-sm"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
          >
            <option value="all">All Directions</option>
            <option value="inbound">Inbound</option>
            <option value="outbound">Outbound</option>
          </select>

          <select
            value={filterAction}
            onChange={(e) => setFilterAction(e.target.value as any)}
            className="h-11 px-4 border rounded text-sm"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
          >
            <option value="all">All Actions</option>
            <option value="continue">Continue</option>
            <option value="reject">Reject</option>
            <option value="quarantine">Quarantine</option>
            <option value="discard">Discard</option>
          </select>
        </div>
      </div>

      {/* Summary Cards */}
      <div className="grid grid-cols-4 gap-4 mb-6">
        <div className="border rounded p-4" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
            TOTAL EXECUTIONS
          </p>
          <p className="text-2xl font-bold" style={{ color: 'var(--black-soft)' }}>
            {logs.length}
          </p>
        </div>
        <div className="border rounded p-4" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
            CONTINUE
          </p>
          <p className="text-2xl font-bold" style={{ color: '#10B981' }}>
            {logs.filter((l) => l.action === 'continue').length}
          </p>
        </div>
        <div className="border rounded p-4" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
            REJECTED
          </p>
          <p className="text-2xl font-bold" style={{ color: '#DC2626' }}>
            {logs.filter((l) => l.action === 'reject').length}
          </p>
        </div>
        <div className="border rounded p-4" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
            AVG DURATION
          </p>
          <p className="text-2xl font-bold" style={{ color: 'var(--black-soft)' }}>
            {logs.length > 0
              ? Math.round(logs.reduce((sum, l) => sum + l.duration_ms, 0) / logs.length)
              : 0}
            ms
          </p>
        </div>
      </div>

      {/* Logs Table */}
      {isLoading ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading logs...
          </p>
        </div>
      ) : logs.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            No logs found matching your filters
          </p>
        </div>
      ) : (
        <div className="border rounded" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b w-8"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  {/* Expand */}
                </th>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  TIMESTAMP
                </th>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  PIPELINE
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
                  ACTION
                </th>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  STEPS
                </th>
                <th
                  className="text-left text-xs font-semibold py-3 px-4 border-b"
                  style={{
                    color: 'var(--gray-secondary)',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  DURATION
                </th>
              </tr>
            </thead>
            <tbody>
              {logs.map((log) => {
                const isExpanded = expandedLogs.has(log.id)
                const pipeline = pipelines.find((p) => p.id === log.pipeline_id)

                return (
                  <>
                    <tr
                      key={log.id}
                      className="border-b hover:bg-gray-50 transition-colors cursor-pointer"
                      style={{ borderColor: 'var(--gray-border)' }}
                      onClick={() => toggleExpanded(log.id)}
                    >
                      <td className="py-3 px-4">
                        {isExpanded ? (
                          <ChevronDown className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                        ) : (
                          <ChevronRight className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                        )}
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--black-soft)' }}>
                          {new Date(log.created_at).toLocaleString()}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {pipeline?.domain?.name || `Pipeline ${log.pipeline_id}`}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span
                          className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                          style={{
                            backgroundColor: log.direction === 'inbound' ? '#DBEAFE' : '#FEF3C7',
                            color: log.direction === 'inbound' ? '#1E40AF' : '#92400E',
                          }}
                        >
                          {log.direction}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span
                          className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                          style={getActionBadgeStyle(log.action)}
                        >
                          {log.action}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {log.steps.length}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {log.duration_ms}ms
                        </span>
                      </td>
                    </tr>

                    {isExpanded && (
                      <tr style={{ borderColor: 'var(--gray-border)' }}>
                        <td colSpan={7} className="p-6 bg-gray-50">
                          <h4 className="text-sm font-semibold mb-3" style={{ color: 'var(--black-soft)' }}>
                            Filter Steps
                          </h4>
                          <div className="space-y-2">
                            {log.steps.map((step, index) => (
                              <div
                                key={index}
                                className="bg-white border rounded p-3"
                                style={{ borderColor: 'var(--gray-border)' }}
                              >
                                <div className="flex items-start justify-between">
                                  <div className="flex-1">
                                    <div className="flex items-center gap-2 mb-1">
                                      <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                                        {index + 1}. {step.filter}
                                      </span>
                                      {step.duration_ms && (
                                        <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                                          ({step.duration_ms}ms)
                                        </span>
                                      )}
                                    </div>
                                    <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                                      Result: {step.result}
                                    </p>
                                    {step.detail && (
                                      <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                                        {step.detail}
                                      </p>
                                    )}
                                  </div>
                                </div>
                              </div>
                            ))}
                          </div>
                        </td>
                      </tr>
                    )}
                  </>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </AppShell>
  )
}
