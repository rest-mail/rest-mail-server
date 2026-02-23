import { useEffect, useState } from 'react'
import { CheckCircle, XCircle, AlertCircle, RefreshCw } from 'lucide-react'
import { apiV1 } from '../../lib/api'

interface DNSRecord {
  type: string
  name: string
  value?: string
  status: 'ok' | 'missing' | 'error'
  message?: string
}

interface DNSCheckResponse {
  domain: string
  records: DNSRecord[]
  summary: string
}

interface DomainDNSProps {
  domainId: number
  domainName: string
  accessToken: string
}

export function DomainDNS({ domainId, domainName, accessToken }: DomainDNSProps) {
  const [dnsData, setDnsData] = useState<DNSCheckResponse | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [lastChecked, setLastChecked] = useState<Date | null>(null)

  const checkDNS = async () => {
    setIsLoading(true)
    setError(null)

    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/dns`,
        { method: 'GET' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to check DNS')
      }

      const data = await response.json()
      setDnsData(data)
      setLastChecked(new Date())
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to check DNS')
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => {
    checkDNS()
  }, [domainId, accessToken])

  const getStatusIcon = (status: string) => {
    if (status === 'ok') {
      return <CheckCircle className="w-5 h-5" style={{ color: 'var(--green-success)' }} />
    } else if (status === 'error') {
      return <XCircle className="w-5 h-5" style={{ color: 'var(--red-primary)' }} />
    } else {
      return <AlertCircle className="w-5 h-5" style={{ color: 'var(--gray-secondary)' }} />
    }
  }

  const getStatusBadge = (status: string) => {
    if (status === 'ok') {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'rgba(34, 197, 94, 0.1)',
            color: 'var(--green-success)',
          }}
        >
          Verified
        </span>
      )
    } else if (status === 'error') {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'rgba(228, 35, 19, 0.1)',
            color: 'var(--red-primary)',
          }}
        >
          Error
        </span>
      )
    } else {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'var(--bg-surface)',
            color: 'var(--gray-secondary)',
          }}
        >
          Missing
        </span>
      )
    }
  }

  const formatTimestamp = (date: Date) => {
    return date.toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    })
  }

  if (isLoading && !dnsData) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-center">
          <div className="w-8 h-8 border-4 border-gray-200 border-t-[var(--red-primary)] rounded-full animate-spin mx-auto mb-4"></div>
          <p style={{ color: 'var(--gray-secondary)' }}>Checking DNS records...</p>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-6 rounded-lg" style={{ border: '1px solid var(--red-primary)', backgroundColor: 'rgba(228, 35, 19, 0.05)' }}>
        <div className="flex items-start gap-3">
          <XCircle className="w-5 h-5 flex-shrink-0" style={{ color: 'var(--red-primary)' }} />
          <div className="flex-1">
            <p style={{ color: 'var(--black-soft)' }} className="font-semibold mb-1">
              DNS Check Failed
            </p>
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
              {error}
            </p>
          </div>
          <button
            onClick={checkDNS}
            disabled={isLoading}
            className="flex items-center gap-2 px-3 py-1.5 rounded text-sm"
            style={{
              border: '1px solid var(--gray-border)',
              color: 'var(--black-soft)',
            }}
          >
            <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
            Retry
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Header with Refresh Button */}
      <div className="flex items-center justify-between">
        <div>
          <h3
            style={{
              fontFamily: 'Space Grotesk, sans-serif',
              color: 'var(--black-soft)',
            }}
            className="text-lg font-semibold"
          >
            DNS Configuration
          </h3>
          {lastChecked && (
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
              Last checked: {formatTimestamp(lastChecked)}
            </p>
          )}
        </div>
        <button
          onClick={checkDNS}
          disabled={isLoading}
          className="flex items-center gap-2 px-4 py-2 rounded-lg"
          style={{
            border: '1px solid var(--gray-border)',
            color: 'var(--black-soft)',
          }}
        >
          <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      {/* DNS Records */}
      <div className="space-y-3">
        {dnsData?.records && dnsData.records.length > 0 ? (
          dnsData.records.map((record, index) => (
            <div
              key={index}
              className="p-4 rounded-lg"
              style={{ border: '1px solid var(--gray-border)' }}
            >
              <div className="flex items-start justify-between mb-3">
                <div className="flex items-center gap-3">
                  {getStatusIcon(record.status)}
                  <div>
                    <div className="flex items-center gap-2">
                      <span
                        style={{
                          fontFamily: 'Space Grotesk, sans-serif',
                          color: 'var(--black-soft)',
                        }}
                        className="font-semibold"
                      >
                        {record.type}
                      </span>
                    </div>
                    <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
                      {record.name}
                    </p>
                  </div>
                </div>
                {getStatusBadge(record.status)}
              </div>

              {record.value && (
                <div
                  className="p-3 rounded text-sm font-mono break-all"
                  style={{
                    backgroundColor: 'var(--bg-surface)',
                    color: 'var(--gray-secondary)',
                  }}
                >
                  {record.value}
                </div>
              )}

              {record.message && (
                <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-2">
                  {record.message}
                </p>
              )}
            </div>
          ))
        ) : (
          <div className="text-center py-8">
            <AlertCircle className="w-12 h-12 mx-auto mb-4" style={{ color: 'var(--gray-secondary)' }} />
            <p style={{ color: 'var(--gray-secondary)' }}>No DNS records found</p>
          </div>
        )}
      </div>
    </div>
  )
}
