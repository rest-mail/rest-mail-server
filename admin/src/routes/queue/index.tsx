import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useQueueStore, type QueueStatus } from '../../lib/stores/queueStore'
import { useUIStore } from '../../lib/stores/uiStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/queue/')({
  component: QueueListPage,
})

type FilterOption = 'all' | QueueStatus

function QueueListPage() {
  const { accessToken } = useAuthStore()
  const {
    entries,
    isLoading,
    error,
    filter,
    selectedIds,
    fetchQueue,
    setFilter,
    deleteEntry,
    retryEntry,
    toggleSelection,
    selectAll,
    clearSelection,
    retryBulk,
    deleteBulk
  } = useQueueStore()
  const { addNotification } = useUIStore()
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)
  const [showBulkConfirm, setShowBulkConfirm] = useState(false)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [isRefreshing, setIsRefreshing] = useState(false)

  // Initial fetch
  useEffect(() => {
    if (accessToken) {
      fetchQueue(accessToken)
      setLastUpdated(new Date())
    }
  }, [accessToken, filter])

  // Auto-refresh every 15 seconds when tab is visible
  useEffect(() => {
    if (!accessToken) return

    const handleVisibilityChange = () => {
      if (!document.hidden && accessToken) {
        fetchQueue(accessToken)
        setLastUpdated(new Date())
      }
    }

    const intervalId = setInterval(() => {
      if (!document.hidden && accessToken) {
        fetchQueue(accessToken)
        setLastUpdated(new Date())
      }
    }, 15000) // 15 seconds

    document.addEventListener('visibilitychange', handleVisibilityChange)

    return () => {
      clearInterval(intervalId)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [accessToken, filter])

  const handleManualRefresh = async () => {
    if (!accessToken) return
    setIsRefreshing(true)
    try {
      await fetchQueue(accessToken)
      setLastUpdated(new Date())
    } finally {
      setIsRefreshing(false)
    }
  }

  const handleFilterChange = (newFilter: FilterOption) => {
    setFilter(newFilter)
    if (accessToken) {
      fetchQueue(accessToken, newFilter)
    }
  }

  const handleDelete = async (id: string) => {
    if (!accessToken) return
    try {
      await deleteEntry(id, accessToken)
      setDeleteConfirm(null)
      addNotification({
        type: 'success',
        message: 'Queue entry deleted successfully'
      })
    } catch (err) {
      console.error('Failed to delete entry:', err)
      addNotification({
        type: 'error',
        message: err instanceof Error ? err.message : 'Failed to delete queue entry'
      })
    }
  }

  const handleRetry = async (id: string) => {
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

  const handleSelectAll = () => {
    if (selectedIds.length === filteredEntries.length && filteredEntries.length > 0) {
      clearSelection()
    } else {
      selectAll(filteredEntries.map(e => e.id))
    }
  }

  const handleBulkRetry = async () => {
    if (!accessToken) return
    const count = selectedIds.length
    try {
      await retryBulk(selectedIds, accessToken)
      addNotification({
        type: 'success',
        message: `Successfully retried ${count} queue ${count === 1 ? 'entry' : 'entries'}`
      })
    } catch (err) {
      console.error('Bulk retry failed:', err)
      addNotification({
        type: 'error',
        message: err instanceof Error ? err.message : 'Bulk retry failed'
      })
    }
  }

  const handleBulkDelete = async () => {
    if (!accessToken) return
    const count = selectedIds.length
    try {
      await deleteBulk(selectedIds, accessToken)
      setShowBulkConfirm(false)
      addNotification({
        type: 'success',
        message: `Successfully deleted ${count} queue ${count === 1 ? 'entry' : 'entries'}`
      })
    } catch (err) {
      console.error('Bulk delete failed:', err)
      addNotification({
        type: 'error',
        message: err instanceof Error ? err.message : 'Bulk delete failed'
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
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date)
  }

  const filteredEntries = filter === 'all' ? entries : entries.filter(e => e.status === filter)

  const formatLastUpdated = (date: Date | null) => {
    if (!date) return 'Never'
    return new Intl.DateTimeFormat('en-US', {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(date)
  }

  return (
    <AppShell title="Email Queue">
      <div>
        <div className="flex items-center justify-between mb-6">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Monitor and manage pending email deliveries
          </p>
          <div className="flex items-center gap-3">
            <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
              Last updated: {formatLastUpdated(lastUpdated)}
            </span>
            <button
              onClick={handleManualRefresh}
              disabled={isRefreshing}
              className="px-3 py-1 text-xs font-medium border hover:bg-gray-50 transition-colors disabled:opacity-50"
              style={{
                borderColor: 'var(--gray-border)',
                fontFamily: 'Space Grotesk',
                color: 'var(--black-soft)'
              }}
            >
              {isRefreshing ? 'Refreshing...' : 'Refresh'}
            </button>
          </div>
        </div>

        {/* Filter Tabs */}
        <div className="border-b mb-6" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="flex gap-6">
            {(['all', 'pending', 'deferred', 'bounced'] as FilterOption[]).map((f) => (
              <button
                key={f}
                onClick={() => handleFilterChange(f)}
                className="py-3 text-sm font-medium border-b-2 transition-colors"
                style={{
                  color: filter === f ? 'var(--red-primary)' : 'var(--gray-secondary)',
                  borderColor: filter === f ? 'var(--red-primary)' : 'transparent',
                  fontFamily: 'Space Grotesk',
                }}
              >
                {f.charAt(0).toUpperCase() + f.slice(1)}
                {f !== 'all' && (
                  <span
                    className="ml-2 px-2 py-0.5 rounded text-xs"
                    style={{
                      backgroundColor: filter === f ? 'var(--bg-surface)' : 'transparent',
                      color: 'var(--gray-secondary)',
                    }}
                  >
                    {entries.filter(e => e.status === f).length}
                  </span>
                )}
              </button>
            ))}
          </div>
        </div>
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

        {isLoading ? (
          <div className="text-center py-12">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading queue entries...
            </div>
          </div>
        ) : filteredEntries.length === 0 ? (
          <div className="text-center py-12">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              No queue entries found
            </div>
          </div>
        ) : (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
                <tr>
                  <th className="px-6 py-3 text-xs">
                    <input
                      type="checkbox"
                      checked={selectedIds.length === filteredEntries.length && filteredEntries.length > 0}
                      onChange={handleSelectAll}
                      className="cursor-pointer"
                    />
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    RECIPIENT
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    SENDER
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    SUBJECT
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    STATUS
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    ATTEMPTS
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    NEXT ATTEMPT
                  </th>
                  <th className="text-left px-6 py-3 text-xs font-medium" style={{ color: 'var(--gray-secondary)' }}>
                    ACTIONS
                  </th>
                </tr>
              </thead>
              <tbody>
                {filteredEntries.map((entry) => {
                  const statusColors = getStatusColor(entry.status)
                  return (
                    <tr
                      key={entry.id}
                      className="border-t"
                      style={{ borderColor: 'var(--gray-border)' }}
                    >
                      <td className="px-6 py-4">
                        <input
                          type="checkbox"
                          checked={selectedIds.includes(entry.id)}
                          onChange={() => toggleSelection(entry.id)}
                          className="cursor-pointer"
                        />
                      </td>
                      <td className="px-6 py-4">
                        <Link
                          to="/queue/$id"
                          params={{ id: entry.id }}
                          className="text-sm font-medium hover:underline"
                          style={{ color: 'var(--black-soft)' }}
                        >
                          {entry.recipient}
                        </Link>
                      </td>
                      <td className="px-6 py-4">
                        <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {entry.sender}
                        </div>
                      </td>
                      <td className="px-6 py-4">
                        <div className="text-sm truncate max-w-xs" style={{ color: 'var(--black-soft)' }}>
                          {entry.subject || '(no subject)'}
                        </div>
                      </td>
                      <td className="px-6 py-4">
                        <span
                          className="inline-flex px-2 py-1 text-xs font-medium border"
                          style={{
                            backgroundColor: statusColors.bg,
                            color: statusColors.text,
                            borderColor: statusColors.border,
                          }}
                        >
                          {entry.status.toUpperCase()}
                        </span>
                      </td>
                      <td className="px-6 py-4">
                        <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {entry.attempts}
                        </div>
                      </td>
                      <td className="px-6 py-4">
                        <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                          {entry.next_attempt_at ? formatTimestamp(entry.next_attempt_at) : '-'}
                        </div>
                      </td>
                      <td className="px-6 py-4">
                        <div className="flex gap-2">
                          <button
                            onClick={() => handleRetry(entry.id)}
                            disabled={isLoading}
                            className="px-3 py-1 text-xs font-medium border hover:bg-gray-50 transition-colors"
                            style={{
                              color: 'var(--black-soft)',
                              borderColor: 'var(--gray-border)',
                              fontFamily: 'Space Grotesk',
                            }}
                          >
                            Retry
                          </button>
                          {deleteConfirm === entry.id ? (
                            <div className="flex gap-2">
                              <button
                                onClick={() => handleDelete(entry.id)}
                                disabled={isLoading}
                                className="px-3 py-1 text-xs font-medium border"
                                style={{
                                  color: '#DC2626',
                                  borderColor: '#DC2626',
                                  backgroundColor: '#FEF2F2',
                                  fontFamily: 'Space Grotesk',
                                }}
                              >
                                Confirm
                              </button>
                              <button
                                onClick={() => setDeleteConfirm(null)}
                                className="px-3 py-1 text-xs font-medium border"
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
                              onClick={() => setDeleteConfirm(entry.id)}
                              disabled={isLoading}
                              className="px-3 py-1 text-xs font-medium border hover:bg-red-50 transition-colors"
                              style={{
                                color: '#DC2626',
                                borderColor: 'var(--gray-border)',
                                fontFamily: 'Space Grotesk',
                              }}
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

        {/* Bulk Action Bar */}
        {selectedIds.length > 0 && (
          <div
            className="fixed bottom-8 left-1/2 transform -translate-x-1/2 px-6 py-4 border shadow-lg flex items-center gap-4 z-50"
            style={{
              backgroundColor: 'var(--bg-surface)',
              borderColor: 'var(--gray-border)',
            }}
          >
            <span className="text-sm font-medium" style={{ color: 'var(--black-soft)', fontFamily: 'Space Grotesk' }}>
              {selectedIds.length} selected
            </span>
            <button
              onClick={handleBulkRetry}
              disabled={isLoading}
              className="px-4 py-2 text-sm font-medium border hover:bg-gray-50 transition-colors disabled:opacity-50"
              style={{
                borderColor: 'var(--gray-border)',
                fontFamily: 'Space Grotesk',
                color: 'var(--black-soft)'
              }}
            >
              Retry All
            </button>
            {showBulkConfirm ? (
              <div className="flex gap-2">
                <button
                  onClick={handleBulkDelete}
                  disabled={isLoading}
                  className="px-4 py-2 text-sm font-medium border disabled:opacity-50"
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
                  onClick={() => setShowBulkConfirm(false)}
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
                onClick={() => setShowBulkConfirm(true)}
                disabled={isLoading}
                className="px-4 py-2 text-sm font-medium border hover:bg-red-50 transition-colors disabled:opacity-50"
                style={{
                  color: '#DC2626',
                  borderColor: 'var(--gray-border)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Delete All
              </button>
            )}
            <button
              onClick={clearSelection}
              className="px-4 py-2 text-sm font-medium"
              style={{ color: 'var(--gray-secondary)', fontFamily: 'Space Grotesk' }}
            >
              Cancel
            </button>
          </div>
        )}
      </div>
    </AppShell>
  )
}
