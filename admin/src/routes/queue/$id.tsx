import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useQueueStore, type QueueStatus } from '../../lib/stores/queueStore'
import { useUIStore } from '../../lib/stores/uiStore'
import { RawMessageViewer } from '../../components/queue/RawMessageViewer'

export const Route = createFileRoute('/queue/$id')({
  component: QueueDetailPage,
})

function QueueDetailPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { currentEntry, isLoading, error, fetchEntry, retryEntry, deleteEntry } = useQueueStore()
  const { addNotification } = useUIStore()
  const [deleteConfirm, setDeleteConfirm] = useState(false)

  useEffect(() => {
    if (accessToken) {
      fetchEntry(id, accessToken)
    }
  }, [id, accessToken])

  const handleRetry = async () => {
    if (!accessToken) return
    try {
      await retryEntry(id, accessToken)
      addNotification({
        type: 'success',
        message: 'Queue entry retry initiated'
      })
    } catch (err) {
      console.error('Failed to retry entry:', err)
      addNotification({
        type: 'error',
        message: err instanceof Error ? err.message : 'Failed to retry queue entry'
      })
    }
  }

  const handleDelete = async () => {
    if (!accessToken) return
    try {
      await deleteEntry(id, accessToken)
      addNotification({
        type: 'success',
        message: 'Queue entry deleted successfully'
      })
      navigate({ to: '/queue' })
    } catch (err) {
      console.error('Failed to delete entry:', err)
      addNotification({
        type: 'error',
        message: err instanceof Error ? err.message : 'Failed to delete queue entry'
      })
    }
  }

  const getStatusColor = (status: QueueStatus) => {
    switch (status) {
      case 'pending':
        return { bg: '#FEF3C7', text: '#92400E', border: '#FDE68A' }
      case 'deferred':
        return { bg: '#FFEDD5', text: '#9A3412', border: '#FED7AA' }
      case 'bounced':
        return { bg: '#FEE2E2', text: '#991B1B', border: '#FECACA' }
    }
  }

  const formatTimestamp = (timestamp: string) => {
    const date = new Date(timestamp)
    return new Intl.DateTimeFormat('en-US', {
      month: 'long',
      day: 'numeric',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(date)
  }

  if (isLoading) {
    return (
      <div className="min-h-screen" style={{ backgroundColor: 'var(--bg-page)' }}>
        <div className="max-w-4xl mx-auto px-8 py-12">
          <div className="text-center">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading queue entry...
            </div>
          </div>
        </div>
      </div>
    )
  }

  if (!currentEntry) {
    return (
      <div className="min-h-screen" style={{ backgroundColor: 'var(--bg-page)' }}>
        <div className="max-w-4xl mx-auto px-8 py-12">
          <div className="text-center">
            <div className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
              Queue entry not found
            </div>
            <Link
              to="/queue"
              className="text-sm"
              style={{ color: 'var(--red-primary)' }}
            >
              Back to Queue
            </Link>
          </div>
        </div>
      </div>
    )
  }

  const statusColors = getStatusColor(currentEntry.status)

  return (
    <div className="min-h-screen" style={{ backgroundColor: 'var(--bg-page)' }}>
      {/* Header */}
      <div className="border-b" style={{ borderColor: 'var(--gray-border)' }}>
        <div className="max-w-4xl mx-auto px-8 py-6">
          <div className="flex items-center gap-4 mb-4">
            <Link
              to="/queue"
              className="text-sm hover:underline"
              style={{ color: 'var(--gray-secondary)' }}
            >
              ← Back to Queue
            </Link>
          </div>
          <div className="flex items-center justify-between">
            <div>
              <h1 className="text-2xl font-semibold" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
                Queue Entry Details
              </h1>
              <p className="text-sm mt-1" style={{ color: 'var(--gray-secondary)' }}>
                {currentEntry.recipient}
              </p>
            </div>
            <div className="flex gap-3">
              <button
                onClick={handleRetry}
                disabled={isLoading}
                className="px-4 py-2 text-sm font-medium border hover:bg-gray-50 transition-colors"
                style={{
                  color: 'var(--black-soft)',
                  borderColor: 'var(--gray-border)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Retry Delivery
              </button>
              {deleteConfirm ? (
                <div className="flex gap-2">
                  <button
                    onClick={handleDelete}
                    disabled={isLoading}
                    className="px-4 py-2 text-sm font-medium border"
                    style={{
                      color: '#DC2626',
                      borderColor: '#DC2626',
                      backgroundColor: '#FEF2F2',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    Confirm Delete
                  </button>
                  <button
                    onClick={() => setDeleteConfirm(false)}
                    className="px-4 py-2 text-sm font-medium border"
                    style={{
                      color: 'var(--gray-secondary)',
                      borderColor: 'var(--gray-border)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    Cancel
                  </button>
                </div>
              ) : (
                <button
                  onClick={() => setDeleteConfirm(true)}
                  disabled={isLoading}
                  className="px-4 py-2 text-sm font-medium border hover:bg-red-50 transition-colors"
                  style={{
                    color: '#DC2626',
                    borderColor: 'var(--gray-border)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  Delete Entry
                </button>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="max-w-4xl mx-auto px-8 py-8">
        {error && (
          <div
            className="mb-6 p-4 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
        )}

        <div className="space-y-6">
          {/* Status and Basic Info */}
          <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h2 className="text-lg font-semibold mb-4" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
              Status & Information
            </h2>
            <div className="grid grid-cols-2 gap-6">
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  STATUS
                </div>
                <span
                  className="inline-flex px-2 py-1 text-xs font-medium border"
                  style={{
                    backgroundColor: statusColors.bg,
                    color: statusColors.text,
                    borderColor: statusColors.border,
                  }}
                >
                  {currentEntry.status.toUpperCase()}
                </span>
              </div>
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  DELIVERY ATTEMPTS
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {currentEntry.attempts}
                </div>
              </div>
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  CREATED AT
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {formatTimestamp(currentEntry.created_at)}
                </div>
              </div>
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  LAST UPDATED
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {formatTimestamp(currentEntry.updated_at)}
                </div>
              </div>
              {currentEntry.next_attempt_at && (
                <div className="col-span-2">
                  <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    NEXT ATTEMPT SCHEDULED
                  </div>
                  <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    {formatTimestamp(currentEntry.next_attempt_at)}
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* Message Details */}
          <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h2 className="text-lg font-semibold mb-4" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
              Message Details
            </h2>
            <div className="space-y-4">
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  RECIPIENT
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {currentEntry.recipient}
                </div>
              </div>
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  SENDER
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {currentEntry.sender}
                </div>
              </div>
              <div>
                <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                  SUBJECT
                </div>
                <div className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  {currentEntry.subject || '(no subject)'}
                </div>
              </div>
            </div>
          </div>

          {/* Error Message */}
          {currentEntry.error_message && (
            <div className="border p-6" style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2' }}>
              <h2 className="text-lg font-semibold mb-4" style={{ fontFamily: 'Space Grotesk', color: '#DC2626' }}>
                Error Details
              </h2>
              <div className="text-sm font-mono p-4 border" style={{
                color: '#DC2626',
                borderColor: '#FECACA',
                backgroundColor: '#FFFFFF',
              }}>
                {currentEntry.error_message}
              </div>
            </div>
          )}

          {/* Raw Message Viewer */}
          {currentEntry.raw_message && (
            <RawMessageViewer rawMessage={currentEntry.raw_message} />
          )}
        </div>
      </div>
    </div>
  )
}
