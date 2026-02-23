import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/domains/new')({
  component: NewDomainPage,
})

function NewDomainPage() {
  const navigate = useNavigate()
  const { createDomain, isLoading, error, clearError } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [formData, setFormData] = useState({
    name: '',
    server_type: 'traditional',
    active: true,
  })
  const [validationError, setValidationError] = useState('')

  const validateDomainName = (domain: string): boolean => {
    // Basic domain validation
    const domainRegex = /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$/i
    return domainRegex.test(domain)
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    clearError()
    setValidationError('')

    if (!isAuthenticated || !accessToken) {
      navigate({ to: '/login' })
      return
    }

    // Validate domain name
    if (!formData.name.trim()) {
      setValidationError('Domain name is required')
      return
    }

    if (!validateDomainName(formData.name)) {
      setValidationError('Please enter a valid domain name (e.g., example.com)')
      return
    }

    try {
      await createDomain(formData, accessToken)
      navigate({ to: '/domains' })
    } catch (err) {
      console.error('Failed to create domain:', err)
    }
  }

  const handleCancel = () => {
    navigate({ to: '/domains' })
  }

  return (
    <AppShell title="Add New Domain">
      <div className="flex items-center gap-3 mb-6">
        <Link
          to="/domains"
          className="text-sm font-medium hover:underline"
          style={{ color: 'var(--gray-secondary)' }}
        >
          ← Domains
        </Link>
      </div>

      {/* Error Message */}
      {(error || validationError) && (
        <div className="mb-6">
          <div
            className="p-4 border flex items-center justify-between"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            <span className="text-sm">{error || validationError}</span>
            <button
              onClick={() => {
                clearError()
                setValidationError('')
              }}
              className="text-sm font-medium"
              style={{ color: '#DC2626' }}
            >
              Dismiss
            </button>
          </div>
        </div>
      )}

      <div className="max-w-3xl">
        <form onSubmit={handleSubmit} className="flex flex-col gap-6">
          {/* Form Card */}
          <div className="border p-6 flex flex-col gap-5" style={{ borderColor: 'var(--gray-border)' }}>
            {/* Domain Name */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
                Domain Name
              </label>
              <div
                className="h-11 px-4 flex items-center border"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                <input
                  type="text"
                  value={formData.name}
                  onChange={(e) => setFormData({ ...formData, name: e.target.value.toLowerCase() })}
                  placeholder="example.com"
                  required
                  className="w-full outline-none text-sm"
                  style={{ color: 'var(--black-soft)' }}
                />
              </div>
              <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                Enter the domain name you want to use for email (e.g., example.com)
              </p>
            </div>

            {/* Domain Type */}
            <div className="flex flex-col gap-2">
              <label className="text-[13px] font-medium" style={{ color: 'var(--black-soft)' }}>
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
                  <option value="traditional">Traditional</option>
                  <option value="restmail">RestMail</option>
                </select>
              </div>
              <div className="flex flex-col gap-1">
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  <strong>Traditional:</strong> Standard SMTP mail server with IMAP/POP3 support
                </p>
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  <strong>RestMail:</strong> REST API-based mail delivery for modern applications
                </p>
              </div>
            </div>

            {/* Active Status */}
            <div className="flex items-start gap-3 pt-2">
              <input
                type="checkbox"
                id="active"
                checked={formData.active}
                onChange={(e) => setFormData({ ...formData, active: e.target.checked })}
                className="w-4 h-4 mt-0.5"
                style={{ accentColor: 'var(--red-primary)' }}
              />
              <div className="flex flex-col gap-1">
                <label htmlFor="active" className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Activate domain immediately
                </label>
                <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Uncheck this if you want to configure DNS records before activating
                </p>
              </div>
            </div>
          </div>

          {/* DNS Configuration Info */}
          <div
            className="border p-6"
            style={{
              borderColor: 'var(--gray-border)',
              backgroundColor: 'var(--bg-surface)',
            }}
          >
            <h3 className="text-sm font-semibold mb-3" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
              Next Steps: DNS Configuration
            </h3>
            <p className="text-xs mb-3" style={{ color: 'var(--gray-secondary)' }}>
              After creating the domain, you'll need to configure the following DNS records:
            </p>
            <ul className="list-disc list-inside text-xs space-y-1" style={{ color: 'var(--gray-secondary)' }}>
              <li>
                <strong>MX Record:</strong> Mail exchange record for receiving email
              </li>
              <li>
                <strong>SPF Record:</strong> Sender Policy Framework for email authentication
              </li>
              <li>
                <strong>DKIM Record:</strong> DomainKeys Identified Mail for email signing
              </li>
            </ul>
            <p className="text-xs mt-3" style={{ color: 'var(--gray-secondary)' }}>
              These records will be displayed on the domain details page after creation.
            </p>
          </div>

          {/* Form Actions */}
          <div className="flex gap-3">
            <button
              type="button"
              onClick={handleCancel}
              className="h-11 px-6 flex items-center justify-center text-sm font-medium border"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--gray-secondary)',
                fontFamily: 'Space Grotesk',
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isLoading}
              className="flex-1 h-11 flex items-center justify-center text-white text-sm font-medium"
              style={{
                backgroundColor: 'var(--red-primary)',
                fontFamily: 'Space Grotesk',
                opacity: isLoading ? 0.6 : 1,
                cursor: isLoading ? 'not-allowed' : 'pointer',
              }}
            >
              {isLoading ? 'Creating Domain...' : 'Create Domain'}
            </button>
          </div>
        </form>
      </div>
    </AppShell>
  )
}
