import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useBanStore } from '../../lib/stores/banStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/settings/bans')({
  component: BansPage,
})

function BansPage() {
  const navigate = useNavigate()
  const { bans, fetchBans, deleteBan, isExpired, isLoading, error, clearError } = useBanStore()
  const { accessToken, isAuthenticated, user } = useAuthStore()
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<number | null>(null)
  const [filterProtocol, setFilterProtocol] = useState<string>('')
  const [filterActive, setFilterActive] = useState(true)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchBans({ active: filterActive, protocol: filterProtocol || undefined }, accessToken).catch(
        console.error
      )
    }
  }, [isAuthenticated, accessToken, filterProtocol, filterActive])

  const handleDelete = async (id: number) => {
    if (!accessToken) return

    try {
      await deleteBan(id, accessToken)
      setDeleteConfirm(null)
      await fetchBans({ active: filterActive, protocol: filterProtocol || undefined }, accessToken)
    } catch (err) {
      console.error('Failed to delete ban:', err)
    }
  }

  return (
    <AppShell title="IP Ban Management" backLink="/settings">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage IP address bans for SMTP, IMAP, and POP3 protocols
          </p>
        </div>
        <button
          onClick={() => setShowCreateModal(true)}
          className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
          style={{
            backgroundColor: 'var(--red-primary)',
            fontFamily: 'Space Grotesk',
          }}
        >
          Add Ban
        </button>
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
            <button onClick={clearError} className="text-sm font-medium" style={{ color: '#DC2626' }}>
              Dismiss
            </button>
          </div>
        </div>
      )}

      {/* Filters */}
      <div className="flex gap-4 mb-6">
        <div>
          <select
            value={filterProtocol}
            onChange={(e) => setFilterProtocol(e.target.value)}
            className="h-11 px-4 border rounded"
            style={{ borderColor: 'var(--gray-border)' }}
          >
            <option value="">All Protocols</option>
            <option value="all">All</option>
            <option value="smtp">SMTP</option>
            <option value="imap">IMAP</option>
            <option value="pop3">POP3</option>
          </select>
        </div>
        <div>
          <select
            value={filterActive ? 'active' : 'all'}
            onChange={(e) => setFilterActive(e.target.value === 'active')}
            className="h-11 px-4 border rounded"
            style={{ borderColor: 'var(--gray-border)' }}
          >
            <option value="active">Active Only</option>
            <option value="all">Include Expired</option>
          </select>
        </div>
      </div>

      {/* Loading State */}
      {isLoading && (
        <div className="text-center py-8" style={{ color: 'var(--gray-secondary)' }}>
          Loading bans...
        </div>
      )}

      {/* Bans Table */}
      {!isLoading && bans.length > 0 && (
        <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  IP Address
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Reason
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Protocol
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Expires
                </th>
                <th className="text-right px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {bans.map((ban) => {
                const expired = isExpired(ban)

                return (
                  <tr
                    key={ban.id}
                    className="border-t"
                    style={{
                      borderColor: 'var(--gray-border)',
                      opacity: expired ? 0.6 : 1,
                    }}
                  >
                    <td className="px-6 py-4">
                      <span
                        className="text-sm font-mono"
                        style={{ color: expired ? 'var(--gray-muted)' : 'var(--black-soft)' }}
                      >
                        {ban.ip}
                      </span>
                    </td>
                    <td className="px-6 py-4">
                      <span
                        className="text-sm"
                        style={{ color: expired ? 'var(--gray-muted)' : 'var(--gray-secondary)' }}
                      >
                        {ban.reason}
                      </span>
                    </td>
                    <td className="px-6 py-4">
                      <span
                        className="inline-block px-2 py-1 text-xs font-medium rounded uppercase"
                        style={{
                          backgroundColor: expired ? 'var(--bg-surface)' : '#DBEAFE',
                          color: expired ? 'var(--gray-muted)' : '#1E40AF',
                        }}
                      >
                        {ban.protocol}
                      </span>
                    </td>
                    <td className="px-6 py-4">
                      {ban.expires_at ? (
                        <div>
                          <span
                            className="text-sm"
                            style={{ color: expired ? 'var(--gray-muted)' : 'var(--gray-secondary)' }}
                          >
                            {new Date(ban.expires_at).toLocaleDateString()}
                          </span>
                          {expired && (
                            <div className="text-xs mt-1" style={{ color: '#DC2626' }}>
                              Expired
                            </div>
                          )}
                        </div>
                      ) : (
                        <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                          Permanent
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-4 text-right">
                      {deleteConfirm === ban.id ? (
                        <div className="inline-flex gap-2">
                          <button
                            onClick={() => handleDelete(ban.id)}
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
                        </div>
                      ) : (
                        <button
                          onClick={() => setDeleteConfirm(ban.id)}
                          className="text-sm font-medium"
                          style={{ color: '#DC2626' }}
                        >
                          Remove
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Empty State */}
      {!isLoading && bans.length === 0 && (
        <div className="border p-12 text-center" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
            No banned IPs found
          </p>
          <button
            onClick={() => setShowCreateModal(true)}
            className="h-10 px-6 inline-flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Add First Ban
          </button>
        </div>
      )}

      {/* Create Modal */}
      {showCreateModal && (
        <BanCreateModal
          onClose={() => setShowCreateModal(false)}
          onSuccess={async () => {
            setShowCreateModal(false)
            if (accessToken) {
              await fetchBans(
                { active: filterActive, protocol: filterProtocol || undefined },
                accessToken
              )
            }
          }}
          currentUser={user?.email || 'admin'}
        />
      )}
    </AppShell>
  )
}

interface BanCreateModalProps {
  onClose: () => void
  onSuccess: () => void
  currentUser: string
}

function BanCreateModal({ onClose, onSuccess, currentUser }: BanCreateModalProps) {
  const { createBan, isLoading, error } = useBanStore()
  const { accessToken } = useAuthStore()
  const [ip, setIp] = useState('')
  const [reason, setReason] = useState('')
  const [protocol, setProtocol] = useState<string>('all')
  const [isPermanent, setIsPermanent] = useState(false)
  const [duration, setDuration] = useState('24')
  const [durationUnit, setDurationUnit] = useState<'hours' | 'days'>('hours')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken) return

    const durationValue = isPermanent
      ? undefined
      : durationUnit === 'hours'
        ? `${duration}h`
        : `${parseInt(duration) * 24}h`

    try {
      await createBan(
        {
          ip,
          reason,
          protocol,
          duration: durationValue,
          created_by: currentUser,
        },
        accessToken
      )
      onSuccess()
    } catch (err) {
      console.error('Failed to create ban:', err)
    }
  }

  return (
    <div
      className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-lg p-6 w-full max-w-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-6">
          <h2
            className="text-xl font-semibold"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            Add IP Ban
          </h2>
          <button onClick={onClose} className="text-xl" style={{ color: 'var(--gray-secondary)' }}>
            ×
          </button>
        </div>

        {error && (
          <div className="mb-4">
            <div
              className="p-4 border rounded"
              style={{
                borderColor: '#EF4444',
                backgroundColor: '#FEF2F2',
                color: '#DC2626',
              }}
            >
              <span className="text-sm">{error}</span>
            </div>
          </div>
        )}

        <form onSubmit={handleSubmit}>
          <div className="space-y-4">
            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                IP Address
              </label>
              <input
                type="text"
                value={ip}
                onChange={(e) => setIp(e.target.value)}
                required
                placeholder="192.168.1.100"
                pattern="^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$"
                className="w-full h-11 px-4 border rounded font-mono"
                style={{ borderColor: 'var(--gray-border)' }}
              />
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Reason
              </label>
              <textarea
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                required
                rows={3}
                placeholder="e.g., Repeated spam attempts, Brute force attack"
                className="w-full px-4 py-3 border rounded"
                style={{ borderColor: 'var(--gray-border)' }}
              />
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Protocol
              </label>
              <select
                value={protocol}
                onChange={(e) => setProtocol(e.target.value)}
                required
                className="w-full h-11 px-4 border rounded"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                <option value="all">All Protocols</option>
                <option value="smtp">SMTP Only</option>
                <option value="imap">IMAP Only</option>
                <option value="pop3">POP3 Only</option>
              </select>
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Duration
              </label>
              <div className="flex items-center gap-2 mb-2">
                <input
                  type="checkbox"
                  id="permanent"
                  checked={isPermanent}
                  onChange={(e) => setIsPermanent(e.target.checked)}
                  className="h-4 w-4"
                />
                <label htmlFor="permanent" className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  Permanent ban
                </label>
              </div>
              {!isPermanent && (
                <div className="flex gap-2">
                  <input
                    type="number"
                    value={duration}
                    onChange={(e) => setDuration(e.target.value)}
                    min="1"
                    required
                    className="w-32 h-11 px-4 border rounded"
                    style={{ borderColor: 'var(--gray-border)' }}
                  />
                  <select
                    value={durationUnit}
                    onChange={(e) => setDurationUnit(e.target.value as 'hours' | 'days')}
                    className="h-11 px-4 border rounded"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <option value="hours">Hours</option>
                    <option value="days">Days</option>
                  </select>
                </div>
              )}
            </div>
          </div>

          <div className="flex gap-3 mt-6">
            <button
              type="submit"
              disabled={isLoading}
              className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
              style={{
                backgroundColor: isLoading ? 'var(--gray-muted)' : 'var(--red-primary)',
                fontFamily: 'Space Grotesk',
              }}
            >
              {isLoading ? 'Adding...' : 'Add Ban'}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="h-10 px-6 flex items-center justify-center text-sm font-medium border rounded"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--gray-secondary)',
              }}
            >
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
