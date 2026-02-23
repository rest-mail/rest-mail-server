import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'
import { DomainDNS } from '../../components/domains/DomainDNS'

export const Route = createFileRoute('/domains/$id')({
  component: DomainDetailsPage,
})

function DomainDetailsPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { currentDomain, fetchDomain, updateDomain, deleteDomain, isLoading, error, clearError, clearCurrentDomain } =
    useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [isEditing, setIsEditing] = useState(false)
  const [formData, setFormData] = useState({
    name: '',
    server_type: 'traditional',
    active: true,
  })
  const [deleteConfirm, setDeleteConfirm] = useState(false)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDomain(id, accessToken).catch((err) => {
        console.error('Failed to fetch domain:', err)
      })
    }

    return () => {
      clearCurrentDomain()
    }
  }, [id, isAuthenticated, accessToken, navigate, fetchDomain, clearCurrentDomain])

  useEffect(() => {
    if (currentDomain) {
      setFormData({
        name: currentDomain.name,
        server_type: currentDomain.server_type,
        active: currentDomain.active,
      })
    }
  }, [currentDomain])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken) return

    try {
      await updateDomain(id, formData, accessToken)
      setIsEditing(false)
    } catch (err) {
      console.error('Failed to update domain:', err)
    }
  }

  const handleDelete = async () => {
    if (!accessToken) return

    try {
      await deleteDomain(id, accessToken)
      navigate({ to: '/domains' })
    } catch (err) {
      console.error('Failed to delete domain:', err)
    }
  }

  const getDnsRecordIcon = (verified: boolean) => {
    return verified ? '✓' : '✗'
  }

  const getDnsRecordColor = (verified: boolean) => {
    return verified ? 'var(--success)' : '#EF4444'
  }

  if (isLoading && !currentDomain) {
    return (
      <AppShell title="Loading...">
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading domain...
          </p>
        </div>
      </AppShell>
    )
  }

  if (!currentDomain) {
    return (
      <AppShell title="Not Found">
        <div className="text-center py-12">
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Domain not found
          </p>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title={currentDomain.name}>
      <div className="flex items-center justify-between mb-6">
        <div>
          <div className="flex items-center gap-3 mb-1">
            <Link
              to="/domains"
              className="text-sm font-medium hover:underline"
              style={{ color: 'var(--gray-secondary)' }}
            >
              ← Domains
            </Link>
          </div>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Domain configuration and DNS records
          </p>
        </div>
        <div className="flex gap-3">
          {isEditing ? (
            <>
              <button
                onClick={() => setIsEditing(false)}
                className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--gray-secondary)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Cancel
              </button>
              <button
                onClick={handleSubmit}
                disabled={isLoading}
                className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium"
                style={{
                  backgroundColor: 'var(--red-primary)',
                  fontFamily: 'Space Grotesk',
                  opacity: isLoading ? 0.6 : 1,
                  cursor: isLoading ? 'not-allowed' : 'pointer',
                }}
              >
                Save Changes
              </button>
            </>
          ) : (
            <>
              <button
                onClick={() => setIsEditing(true)}
                className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--black-soft)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Edit
              </button>
              {deleteConfirm ? (
                <>
                  <button
                    onClick={handleDelete}
                    className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium"
                    style={{
                      backgroundColor: '#DC2626',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    Confirm Delete
                  </button>
                  <button
                    onClick={() => setDeleteConfirm(false)}
                    className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
                    style={{
                      borderColor: 'var(--gray-border)',
                      color: 'var(--gray-secondary)',
                      fontFamily: 'Space Grotesk',
                    }}
                  >
                    Cancel
                  </button>
                </>
              ) : (
                <button
                  onClick={() => setDeleteConfirm(true)}
                  className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
                  style={{
                    borderColor: '#EF4444',
                    color: '#DC2626',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  Delete
                </button>
              )}
            </>
          )}
        </div>
      </div>

      {/* Error Message */}
      {error && (
        <div className="mb-6">
          <div
            className="p-4 border flex items-center justify-between"
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

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
          {/* Domain Information */}
          <div>
            <h2 className="text-lg font-semibold mb-4" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
              Domain Information
            </h2>

            {isEditing ? (
              <form onSubmit={handleSubmit} className="flex flex-col gap-5 border p-6" style={{ borderColor: 'var(--gray-border)' }}>
                {/* Domain Name */}
                <div className="flex flex-col gap-2">
                  <label className="text-[13px]" style={{ color: 'var(--black-soft)' }}>
                    Domain Name
                  </label>
                  <div
                    className="h-11 px-4 flex items-center border"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <input
                      type="text"
                      value={formData.name}
                      onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                      placeholder="example.com"
                      required
                      className="w-full outline-none text-sm"
                      style={{ color: 'var(--black-soft)' }}
                    />
                  </div>
                </div>

                {/* Domain Type */}
                <div className="flex flex-col gap-2">
                  <label className="text-[13px]" style={{ color: 'var(--black-soft)' }}>
                    Domain Type
                  </label>
                  <div
                    className="h-11 px-4 flex items-center border"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <select
                      value={formData.server_type}
                      onChange={(e) => setFormData({ ...formData, server_type: e.target.value })}
                      className="w-full outline-none text-sm"
                      style={{ color: 'var(--black-soft)' }}
                    >
                      <option value="primary">Primary</option>
                      <option value="alias">Alias</option>
                      <option value="relay">Relay</option>
                    </select>
                  </div>
                </div>

                {/* Active Status */}
                <div className="flex items-center gap-3">
                  <input
                    type="checkbox"
                    id="active"
                    checked={formData.active}
                    onChange={(e) => setFormData({ ...formData, active: e.target.checked })}
                    className="w-4 h-4"
                    style={{ accentColor: 'var(--red-primary)' }}
                  />
                  <label htmlFor="active" className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    Domain is active
                  </label>
                </div>
              </form>
            ) : (
              <div className="border p-6 flex flex-col gap-4" style={{ borderColor: 'var(--gray-border)' }}>
                <div>
                  <p className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    DOMAIN NAME
                  </p>
                  <p className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    {currentDomain.name}
                  </p>
                </div>
                <div>
                  <p className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    DOMAIN TYPE
                  </p>
                  <p className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    {currentDomain.server_type}
                  </p>
                </div>
                <div>
                  <p className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    STATUS
                  </p>
                  <span
                    className="inline-flex items-center h-6 px-2 text-xs font-medium"
                    style={{
                      backgroundColor: currentDomain.active ? '#ECFDF5' : '#F3F4F6',
                      color: currentDomain.active ? '#10B981' : 'var(--gray-secondary)',
                    }}
                  >
                    {currentDomain.active ? 'Active' : 'Inactive'}
                  </span>
                </div>
                <div>
                  <p className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
                    CREATED
                  </p>
                  <p className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    {new Date(currentDomain.created_at).toLocaleString()}
                  </p>
                </div>
              </div>
            )}
          </div>

          {/* DNS Configuration */}
          <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
            {accessToken && (
              <DomainDNS
                domainId={currentDomain.id}
                domainName={currentDomain.name}
                accessToken={accessToken}
              />
            )}
          </div>
        </div>
    </AppShell>
  )
}
