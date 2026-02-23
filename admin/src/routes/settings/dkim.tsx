import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useDkimStore } from '../../lib/stores/dkimStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/settings/dkim')({
  component: DkimPage,
})

function DkimPage() {
  const navigate = useNavigate()
  const { entries, fetchDkimKeys, setDkimKey, deleteDkimKey, isLoading, error, clearError } =
    useDkimStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<number | null>(null)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDkimKeys(accessToken).catch(console.error)
      fetchDomains(accessToken).catch(console.error)
    }
  }, [isAuthenticated, accessToken])

  const handleDelete = async (domainId: number) => {
    if (!accessToken) return

    try {
      await deleteDkimKey(domainId, accessToken)
      setDeleteConfirm(null)
      await fetchDkimKeys(accessToken)
    } catch (err) {
      console.error('Failed to delete DKIM key:', err)
    }
  }

  return (
    <AppShell title="DKIM Key Management" backLink="/settings">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage DKIM signing keys for email authentication
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
          Add DKIM Key
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

      {/* Loading State */}
      {isLoading && (
        <div className="text-center py-8" style={{ color: 'var(--gray-secondary)' }}>
          Loading DKIM keys...
        </div>
      )}

      {/* DKIM Table */}
      {!isLoading && entries.length > 0 && (
        <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Domain
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Selector
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Status
                </th>
                <th className="text-right px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr
                  key={entry.domain_id}
                  className="border-t"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <td className="px-6 py-4">
                    <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                      {entry.domain}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                      {entry.selector || '-'}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    {entry.has_key ? (
                      <span
                        className="inline-block px-2 py-1 text-xs font-medium rounded"
                        style={{
                          backgroundColor: '#D1FAE5',
                          color: '#065F46',
                        }}
                      >
                        Active
                      </span>
                    ) : (
                      <span
                        className="inline-block px-2 py-1 text-xs font-medium rounded"
                        style={{
                          backgroundColor: '#F3F4F6',
                          color: '#6B7280',
                        }}
                      >
                        Not Configured
                      </span>
                    )}
                  </td>
                  <td className="px-6 py-4 text-right">
                    {entry.has_key && (
                      <>
                        {deleteConfirm === entry.domain_id ? (
                          <div className="inline-flex gap-2">
                            <button
                              onClick={() => handleDelete(entry.domain_id)}
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
                            onClick={() => setDeleteConfirm(entry.domain_id)}
                            className="text-sm font-medium"
                            style={{ color: '#DC2626' }}
                          >
                            Delete
                          </button>
                        )}
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Empty State */}
      {!isLoading && entries.length === 0 && (
        <div
          className="border p-12 text-center"
          style={{ borderColor: 'var(--gray-border)' }}
        >
          <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
            No DKIM keys configured yet
          </p>
          <button
            onClick={() => setShowCreateModal(true)}
            className="h-10 px-6 inline-flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Add First DKIM Key
          </button>
        </div>
      )}

      {/* Create Modal */}
      {showCreateModal && (
        <DkimCreateModal
          domains={domains}
          onClose={() => setShowCreateModal(false)}
          onSuccess={async () => {
            setShowCreateModal(false)
            if (accessToken) {
              await fetchDkimKeys(accessToken)
            }
          }}
        />
      )}
    </AppShell>
  )
}

interface DkimCreateModalProps {
  domains: Array<{ id: number; name: string }>
  onClose: () => void
  onSuccess: () => void
}

function DkimCreateModal({ domains, onClose, onSuccess }: DkimCreateModalProps) {
  const { setDkimKey, isLoading, error, clearError } = useDkimStore()
  const { accessToken } = useAuthStore()
  const [domainId, setDomainId] = useState<string>('')
  const [selector, setSelector] = useState('mail')
  const [privateKey, setPrivateKey] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken || !domainId) return

    try {
      await setDkimKey(parseInt(domainId), { selector, private_key: privateKey }, accessToken)
      onSuccess()
    } catch (err) {
      console.error('Failed to set DKIM key:', err)
    }
  }

  return (
    <div
      className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-lg p-6 w-full max-w-2xl max-h-[90vh] overflow-y-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-6">
          <h2
            className="text-xl font-semibold"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            Add DKIM Key
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
                Domain
              </label>
              <select
                value={domainId}
                onChange={(e) => setDomainId(e.target.value)}
                required
                className="w-full h-11 px-4 border rounded"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                <option value="">Select a domain</option>
                {domains.map((domain) => (
                  <option key={domain.id} value={domain.id}>
                    {domain.name}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Selector
              </label>
              <input
                type="text"
                value={selector}
                onChange={(e) => setSelector(e.target.value)}
                required
                placeholder="mail"
                className="w-full h-11 px-4 border rounded"
                style={{ borderColor: 'var(--gray-border)' }}
              />
              <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                Default: "mail" (creates mail._domainkey.yourdomain.com)
              </p>
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Private Key (PEM format)
              </label>
              <textarea
                value={privateKey}
                onChange={(e) => setPrivateKey(e.target.value)}
                required
                rows={10}
                placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;...&#10;-----END RSA PRIVATE KEY-----"
                className="w-full px-4 py-3 border rounded font-mono text-xs"
                style={{ borderColor: 'var(--gray-border)' }}
              />
              <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                Generate with: openssl genrsa -out dkim.private 2048
              </p>
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
              {isLoading ? 'Creating...' : 'Create DKIM Key'}
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
