import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useMTASTSStore } from '../../lib/stores/mtastsStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/settings/mta-sts')({
  component: MTASTSPage,
})

function MTASTSPage() {
  const navigate = useNavigate()
  const { currentPolicy, fetchPolicy, setPolicy, deletePolicy, isLoading, error, clearError } =
    useMTASTSStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [selectedDomainId, setSelectedDomainId] = useState<string>('')
  const [mode, setMode] = useState<'none' | 'testing' | 'enforce'>('testing')
  const [mxHosts, setMxHosts] = useState('')
  const [maxAge, setMaxAge] = useState('604800')
  const [active, setActive] = useState(true)
  const [deleteConfirm, setDeleteConfirm] = useState(false)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDomains(accessToken).catch(console.error)
    }
  }, [isAuthenticated, accessToken])

  useEffect(() => {
    if (currentPolicy) {
      setMode(currentPolicy.mode)
      setMxHosts(currentPolicy.mx_hosts)
      setMaxAge(String(currentPolicy.max_age))
      setActive(currentPolicy.active)
    } else {
      // Reset to defaults
      setMode('testing')
      setMxHosts('')
      setMaxAge('604800')
      setActive(true)
    }
  }, [currentPolicy])

  const handleLoadPolicy = async () => {
    if (!accessToken || !selectedDomainId) return

    try {
      await fetchPolicy(parseInt(selectedDomainId), accessToken)
    } catch (err) {
      // Policy doesn't exist - that's OK, we'll show empty form
      console.log('No policy exists for this domain')
    }
  }

  const handleSavePolicy = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken || !selectedDomainId) return

    try {
      await setPolicy(
        parseInt(selectedDomainId),
        {
          mode,
          mx_hosts: mxHosts,
          max_age: parseInt(maxAge),
          active,
        },
        accessToken
      )
      // Reload the policy to show updated data
      await fetchPolicy(parseInt(selectedDomainId), accessToken)
    } catch (err) {
      console.error('Failed to save policy:', err)
    }
  }

  const handleDeletePolicy = async () => {
    if (!accessToken || !selectedDomainId) return

    try {
      await deletePolicy(parseInt(selectedDomainId), accessToken)
      setDeleteConfirm(false)
      // Clear form
      setMode('testing')
      setMxHosts('')
      setMaxAge('604800')
      setActive(true)
    } catch (err) {
      console.error('Failed to delete policy:', err)
    }
  }

  const secondsToDays = (seconds: number): number => {
    return Math.floor(seconds / 86400)
  }

  const generatePolicyPreview = (): string => {
    const hosts = mxHosts
      .split(/[,\n]/)
      .map((h) => h.trim())
      .filter(Boolean)
    const lines = ['version: STSv1', `mode: ${mode}`, ...hosts.map((mx) => `mx: ${mx}`), `max_age: ${maxAge}`]
    return lines.join('\n')
  }

  return (
    <AppShell title="MTA-STS Policy Management" backLink="/settings">
      <div className="mb-6">
        <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          Configure Mail Transfer Agent Strict Transport Security policies per domain
        </p>
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

      {/* Domain Selector */}
      <div className="mb-6">
        <div className="flex gap-3">
          <select
            value={selectedDomainId}
            onChange={(e) => setSelectedDomainId(e.target.value)}
            className="flex-1 h-11 px-4 border rounded"
            style={{ borderColor: 'var(--gray-border)' }}
          >
            <option value="">Select a domain...</option>
            {domains.map((domain) => (
              <option key={domain.id} value={domain.id}>
                {domain.name}
              </option>
            ))}
          </select>
          <button
            onClick={handleLoadPolicy}
            disabled={!selectedDomainId || isLoading}
            className="h-11 px-6 text-white text-sm font-medium rounded"
            style={{
              backgroundColor:
                !selectedDomainId || isLoading ? 'var(--gray-muted)' : 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            {isLoading ? 'Loading...' : 'Load Policy'}
          </button>
        </div>
      </div>

      {selectedDomainId && (
        <form onSubmit={handleSavePolicy}>
          <div className="border p-6 mb-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h3
              className="text-base font-semibold mb-4"
              style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
            >
              Policy Configuration for{' '}
              {domains.find((d) => d.id === parseInt(selectedDomainId))?.name}
            </h3>

            <div className="space-y-4">
              {/* Mode */}
              <div>
                <label
                  className="block text-sm font-medium mb-2"
                  style={{ color: 'var(--black-soft)' }}
                >
                  Mode
                </label>
                <div className="flex gap-4">
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      value="none"
                      checked={mode === 'none'}
                      onChange={(e) => setMode(e.target.value as any)}
                      className="h-4 w-4"
                    />
                    <span className="text-sm" style={{ color: 'var(--black-soft)' }}>
                      None
                    </span>
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      value="testing"
                      checked={mode === 'testing'}
                      onChange={(e) => setMode(e.target.value as any)}
                      className="h-4 w-4"
                    />
                    <span className="text-sm" style={{ color: 'var(--black-soft)' }}>
                      Testing
                    </span>
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      value="enforce"
                      checked={mode === 'enforce'}
                      onChange={(e) => setMode(e.target.value as any)}
                      className="h-4 w-4"
                    />
                    <span className="text-sm" style={{ color: 'var(--black-soft)' }}>
                      Enforce
                    </span>
                  </label>
                </div>
                <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                  Use "testing" mode first to monitor without enforcing
                </p>
              </div>

              {/* MX Hosts */}
              <div>
                <label
                  className="block text-sm font-medium mb-2"
                  style={{ color: 'var(--black-soft)' }}
                >
                  MX Hosts
                </label>
                <textarea
                  value={mxHosts}
                  onChange={(e) => setMxHosts(e.target.value)}
                  required
                  rows={4}
                  placeholder="mx1.example.com, mx2.example.com, *.example.com"
                  className="w-full px-4 py-3 border rounded font-mono text-sm"
                  style={{ borderColor: 'var(--gray-border)' }}
                />
                <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                  Comma-separated or one per line. Wildcards supported (e.g., *.example.com)
                </p>
              </div>

              {/* Max Age */}
              <div>
                <label
                  className="block text-sm font-medium mb-2"
                  style={{ color: 'var(--black-soft)' }}
                >
                  Max Age
                </label>
                <div className="flex gap-3 items-center">
                  <input
                    type="number"
                    value={maxAge}
                    onChange={(e) => setMaxAge(e.target.value)}
                    required
                    min="86400"
                    className="w-40 h-11 px-4 border rounded"
                    style={{ borderColor: 'var(--gray-border)' }}
                  />
                  <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    seconds ({secondsToDays(parseInt(maxAge || '0'))} days)
                  </span>
                </div>
                <div className="flex gap-2 mt-2">
                  <button
                    type="button"
                    onClick={() => setMaxAge('86400')}
                    className="text-xs px-3 py-1 border rounded"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                  >
                    1 day
                  </button>
                  <button
                    type="button"
                    onClick={() => setMaxAge('604800')}
                    className="text-xs px-3 py-1 border rounded"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                  >
                    7 days
                  </button>
                  <button
                    type="button"
                    onClick={() => setMaxAge('2592000')}
                    className="text-xs px-3 py-1 border rounded"
                    style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                  >
                    30 days
                  </button>
                </div>
              </div>

              {/* Active */}
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="active"
                  checked={active}
                  onChange={(e) => setActive(e.target.checked)}
                  className="h-4 w-4"
                />
                <label htmlFor="active" className="text-sm" style={{ color: 'var(--black-soft)' }}>
                  Active
                </label>
              </div>
            </div>

            {/* Action Buttons */}
            <div className="flex gap-3 mt-6">
              <button
                type="submit"
                disabled={isLoading}
                className="h-10 px-6 text-white text-sm font-medium rounded"
                style={{
                  backgroundColor: isLoading ? 'var(--gray-muted)' : 'var(--red-primary)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                {isLoading ? 'Saving...' : 'Save Policy'}
              </button>
              {currentPolicy && (
                <>
                  {deleteConfirm ? (
                    <>
                      <button
                        type="button"
                        onClick={handleDeletePolicy}
                        className="h-10 px-6 text-white text-sm font-medium rounded"
                        style={{ backgroundColor: '#DC2626' }}
                      >
                        Confirm Delete
                      </button>
                      <button
                        type="button"
                        onClick={() => setDeleteConfirm(false)}
                        className="h-10 px-6 text-sm font-medium border rounded"
                        style={{
                          borderColor: 'var(--gray-border)',
                          color: 'var(--gray-secondary)',
                        }}
                      >
                        Cancel
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      onClick={() => setDeleteConfirm(true)}
                      className="h-10 px-6 text-sm font-medium border rounded"
                      style={{ borderColor: '#DC2626', color: '#DC2626' }}
                    >
                      Delete Policy
                    </button>
                  )}
                </>
              )}
            </div>
          </div>

          {/* Policy Preview */}
          <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h3
              className="text-base font-semibold mb-3"
              style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
            >
              Generated Policy File Preview
            </h3>
            <p className="text-xs mb-3" style={{ color: 'var(--gray-secondary)' }}>
              This policy will be served at:{' '}
              <code
                className="px-2 py-1 rounded"
                style={{ backgroundColor: 'var(--bg-surface)' }}
              >
                https://mta-sts.{domains.find((d) => d.id === parseInt(selectedDomainId))?.name}
                /.well-known/mta-sts.txt
              </code>
            </p>
            <pre
              className="p-4 border rounded text-sm"
              style={{
                borderColor: 'var(--gray-border)',
                backgroundColor: 'var(--bg-surface)',
                fontFamily: 'monospace',
              }}
            >
              {generatePolicyPreview()}
            </pre>
          </div>
        </form>
      )}

      {/* Info Box */}
      {!selectedDomainId && (
        <div
          className="border p-6"
          style={{
            borderColor: 'var(--gray-border)',
            backgroundColor: 'var(--bg-surface)',
          }}
        >
          <h3
            className="text-base font-semibold mb-2"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            About MTA-STS
          </h3>
          <p className="text-sm mb-3" style={{ color: 'var(--gray-secondary)' }}>
            MTA-STS (Mail Transfer Agent Strict Transport Security) enables mail service providers to
            declare their ability to receive TLS-secured connections and to specify whether sending
            MTAs should refuse to deliver to MX hosts that do not offer TLS.
          </p>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Select a domain above to configure or view its MTA-STS policy.
          </p>
        </div>
      )}
    </AppShell>
  )
}
