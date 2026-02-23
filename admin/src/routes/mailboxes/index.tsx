import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState, useMemo } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/mailboxes/')({
  component: MailboxesPage,
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
}

function QuotaBar({ used, total }: { used: number; total: number }) {
  const percentage = total > 0 ? (used / total) * 100 : 0
  const isNearLimit = percentage > 80
  const isFull = percentage > 95

  return (
    <div className="flex items-center gap-3">
      <div className="flex-1 h-2 bg-gray-100 rounded-full overflow-hidden">
        <div
          className="h-full transition-all duration-300"
          style={{
            width: `${Math.min(percentage, 100)}%`,
            backgroundColor: isFull
              ? '#EF4444'
              : isNearLimit
              ? '#F59E0B'
              : 'var(--success)',
          }}
        />
      </div>
      <div className="text-xs whitespace-nowrap" style={{ color: 'var(--gray-secondary)' }}>
        {formatBytes(used)} / {formatBytes(total)}
      </div>
    </div>
  )
}

function MailboxesPage() {
  const { accessToken } = useAuthStore()
  const { mailboxes, isLoading, error, fetchMailboxes, selectedDomain, setSelectedDomain, deleteMailbox } =
    useMailboxStore()
  const [searchQuery, setSearchQuery] = useState('')
  const [deletingId, setDeletingId] = useState<number | null>(null)

  useEffect(() => {
    if (accessToken) {
      fetchMailboxes(accessToken)
    }
  }, [accessToken, fetchMailboxes])

  // Extract unique domains
  const domains = useMemo(() => {
    const uniqueDomains = Array.from(new Set(mailboxes.map((m) => m.domain.name)))
    return uniqueDomains.sort()
  }, [mailboxes])

  // Filter mailboxes
  const filteredMailboxes = useMemo(() => {
    return mailboxes.filter((mailbox) => {
      const matchesSearch =
        searchQuery === '' ||
        mailbox.address.toLowerCase().includes(searchQuery.toLowerCase()) ||
        mailbox.display_name?.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesDomain = !selectedDomain || mailbox.domain.name === selectedDomain

      return matchesSearch && matchesDomain
    })
  }, [mailboxes, searchQuery, selectedDomain])

  const handleDelete = async (id: number, email: string) => {
    if (!accessToken) return
    if (!confirm(`Are you sure you want to delete mailbox "${email}"?`)) return

    setDeletingId(id)
    try {
      await deleteMailbox(accessToken, id)
    } catch (err) {
      console.error('Failed to delete mailbox:', err)
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <AppShell title="Mailboxes">
      <div>
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Manage email accounts and quotas
            </p>
          </div>

          <Link
            to="/mailboxes/new"
            className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Create Mailbox
          </Link>
        </div>

        {/* Error Message */}
        {error && (
          <div
            className="p-4 mb-6 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
        )}

        {/* Filters */}
        <div className="flex gap-4 mb-6">
          {/* Search */}
          <div className="flex-1">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search mailboxes..."
                className="w-full outline-none text-sm"
                style={{ color: 'var(--black-soft)' }}
              />
            </div>
          </div>

          {/* Domain Filter */}
          <div className="w-64">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <select
                value={selectedDomain || ''}
                onChange={(e) => setSelectedDomain(e.target.value || null)}
                className="w-full outline-none text-sm bg-transparent"
                style={{ color: 'var(--black-soft)' }}
              >
                <option value="">All Domains</option>
                {domains.map((domain) => (
                  <option key={domain} value={domain}>
                    {domain}
                  </option>
                ))}
              </select>
            </div>
          </div>
        </div>

        {/* Table */}
        {isLoading ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading mailboxes...
            </div>
          </div>
        ) : filteredMailboxes.length === 0 ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {searchQuery || selectedDomain ? 'No mailboxes found' : 'No mailboxes yet'}
            </div>
          </div>
        ) : (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead>
                <tr
                  className="border-b text-left text-xs font-medium"
                  style={{
                    backgroundColor: 'var(--bg-surface)',
                    borderColor: 'var(--gray-border)',
                    color: 'var(--gray-secondary)',
                  }}
                >
                  <th className="px-6 py-4">Email</th>
                  <th className="px-6 py-4">Display Name</th>
                  <th className="px-6 py-4">Quota Usage</th>
                  <th className="px-6 py-4">Status</th>
                  <th className="px-6 py-4 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {filteredMailboxes.map((mailbox) => (
                  <tr
                    key={mailbox.id}
                    className="border-b transition-colors hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="px-6 py-4">
                      <Link
                        to="/mailboxes/$id"
                        params={{ id: String(mailbox.id) }}
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--black-soft)' }}
                      >
                        {mailbox.address}
                      </Link>
                    </td>
                    <td className="px-6 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {mailbox.display_name || '—'}
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <div className="w-64">
                        <QuotaBar used={mailbox.quota_used_bytes || 0} total={mailbox.quota_bytes} />
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <span
                        className="inline-flex items-center px-2 py-1 text-xs font-medium"
                        style={{
                          backgroundColor: mailbox.active ? '#DCFCE7' : '#F3F4F6',
                          color: mailbox.active ? '#166534' : '#6B7280',
                        }}
                      >
                        {mailbox.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="px-6 py-4 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link
                          to="/mailboxes/$id"
                          params={{ id: String(mailbox.id) }}
                          className="text-sm hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          View
                        </Link>
                        <button
                          onClick={() => handleDelete(mailbox.id, mailbox.address)}
                          disabled={deletingId === mailbox.id}
                          className="text-sm hover:underline disabled:opacity-50"
                          style={{ color: '#DC2626' }}
                        >
                          {deletingId === mailbox.id ? 'Deleting...' : 'Delete'}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Stats */}
        <div className="mt-6 flex items-center justify-between">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Showing {filteredMailboxes.length} of {mailboxes.length} mailboxes
          </div>
        </div>
      </div>
    </AppShell>
  )
}
