import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useCertificateStore } from '../../lib/stores/certificateStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/settings/certificates')({
  component: CertificatesPage,
})

function CertificatesPage() {
  const navigate = useNavigate()
  const {
    certificates,
    fetchCertificates,
    deleteCertificate,
    getExpiringCertificates,
    isLoading,
    error,
    clearError,
  } = useCertificateStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [showUploadModal, setShowUploadModal] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<number | null>(null)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchCertificates(accessToken).catch(console.error)
      fetchDomains(accessToken).catch(console.error)
    }
  }, [isAuthenticated, accessToken])

  const handleDelete = async (id: number) => {
    if (!accessToken) return

    try {
      await deleteCertificate(id, accessToken)
      setDeleteConfirm(null)
      await fetchCertificates(accessToken)
    } catch (err) {
      console.error('Failed to delete certificate:', err)
    }
  }

  const getDaysUntilExpiry = (notAfter: string): number => {
    const now = new Date()
    const expiry = new Date(notAfter)
    const diff = expiry.getTime() - now.getTime()
    return Math.floor(diff / (1000 * 60 * 60 * 24))
  }

  const getExpiryStatus = (days: number): 'expired' | 'critical' | 'warning' | 'ok' => {
    if (days < 0) return 'expired'
    if (days <= 7) return 'critical'
    if (days <= 30) return 'warning'
    return 'ok'
  }

  const expiringCount = getExpiringCertificates(30).length

  return (
    <AppShell title="Certificate Management" backLink="/settings">
      <div className="flex items-center justify-between mb-6">
        <div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Manage TLS certificates for email domains
          </p>
        </div>
        <button
          onClick={() => setShowUploadModal(true)}
          className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
          style={{
            backgroundColor: 'var(--red-primary)',
            fontFamily: 'Space Grotesk',
          }}
        >
          Upload Certificate
        </button>
      </div>

      {/* Expiring Warning */}
      {expiringCount > 0 && (
        <div className="mb-6">
          <div
            className="p-4 border flex items-center gap-3 rounded"
            style={{
              borderColor: '#F59E0B',
              backgroundColor: '#FEF3C7',
              color: '#92400E',
            }}
          >
            <span className="text-lg">⚠️</span>
            <span className="text-sm font-medium">
              {expiringCount} certificate{expiringCount > 1 ? 's' : ''} expiring in the next 30 days
            </span>
          </div>
        </div>
      )}

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
          Loading certificates...
        </div>
      )}

      {/* Certificates Table */}
      {!isLoading && certificates.length > 0 && (
        <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Domain
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Issuer
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Valid Until
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
              {certificates.map((cert) => {
                const daysLeft = getDaysUntilExpiry(cert.not_after)
                const status = getExpiryStatus(daysLeft)

                return (
                  <tr
                    key={cert.id}
                    className="border-t"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="px-6 py-4">
                      <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                        {cert.domain?.name || `Domain ${cert.domain_id}`}
                      </span>
                    </td>
                    <td className="px-6 py-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {cert.issuer}
                      </span>
                    </td>
                    <td className="px-6 py-4">
                      <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {new Date(cert.not_after).toLocaleDateString()}
                      </span>
                      <div className="text-xs mt-1" style={{ color: 'var(--gray-muted)' }}>
                        {daysLeft} days left
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      {status === 'expired' && (
                        <span
                          className="inline-block px-2 py-1 text-xs font-medium rounded"
                          style={{
                            backgroundColor: '#FEE2E2',
                            color: '#991B1B',
                          }}
                        >
                          Expired
                        </span>
                      )}
                      {status === 'critical' && (
                        <span
                          className="inline-block px-2 py-1 text-xs font-medium rounded flex items-center gap-1"
                          style={{
                            backgroundColor: '#FEE2E2',
                            color: '#991B1B',
                          }}
                        >
                          <span>⚠️</span> Critical
                        </span>
                      )}
                      {status === 'warning' && (
                        <span
                          className="inline-block px-2 py-1 text-xs font-medium rounded flex items-center gap-1"
                          style={{
                            backgroundColor: '#FEF3C7',
                            color: '#92400E',
                          }}
                        >
                          <span>⚠️</span> Expiring Soon
                        </span>
                      )}
                      {status === 'ok' && (
                        <span
                          className="inline-block px-2 py-1 text-xs font-medium rounded"
                          style={{
                            backgroundColor: '#D1FAE5',
                            color: '#065F46',
                          }}
                        >
                          Valid
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-4 text-right">
                      {deleteConfirm === cert.id ? (
                        <div className="inline-flex gap-2">
                          <button
                            onClick={() => handleDelete(cert.id)}
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
                          onClick={() => setDeleteConfirm(cert.id)}
                          className="text-sm font-medium"
                          style={{ color: '#DC2626' }}
                        >
                          Delete
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
      {!isLoading && certificates.length === 0 && (
        <div className="border p-12 text-center" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
            No certificates uploaded yet
          </p>
          <button
            onClick={() => setShowUploadModal(true)}
            className="h-10 px-6 inline-flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Upload First Certificate
          </button>
        </div>
      )}

      {/* Upload Modal */}
      {showUploadModal && (
        <CertificateUploadModal
          domains={domains}
          onClose={() => setShowUploadModal(false)}
          onSuccess={async () => {
            setShowUploadModal(false)
            if (accessToken) {
              await fetchCertificates(accessToken)
            }
          }}
        />
      )}
    </AppShell>
  )
}

interface CertificateUploadModalProps {
  domains: Array<{ id: number; name: string }>
  onClose: () => void
  onSuccess: () => void
}

function CertificateUploadModal({ domains, onClose, onSuccess }: CertificateUploadModalProps) {
  const { uploadCertificate, isLoading, error } = useCertificateStore()
  const { accessToken } = useAuthStore()
  const [domainId, setDomainId] = useState<string>('')
  const [certPem, setCertPem] = useState('')
  const [keyPem, setKeyPem] = useState('')
  const [autoRenew, setAutoRenew] = useState(true)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken || !domainId) return

    try {
      await uploadCertificate(
        {
          domain_id: parseInt(domainId),
          cert_pem: certPem,
          key_pem: keyPem,
          auto_renew: autoRenew,
        },
        accessToken
      )
      onSuccess()
    } catch (err) {
      console.error('Failed to upload certificate:', err)
    }
  }

  return (
    <div
      className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-lg p-6 w-full max-w-3xl max-h-[90vh] overflow-y-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-6">
          <h2
            className="text-xl font-semibold"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            Upload Certificate
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
                Certificate (PEM format)
              </label>
              <textarea
                value={certPem}
                onChange={(e) => setCertPem(e.target.value)}
                required
                rows={8}
                placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
                className="w-full px-4 py-3 border rounded font-mono text-xs"
                style={{ borderColor: 'var(--gray-border)' }}
              />
            </div>

            <div>
              <label
                className="block text-sm font-medium mb-2"
                style={{ color: 'var(--black-soft)' }}
              >
                Private Key (PEM format)
              </label>
              <textarea
                value={keyPem}
                onChange={(e) => setKeyPem(e.target.value)}
                required
                rows={8}
                placeholder="-----BEGIN PRIVATE KEY-----&#10;...&#10;-----END PRIVATE KEY-----"
                className="w-full px-4 py-3 border rounded font-mono text-xs"
                style={{ borderColor: 'var(--gray-border)' }}
              />
              <p className="text-xs mt-1" style={{ color: '#DC2626' }}>
                Warning: Private keys are never returned after upload. Store securely.
              </p>
            </div>

            <div className="flex items-center gap-2">
              <input
                type="checkbox"
                id="autoRenew"
                checked={autoRenew}
                onChange={(e) => setAutoRenew(e.target.checked)}
                className="h-4 w-4"
              />
              <label htmlFor="autoRenew" className="text-sm" style={{ color: 'var(--black-soft)' }}>
                Enable auto-renewal (if supported)
              </label>
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
              {isLoading ? 'Uploading...' : 'Upload Certificate'}
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
