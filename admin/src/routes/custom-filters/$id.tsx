import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useCustomFilterStore } from '../../lib/stores/customFilterStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { useUIStore } from '../../lib/stores/uiStore'
import { AppShell } from '../../components/layout/AppShell'
import { Play } from 'lucide-react'

export const Route = createFileRoute('/custom-filters/$id')({
  component: CustomFilterEditorPage,
})

const FILTER_TEMPLATE = `/**
 * Custom email filter
 * @param {EmailJSON} email - The email object
 * @returns {FilterResult} - The filter result
 */
function filter(email) {
  // Your filter logic here

  // Example: Reject emails with specific subject
  if (email.headers.subject && email.headers.subject.includes('SPAM')) {
    return {
      action: 'reject',
      message: 'Rejected: Subject contains spam keyword'
    }
  }

  // Example: Add custom header
  if (!email.headers.extra) {
    email.headers.extra = {}
  }
  email.headers.extra['X-Custom-Filter'] = 'Processed'

  return {
    action: 'continue',
    message: email
  }
}
`

const SAMPLE_EMAIL = {
  headers: {
    from: 'sender@example.com',
    to: 'recipient@example.com',
    subject: 'Test Email',
    extra: {},
  },
  body: 'This is a test email body.',
  attachments: [],
}

function CustomFilterEditorPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { currentFilter, fetchFilter, updateFilter, validateScript, testFilter, isLoading, error, clearError } = useCustomFilterStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const { addNotification } = useUIStore()
  const [domainId, setDomainId] = useState<number>(0)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [filterType, setFilterType] = useState<'action' | 'transform'>('action')
  const [direction, setDirection] = useState<'inbound' | 'outbound' | 'both'>('inbound')
  const [enabled, setEnabled] = useState(true)
  const [script, setScript] = useState(FILTER_TEMPLATE)
  const [testEmail, setTestEmail] = useState(JSON.stringify(SAMPLE_EMAIL, null, 2))
  const [testResult, setTestResult] = useState<any>(null)
  const [validationResult, setValidationResult] = useState<any>(null)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDomains(accessToken).catch((err) => {
        console.error('Failed to fetch domains:', err)
      })
      fetchFilter(parseInt(id), accessToken).catch((err) => {
        console.error('Failed to fetch custom filter:', err)
      })
    }
  }, [isAuthenticated, accessToken, id, navigate, fetchFilter, fetchDomains])

  useEffect(() => {
    if (currentFilter) {
      setDomainId(currentFilter.domain_id)
      setName(currentFilter.name)
      setDescription(currentFilter.description)
      setFilterType(currentFilter.filter_type)
      setDirection(currentFilter.direction)
      setEnabled(currentFilter.enabled)
      setScript(currentFilter.config.script || FILTER_TEMPLATE)
    }
  }, [currentFilter])

  const handleSave = async () => {
    if (!accessToken || domainId === 0 || !name.trim()) {
      addNotification({
        type: 'error',
        message: 'Please fill in all required fields',
      })
      return
    }

    try {
      await updateFilter(
        parseInt(id),
        {
          domain_id: domainId,
          name,
          description,
          filter_type: filterType,
          direction,
          enabled,
          config: {
            script,
          },
        },
        accessToken
      )
      addNotification({
        type: 'success',
        message: 'Custom filter updated successfully',
      })
      navigate({ to: '/custom-filters' })
    } catch (err) {
      console.error('Failed to update custom filter:', err)
      addNotification({
        type: 'error',
        message: 'Failed to update custom filter',
      })
    }
  }

  const handleValidate = async () => {
    if (!accessToken) return

    try {
      let emailObj
      try {
        emailObj = JSON.parse(testEmail)
      } catch (e) {
        addNotification({
          type: 'error',
          message: 'Invalid test email JSON',
        })
        return
      }

      const result = await validateScript(script, accessToken, emailObj)
      setValidationResult(result)

      if (result.valid) {
        addNotification({
          type: 'success',
          message: 'Script is valid!',
        })
      } else {
        addNotification({
          type: 'error',
          message: 'Script validation failed',
        })
      }
    } catch (err) {
      console.error('Failed to validate script:', err)
      addNotification({
        type: 'error',
        message: 'Failed to validate script',
      })
    }
  }

  const handleTest = async () => {
    if (!accessToken) return

    try {
      let emailObj
      try {
        emailObj = JSON.parse(testEmail)
      } catch (e) {
        addNotification({
          type: 'error',
          message: 'Invalid test email JSON',
        })
        return
      }

      const result = await testFilter(parseInt(id), accessToken, emailObj)
      setTestResult(result)

      addNotification({
        type: 'info',
        message: `Test completed: ${result.action}`,
      })
    } catch (err) {
      console.error('Failed to test filter:', err)
      addNotification({
        type: 'error',
        message: 'Failed to test filter',
      })
    }
  }

  return (
    <AppShell title="Edit Custom Filter">
      <div className="mb-6">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-2xl font-bold" style={{ color: 'var(--black-soft)' }}>
            Edit Custom Filter
          </h2>
          <div className="flex gap-3">
            <button
              onClick={() => navigate({ to: '/custom-filters' })}
              className="h-10 px-6 flex items-center justify-center text-sm font-medium rounded border"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--gray-secondary)',
                fontFamily: 'Space Grotesk',
              }}
            >
              Cancel
            </button>
            <button
              onClick={handleValidate}
              disabled={isLoading}
              className="h-10 px-6 flex items-center justify-center text-sm font-medium rounded border"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--black-soft)',
                fontFamily: 'Space Grotesk',
                opacity: isLoading ? 0.5 : 1,
              }}
            >
              Validate
            </button>
            <button
              onClick={handleSave}
              disabled={isLoading || domainId === 0 || !name.trim()}
              className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
              style={{
                backgroundColor: 'var(--red-primary)',
                fontFamily: 'Space Grotesk',
                opacity: isLoading || domainId === 0 || !name.trim() ? 0.5 : 1,
              }}
            >
              {isLoading ? 'Saving...' : 'Save Filter'}
            </button>
          </div>
        </div>

        {/* Error Message */}
        {error && (
          <div className="mb-4">
            <div
              className="p-4 border flex items-center justify-between rounded"
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
      </div>

      <div className="grid grid-cols-3 gap-6">
        {/* Left Panel - Configuration */}
        <div className="col-span-1 space-y-6">
          <div className="border rounded p-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h3 className="text-lg font-semibold mb-4" style={{ color: 'var(--black-soft)' }}>
              Configuration
            </h3>

            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Name *
                </label>
                <input
                  type="text"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="My Custom Filter"
                  className="w-full h-11 px-4 border rounded text-sm"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                />
              </div>

              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Description
                </label>
                <textarea
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  placeholder="Filter description..."
                  rows={3}
                  className="w-full px-4 py-2 border rounded text-sm"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                />
              </div>

              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Domain *
                </label>
                <select
                  value={domainId}
                  onChange={(e) => setDomainId(parseInt(e.target.value))}
                  className="w-full h-11 px-4 border rounded text-sm"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                >
                  <option value={0}>Select domain...</option>
                  {domains.map((domain) => (
                    <option key={domain.id} value={domain.id}>
                      {domain.name}
                    </option>
                  ))}
                </select>
              </div>

              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Filter Type
                </label>
                <div className="flex gap-2">
                  <button
                    onClick={() => setFilterType('action')}
                    className="flex-1 h-10 text-sm font-medium border rounded"
                    style={{
                      borderColor: filterType === 'action' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: filterType === 'action' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: filterType === 'action' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Action
                  </button>
                  <button
                    onClick={() => setFilterType('transform')}
                    className="flex-1 h-10 text-sm font-medium border rounded"
                    style={{
                      borderColor: filterType === 'transform' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: filterType === 'transform' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: filterType === 'transform' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Transform
                  </button>
                </div>
              </div>

              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Direction
                </label>
                <div className="flex gap-2">
                  <button
                    onClick={() => setDirection('inbound')}
                    className="flex-1 h-10 text-xs font-medium border rounded"
                    style={{
                      borderColor: direction === 'inbound' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: direction === 'inbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: direction === 'inbound' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Inbound
                  </button>
                  <button
                    onClick={() => setDirection('outbound')}
                    className="flex-1 h-10 text-xs font-medium border rounded"
                    style={{
                      borderColor: direction === 'outbound' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: direction === 'outbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: direction === 'outbound' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Outbound
                  </button>
                  <button
                    onClick={() => setDirection('both')}
                    className="flex-1 h-10 text-xs font-medium border rounded"
                    style={{
                      borderColor: direction === 'both' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: direction === 'both' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: direction === 'both' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Both
                  </button>
                </div>
              </div>

              <div>
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={enabled}
                    onChange={(e) => setEnabled(e.target.checked)}
                    className="w-4 h-4"
                  />
                  <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Enabled
                  </span>
                </label>
              </div>
            </div>
          </div>

          {/* Validation Result */}
          {validationResult && (
            <div
              className="border rounded p-4"
              style={{
                borderColor: validationResult.valid ? '#10B981' : '#EF4444',
                backgroundColor: validationResult.valid ? '#ECFDF5' : '#FEF2F2',
              }}
            >
              <h4 className="text-sm font-semibold mb-2" style={{ color: validationResult.valid ? '#10B981' : '#DC2626' }}>
                {validationResult.valid ? 'Validation Passed' : 'Validation Failed'}
              </h4>
              {validationResult.errors && validationResult.errors.length > 0 && (
                <ul className="text-xs space-y-1" style={{ color: '#DC2626' }}>
                  {validationResult.errors.map((err: string, i: number) => (
                    <li key={i}>• {err}</li>
                  ))}
                </ul>
              )}
              {validationResult.warnings && validationResult.warnings.length > 0 && (
                <ul className="text-xs space-y-1 mt-2" style={{ color: '#D97706' }}>
                  {validationResult.warnings.map((warn: string, i: number) => (
                    <li key={i}>• {warn}</li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        {/* Right Panel - Code Editor and Test Panel */}
        <div className="col-span-2 space-y-6">
          {/* Code Editor */}
          <div className="border rounded" style={{ borderColor: 'var(--gray-border)' }}>
            <div className="p-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
              <h3 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
                Filter Script
              </h3>
            </div>
            <div className="p-4">
              <textarea
                value={script}
                onChange={(e) => setScript(e.target.value)}
                className="w-full h-96 px-4 py-3 border rounded text-sm font-mono"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--black-soft)',
                  fontFamily: 'monospace',
                }}
                spellCheck={false}
              />
            </div>
          </div>

          {/* Test Panel */}
          <div className="border rounded" style={{ borderColor: 'var(--gray-border)' }}>
            <div className="p-4 border-b flex items-center justify-between" style={{ borderColor: 'var(--gray-border)' }}>
              <h3 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
                Test Filter
              </h3>
              <button
                onClick={handleTest}
                disabled={isLoading}
                className="h-9 px-4 flex items-center gap-2 text-white text-sm font-medium rounded"
                style={{
                  backgroundColor: 'var(--red-primary)',
                  fontFamily: 'Space Grotesk',
                  opacity: isLoading ? 0.5 : 1,
                }}
              >
                <Play className="w-4 h-4" />
                Run Test
              </button>
            </div>
            <div className="p-4 space-y-4">
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Test Email (JSON)
                </label>
                <textarea
                  value={testEmail}
                  onChange={(e) => setTestEmail(e.target.value)}
                  className="w-full h-32 px-4 py-3 border rounded text-xs font-mono"
                  style={{
                    borderColor: 'var(--gray-border)',
                    color: 'var(--black-soft)',
                    fontFamily: 'monospace',
                  }}
                  spellCheck={false}
                />
              </div>

              {testResult && (
                <div
                  className="border rounded p-4"
                  style={{
                    borderColor: 'var(--gray-border)',
                    backgroundColor: 'var(--bg-surface)',
                  }}
                >
                  <h4 className="text-sm font-semibold mb-2" style={{ color: 'var(--black-soft)' }}>
                    Test Result
                  </h4>
                  <div className="space-y-2 text-xs font-mono">
                    <div>
                      <span style={{ color: 'var(--gray-secondary)' }}>Action: </span>
                      <span
                        className="inline-flex items-center h-5 px-2 rounded"
                        style={{
                          backgroundColor: testResult.action === 'continue' ? '#ECFDF5' : '#FEE2E2',
                          color: testResult.action === 'continue' ? '#10B981' : '#DC2626',
                        }}
                      >
                        {testResult.action}
                      </span>
                    </div>
                    {testResult.result && (
                      <div>
                        <span style={{ color: 'var(--gray-secondary)' }}>Result: </span>
                        <span style={{ color: 'var(--black-soft)' }}>{testResult.result}</span>
                      </div>
                    )}
                    {testResult.detail && (
                      <div>
                        <span style={{ color: 'var(--gray-secondary)' }}>Detail: </span>
                        <span style={{ color: 'var(--black-soft)' }}>{testResult.detail}</span>
                      </div>
                    )}
                    {testResult.duration_ms && (
                      <div>
                        <span style={{ color: 'var(--gray-secondary)' }}>Duration: </span>
                        <span style={{ color: 'var(--black-soft)' }}>{testResult.duration_ms}ms</span>
                      </div>
                    )}
                    {testResult.errors && testResult.errors.length > 0 && (
                      <div>
                        <span style={{ color: '#DC2626' }}>Errors: </span>
                        <ul className="ml-4 mt-1 space-y-1">
                          {testResult.errors.map((err: string, i: number) => (
                            <li key={i} style={{ color: '#DC2626' }}>
                              • {err}
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                    {testResult.message && (
                      <div className="mt-3">
                        <span style={{ color: 'var(--gray-secondary)' }}>Modified Email: </span>
                        <pre className="mt-1 p-2 bg-gray-100 rounded overflow-x-auto text-xs">
                          {JSON.stringify(testResult.message, null, 2)}
                        </pre>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </AppShell>
  )
}
