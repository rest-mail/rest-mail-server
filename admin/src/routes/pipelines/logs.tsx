import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { usePipelineStore, type TraceQueryParams } from '../../lib/stores/pipelineStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'
import { TraceTimeline, actionColor } from '../../components/pipelines/TraceTimeline'
import { ChevronDown, ChevronRight, GitBranch } from 'lucide-react'

export const Route = createFileRoute('/pipelines/logs')({
  component: PipelineLogsPage,
})

const OUTCOME_OPTIONS = ['delivered', 'queued', 'rejected', 'quarantined', 'discarded', 'deferred']

function PipelineLogsPage() {
  const navigate = useNavigate()
  const { traces, pipelines, fetchTraces, fetchPipelines, isLoading, error, clearError } = usePipelineStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [selectedPipeline, setSelectedPipeline] = useState<number | null>(null)
  const [filterDirection, setFilterDirection] = useState<'all' | 'inbound' | 'outbound'>('all')
  const [filterOutcome, setFilterOutcome] = useState<string>('all')
  const [filterReasonCode, setFilterReasonCode] = useState('')
  const [filterRfcMessageId, setFilterRfcMessageId] = useState('')
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
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
      loadTraces()
    }
  }, [isAuthenticated, accessToken, navigate, fetchPipelines])

  useEffect(() => {
    if (autoRefresh && accessToken) {
      const interval = setInterval(() => {
        loadTraces()
      }, 10000) // 10 seconds

      return () => clearInterval(interval)
    }
  }, [autoRefresh, accessToken, selectedPipeline, filterDirection, filterOutcome, filterReasonCode, filterRfcMessageId])

  const loadTraces = () => {
    if (!accessToken) return

    const params: TraceQueryParams = { limit: 50 }
    if (selectedPipeline) params.pipeline_id = selectedPipeline
    if (filterDirection !== 'all') params.direction = filterDirection
    if (filterOutcome !== 'all') params.outcome = filterOutcome
    if (filterReasonCode.trim()) params.reason_code = filterReasonCode.trim()
    if (filterRfcMessageId.trim()) params.rfc_message_id = filterRfcMessageId.trim()

    fetchTraces(params, accessToken).catch((err) => {
      console.error('Failed to fetch traces:', err)
    })
  }

  const toggleExpanded = (id: number) => {
    const next = new Set(expanded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setExpanded(next)
  }

  const delivered = traces.filter((t) => t.outcome === 'delivered' || t.outcome === 'queued').length
  const rejected = traces.filter((t) => t.outcome === 'rejected').length
  const avgDuration =
    traces.length > 0 ? Math.round(traces.reduce((sum, t) => sum + (t.duration_ms || 0), 0) / traces.length) : 0

  return (
    <AppShell title="Message Traces">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Per-message pipeline traces — outcome, reason, transport and the stage timeline
          </p>
        </div>
        <div className="flex gap-3">
          <label
            className="flex items-center gap-2 h-10 px-4 border rounded cursor-pointer"
            style={{ borderColor: 'var(--gray-border)' }}
          >
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
            onClick={loadTraces}
            className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
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
            style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2', color: '#DC2626' }}
          >
            <span className="text-sm">{error}</span>
            <button onClick={clearError} className="text-sm font-medium" style={{ color: '#DC2626' }}>
              Dismiss
            </button>
          </div>
        </div>
      )}

      {/* Filters */}
      <div className="flex gap-4 mb-4 flex-wrap">
        <div className="flex-1 min-w-[200px]">
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

        <select
          value={filterDirection}
          onChange={(e) => setFilterDirection(e.target.value as 'all' | 'inbound' | 'outbound')}
          className="h-11 px-4 border rounded text-sm"
          style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
        >
          <option value="all">All Directions</option>
          <option value="inbound">Inbound</option>
          <option value="outbound">Outbound</option>
        </select>

        <select
          value={filterOutcome}
          onChange={(e) => setFilterOutcome(e.target.value)}
          className="h-11 px-4 border rounded text-sm"
          style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
        >
          <option value="all">All Outcomes</option>
          {OUTCOME_OPTIONS.map((o) => (
            <option key={o} value={o}>
              {o.charAt(0).toUpperCase() + o.slice(1)}
            </option>
          ))}
        </select>
      </div>

      <div className="flex gap-4 mb-6 flex-wrap">
        <input
          type="text"
          value={filterReasonCode}
          onChange={(e) => setFilterReasonCode(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && loadTraces()}
          placeholder="Reason code (e.g. dmarc_fail)"
          className="flex-1 min-w-[200px] h-11 px-4 border rounded text-sm"
          style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
        />
        <input
          type="text"
          value={filterRfcMessageId}
          onChange={(e) => setFilterRfcMessageId(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && loadTraces()}
          placeholder="RFC Message-ID"
          className="flex-1 min-w-[200px] h-11 px-4 border rounded text-sm"
          style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
        />
        <button
          onClick={loadTraces}
          className="h-11 px-6 border rounded text-sm font-medium"
          style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
        >
          Apply Filters
        </button>
      </div>

      {/* Summary Cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        <SummaryCard label="TRACES" value={traces.length} color="var(--black-soft)" />
        <SummaryCard label="DELIVERED" value={delivered} color="#10B981" />
        <SummaryCard label="REJECTED" value={rejected} color="#DC2626" />
        <SummaryCard label="AVG DURATION" value={`${avgDuration}ms`} color="var(--black-soft)" />
      </div>

      {/* Traces Table */}
      {isLoading && traces.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading traces...
          </p>
        </div>
      ) : traces.length === 0 ? (
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            No traces found matching your filters
          </p>
        </div>
      ) : (
        <div className="border rounded overflow-x-auto" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full min-w-[720px]">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                {['', 'TIMESTAMP', 'PIPELINE', 'DIRECTION', 'TRANSPORT', 'OUTCOME', 'REASON', 'DURATION'].map(
                  (h, i) => (
                    <th
                      key={i}
                      className="text-left text-xs font-semibold py-3 px-4 border-b"
                      style={{
                        color: 'var(--gray-secondary)',
                        borderColor: 'var(--gray-border)',
                        fontFamily: 'Space Grotesk',
                      }}
                    >
                      {h}
                    </th>
                  )
                )}
              </tr>
            </thead>
            <tbody>
              {traces.map((trace) => {
                const isExpanded = expanded.has(trace.id)
                const pipeline = pipelines.find((p) => p.id === trace.pipeline_id)

                return (
                  <>
                    <tr
                      key={trace.id}
                      className="border-b hover:bg-gray-50 transition-colors cursor-pointer"
                      style={{ borderColor: 'var(--gray-border)' }}
                      onClick={() => toggleExpanded(trace.id)}
                    >
                      <td className="py-3 px-4">
                        {isExpanded ? (
                          <ChevronDown className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                        ) : (
                          <ChevronRight className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                        )}
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm whitespace-nowrap" style={{ color: 'var(--black-soft)' }}>
                          {new Date(trace.created_at).toLocaleString()}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {pipeline?.domain?.name || `Pipeline ${trace.pipeline_id}`}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span
                          className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                          style={{
                            backgroundColor: trace.direction === 'inbound' ? '#DBEAFE' : '#FEF3C7',
                            color: trace.direction === 'inbound' ? '#1E40AF' : '#92400E',
                          }}
                        >
                          {trace.direction}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <TransportChip transport={trace.transport} />
                      </td>
                      <td className="py-3 px-4">
                        <span
                          className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
                          style={actionColor(trace.outcome)}
                        >
                          {trace.outcome || '—'}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {trace.reason_code || '—'}
                        </span>
                      </td>
                      <td className="py-3 px-4">
                        <span className="text-sm whitespace-nowrap" style={{ color: 'var(--gray-secondary)' }}>
                          {trace.duration_ms}ms
                        </span>
                      </td>
                    </tr>

                    {isExpanded && (
                      <tr style={{ borderColor: 'var(--gray-border)' }}>
                        <td colSpan={8} className="p-6 bg-gray-50">
                          {/* Trace metadata */}
                          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
                            <MetaField label="Mail from" value={trace.mail_from} />
                            <MetaField label="Rcpt to" value={trace.rcpt_to} />
                            <MetaField label="Client IP" value={trace.client_ip} />
                            <MetaField label="RFC Message-ID" value={trace.rfc_message_id} />
                          </div>

                          {trace.message_id != null && (
                            <button
                              onClick={(e) => {
                                e.stopPropagation()
                                navigate({
                                  to: '/messages/$id/trace',
                                  params: { id: String(trace.message_id) },
                                })
                              }}
                              className="inline-flex items-center gap-2 mb-6 h-9 px-4 border rounded text-sm font-medium"
                              style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                            >
                              <GitBranch className="w-4 h-4" />
                              Open message trace timeline
                            </button>
                          )}

                          <h4 className="text-sm font-semibold mb-4" style={{ color: 'var(--black-soft)' }}>
                            Stage timeline
                          </h4>
                          <TraceTimeline stages={trace.stages} />
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

function TransportChip({ transport }: { transport: string }) {
  if (!transport) {
    return (
      <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
        —
      </span>
    )
  }
  const isTLS = transport === 'tls'
  return (
    <span
      className="inline-flex items-center h-6 px-2 text-xs font-medium rounded"
      style={{
        backgroundColor: isTLS ? '#ECFDF5' : '#FEE2E2',
        color: isTLS ? '#10B981' : '#DC2626',
      }}
    >
      {transport}
    </span>
  )
}

function SummaryCard({ label, value, color }: { label: string; value: number | string; color: string }) {
  return (
    <div className="border rounded p-4" style={{ borderColor: 'var(--gray-border)' }}>
      <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
        {label}
      </p>
      <p className="text-2xl font-bold" style={{ color }}>
        {typeof value === 'number' ? value.toLocaleString() : value}
      </p>
    </div>
  )
}

function MetaField({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-xs font-semibold mb-1" style={{ color: 'var(--gray-secondary)' }}>
        {label}
      </p>
      <p className="text-sm break-words" style={{ color: 'var(--black-soft)' }}>
        {value || '—'}
      </p>
    </div>
  )
}
