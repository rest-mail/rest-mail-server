import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'
import { usePipelineStore } from '../../../lib/stores/pipelineStore'
import { useAuthStore } from '../../../lib/stores/authStore'
import { AppShell } from '../../../components/layout/AppShell'
import { TraceTimeline, actionColor } from '../../../components/pipelines/TraceTimeline'
import { ArrowLeft, AlertCircle } from 'lucide-react'

export const Route = createFileRoute('/messages/$id/trace')({
  component: MessageTracePage,
})

function MessageTracePage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { currentTrace, isLoading, error, fetchMessageTrace, clearCurrentTrace, clearError } = usePipelineStore()
  const { accessToken, isAuthenticated } = useAuthStore()

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }
    clearCurrentTrace()
    clearError()
    const messageId = parseInt(id, 10)
    if (accessToken && !Number.isNaN(messageId)) {
      fetchMessageTrace(messageId, accessToken).catch((err) => {
        console.error('Failed to fetch message trace:', err)
      })
    }
  }, [id, accessToken, isAuthenticated])

  return (
    <AppShell title="Message Trace">
      <button
        onClick={() => navigate({ to: '/pipelines/logs' })}
        className="inline-flex items-center gap-2 mb-6 text-sm font-medium"
        style={{ color: 'var(--gray-secondary)' }}
      >
        <ArrowLeft className="w-4 h-4" />
        Back to traces
      </button>

      {isLoading && !currentTrace ? (
        <div className="flex items-center justify-center py-20">
          <div className="w-8 h-8 border-4 border-gray-200 border-t-[var(--red-primary)] rounded-full animate-spin" />
        </div>
      ) : error ? (
        <div className="flex items-center justify-center py-20">
          <div className="text-center">
            <AlertCircle className="w-12 h-12 mx-auto mb-4" style={{ color: 'var(--gray-secondary)' }} />
            <p className="font-semibold mb-1" style={{ color: 'var(--black-soft)' }}>
              No trace available
            </p>
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {error}
            </p>
          </div>
        </div>
      ) : currentTrace ? (
        <div>
          <div className="flex items-center justify-between mb-6 gap-4 flex-wrap">
            <div>
              <h1
                className="text-2xl font-bold"
                style={{ fontFamily: 'Space Grotesk, sans-serif', color: 'var(--black-soft)' }}
              >
                Message #{id}
              </h1>
              <p className="text-sm mt-1" style={{ color: 'var(--gray-secondary)' }}>
                {new Date(currentTrace.created_at).toLocaleString()} · {currentTrace.duration_ms}ms total
              </p>
            </div>
            <span
              className="inline-flex items-center h-7 px-3 text-sm font-medium rounded"
              style={actionColor(currentTrace.outcome)}
            >
              {currentTrace.outcome || 'unknown'}
            </span>
          </div>

          {/* Metadata */}
          <div
            className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 mb-8 p-6 rounded-lg"
            style={{ border: '1px solid var(--gray-border)' }}
          >
            <Meta label="Direction" value={currentTrace.direction} />
            <Meta label="Transport" value={currentTrace.transport} />
            <Meta label="Final action" value={currentTrace.final_action} />
            <Meta label="Reason code" value={currentTrace.reason_code} />
            <Meta label="Mail from" value={currentTrace.mail_from} />
            <Meta label="Rcpt to" value={currentTrace.rcpt_to} />
            <Meta label="Client IP" value={currentTrace.client_ip} />
            <Meta label="RFC Message-ID" value={currentTrace.rfc_message_id} />
            <Meta
              label="Spam score"
              value={currentTrace.spam_score != null ? String(currentTrace.spam_score) : ''}
            />
          </div>

          {/* Stage timeline */}
          <div className="p-6 rounded-lg" style={{ border: '1px solid var(--gray-border)' }}>
            <h2
              className="text-xl font-semibold mb-6"
              style={{ fontFamily: 'Space Grotesk, sans-serif', color: 'var(--black-soft)' }}
            >
              Stage timeline
            </h2>
            <TraceTimeline stages={currentTrace.stages} />
          </div>
        </div>
      ) : null}
    </AppShell>
  )
}

function Meta({ label, value }: { label: string; value: string }) {
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
