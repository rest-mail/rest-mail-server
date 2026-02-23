import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTLSReportStore } from '../../lib/stores/tlsReportStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/settings/tls-reports')({
  component: TLSReportsPage,
})

function TLSReportsPage() {
  const navigate = useNavigate()
  const { reports, currentReport, fetchReports, selectReport, clearCurrentReport, isLoading, error, clearError } =
    useTLSReportStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const [filterDomain, setFilterDomain] = useState<string>('')
  const [filterReportingOrg, setFilterReportingOrg] = useState('')

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchReports(
        {
          domain_id: filterDomain ? parseInt(filterDomain) : undefined,
          reporting_org: filterReportingOrg || undefined,
        },
        accessToken
      ).catch(console.error)
      fetchDomains(accessToken).catch(console.error)
    }
  }, [isAuthenticated, accessToken, filterDomain, filterReportingOrg])

  const formatDateRange = (start: string, end: string) => {
    const startDate = new Date(start).toLocaleDateString()
    const endDate = new Date(end).toLocaleDateString()
    return `${startDate} - ${endDate}`
  }

  return (
    <AppShell title="TLS-RPT Reports" backLink="/settings">
      <div className="mb-6">
        <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          View TLS reporting data from external mail transfer agents
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

      {/* Filters */}
      <div className="flex gap-4 mb-6">
        <div className="flex-1">
          <select
            value={filterDomain}
            onChange={(e) => setFilterDomain(e.target.value)}
            className="w-full h-11 px-4 border rounded"
            style={{ borderColor: 'var(--gray-border)' }}
          >
            <option value="">All Domains</option>
            {domains.map((domain) => (
              <option key={domain.id} value={domain.id}>
                {domain.name}
              </option>
            ))}
          </select>
        </div>
        <div className="flex-1">
          <input
            type="text"
            value={filterReportingOrg}
            onChange={(e) => setFilterReportingOrg(e.target.value)}
            placeholder="Filter by reporting organization..."
            className="w-full h-11 px-4 border rounded"
            style={{ borderColor: 'var(--gray-border)' }}
          />
        </div>
      </div>

      {/* Loading State */}
      {isLoading && (
        <div className="text-center py-8" style={{ color: 'var(--gray-secondary)' }}>
          Loading TLS reports...
        </div>
      )}

      {/* Reports Table */}
      {!isLoading && reports.length > 0 && (
        <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
          <table className="w-full">
            <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
              <tr>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Domain
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Reporting Org
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Report Period
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Success
                </th>
                <th className="text-left px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Failures
                </th>
                <th className="text-right px-6 py-3 text-xs font-medium uppercase tracking-wider">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {reports.map((report) => (
                <tr
                  key={report.id}
                  className="border-t"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <td className="px-6 py-4">
                    <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                      {report.policy_domain}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                      {report.reporting_org}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                      {formatDateRange(report.start_date, report.end_date)}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    <span className="text-sm font-medium" style={{ color: '#065F46' }}>
                      {report.total_successful.toLocaleString()}
                    </span>
                  </td>
                  <td className="px-6 py-4">
                    <span
                      className="text-sm font-medium"
                      style={{ color: report.total_failure > 0 ? '#DC2626' : 'var(--gray-muted)' }}
                    >
                      {report.total_failure.toLocaleString()}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-right">
                    <button
                      onClick={() => selectReport(report)}
                      className="text-sm font-medium"
                      style={{ color: 'var(--red-primary)' }}
                    >
                      View Details
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Empty State */}
      {!isLoading && reports.length === 0 && (
        <div className="border p-12 text-center" style={{ borderColor: 'var(--gray-border)' }}>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            No TLS reports received yet
          </p>
        </div>
      )}

      {/* Report Detail Modal */}
      {currentReport && (
        <ReportDetailModal report={currentReport} onClose={clearCurrentReport} />
      )}
    </AppShell>
  )
}

interface ReportDetailModalProps {
  report: {
    id: number
    reporting_org: string
    start_date: string
    end_date: string
    policy_type: string
    policy_domain: string
    total_successful: number
    total_failure: number
    failure_details: any
    raw_report: string
  }
  onClose: () => void
}

function ReportDetailModal({ report, onClose }: ReportDetailModalProps) {
  const [showRawJson, setShowRawJson] = useState(false)

  const failureDetails = Array.isArray(report.failure_details)
    ? report.failure_details
    : report.failure_details
      ? [report.failure_details]
      : []

  return (
    <div
      className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-lg p-6 w-full max-w-4xl max-h-[90vh] overflow-y-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-6">
          <h2
            className="text-xl font-semibold"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            TLS-RPT Report Details
          </h2>
          <button onClick={onClose} className="text-xl" style={{ color: 'var(--gray-secondary)' }}>
            ×
          </button>
        </div>

        {/* Report Metadata */}
        <div className="grid grid-cols-2 gap-4 mb-6">
          <div>
            <div className="text-xs font-medium mb-1" style={{ color: 'var(--gray-muted)' }}>
              REPORTING ORGANIZATION
            </div>
            <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
              {report.reporting_org}
            </div>
          </div>
          <div>
            <div className="text-xs font-medium mb-1" style={{ color: 'var(--gray-muted)' }}>
              POLICY DOMAIN
            </div>
            <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
              {report.policy_domain}
            </div>
          </div>
          <div>
            <div className="text-xs font-medium mb-1" style={{ color: 'var(--gray-muted)' }}>
              REPORT PERIOD
            </div>
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {new Date(report.start_date).toLocaleString()} -{' '}
              {new Date(report.end_date).toLocaleString()}
            </div>
          </div>
          <div>
            <div className="text-xs font-medium mb-1" style={{ color: 'var(--gray-muted)' }}>
              POLICY TYPE
            </div>
            <div className="text-sm uppercase" style={{ color: 'var(--gray-secondary)' }}>
              {report.policy_type}
            </div>
          </div>
        </div>

        {/* Success/Failure Summary */}
        <div className="grid grid-cols-2 gap-4 mb-6">
          <div
            className="p-4 border rounded"
            style={{ borderColor: '#10B981', backgroundColor: '#D1FAE5' }}
          >
            <div className="text-xs font-medium mb-1" style={{ color: '#065F46' }}>
              SUCCESSFUL SESSIONS
            </div>
            <div className="text-2xl font-bold" style={{ color: '#065F46' }}>
              {report.total_successful.toLocaleString()}
            </div>
          </div>
          <div
            className="p-4 border rounded"
            style={{
              borderColor: report.total_failure > 0 ? '#EF4444' : '#E5E7EB',
              backgroundColor: report.total_failure > 0 ? '#FEE2E2' : '#F9FAFB',
            }}
          >
            <div
              className="text-xs font-medium mb-1"
              style={{ color: report.total_failure > 0 ? '#991B1B' : '#6B7280' }}
            >
              FAILED SESSIONS
            </div>
            <div
              className="text-2xl font-bold"
              style={{ color: report.total_failure > 0 ? '#991B1B' : '#6B7280' }}
            >
              {report.total_failure.toLocaleString()}
            </div>
          </div>
        </div>

        {/* Failure Details */}
        {failureDetails.length > 0 && (
          <div className="mb-6">
            <h3
              className="text-base font-semibold mb-3"
              style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
            >
              Failure Details
            </h3>
            <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
              <table className="w-full">
                <thead style={{ backgroundColor: 'var(--bg-surface)' }}>
                  <tr>
                    <th className="text-left px-4 py-2 text-xs font-medium uppercase">
                      Result Type
                    </th>
                    <th className="text-left px-4 py-2 text-xs font-medium uppercase">MX Host</th>
                    <th className="text-left px-4 py-2 text-xs font-medium uppercase">IP</th>
                    <th className="text-left px-4 py-2 text-xs font-medium uppercase">Count</th>
                  </tr>
                </thead>
                <tbody>
                  {failureDetails.map((detail: any, idx: number) => (
                    <tr
                      key={idx}
                      className="border-t"
                      style={{ borderColor: 'var(--gray-border)' }}
                    >
                      <td className="px-4 py-2 text-sm" style={{ color: 'var(--black-soft)' }}>
                        {detail.result_type || '-'}
                      </td>
                      <td className="px-4 py-2 text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {detail.receiving_mx_hostname || '-'}
                      </td>
                      <td
                        className="px-4 py-2 text-sm font-mono"
                        style={{ color: 'var(--gray-secondary)' }}
                      >
                        {detail.receiving_ip || '-'}
                      </td>
                      <td className="px-4 py-2 text-sm font-medium" style={{ color: '#DC2626' }}>
                        {detail.failed_session_count || 0}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {/* Raw JSON Toggle */}
        <div>
          <button
            onClick={() => setShowRawJson(!showRawJson)}
            className="text-sm font-medium mb-2"
            style={{ color: 'var(--red-primary)' }}
          >
            {showRawJson ? '− Hide' : '+ Show'} Raw JSON Report
          </button>
          {showRawJson && (
            <pre
              className="p-4 border rounded text-xs overflow-auto max-h-96"
              style={{
                borderColor: 'var(--gray-border)',
                backgroundColor: 'var(--bg-surface)',
                fontFamily: 'monospace',
              }}
            >
              {JSON.stringify(JSON.parse(report.raw_report || '{}'), null, 2)}
            </pre>
          )}
        </div>

        <div className="mt-6">
          <button
            onClick={onClose}
            className="h-10 px-6 flex items-center justify-center text-sm font-medium border rounded"
            style={{
              borderColor: 'var(--gray-border)',
              color: 'var(--gray-secondary)',
            }}
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
