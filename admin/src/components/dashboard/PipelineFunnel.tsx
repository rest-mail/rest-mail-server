import { useEffect } from 'react'
import { Inbox, ShieldCheck, ShieldAlert, CheckCircle2, XCircle, Activity } from 'lucide-react'
import {
  useAnalyticsStore,
  summarizeFunnel,
  type PipelineFunnel as Funnel,
} from '../../lib/stores/analyticsStore'

// PipelineFunnel renders the windowed message-lifecycle funnel from the PR5
// analytics endpoint: received (by transport) → auth verdicts → stage decisions
// → terminal outcomes, plus top reject reasons. Deliberately dependency-free —
// stat tiles + inline CSS bars, matching the dashboard's MetricCard idiom.
// Operators wanting rich over-time charts are pointed at Grafana.

const WINDOW_OPTIONS = [
  { value: '1h', label: '1h' },
  { value: '24h', label: '24h' },
  { value: '168h', label: '7d' },
]

export function PipelineFunnel({ accessToken }: { accessToken: string | null }) {
  const { analytics, window: win, isLoading, error, fetchAnalytics, setWindow } = useAnalyticsStore()

  useEffect(() => {
    if (!accessToken) return
    fetchAnalytics(accessToken).catch((err) => {
      console.error('Failed to fetch pipeline analytics:', err)
    })
  }, [accessToken, fetchAnalytics])

  const onWindowChange = (value: string) => {
    setWindow(value)
    if (accessToken) {
      fetchAnalytics(accessToken, value).catch((err) => {
        console.error('Failed to fetch pipeline analytics:', err)
      })
    }
  }

  const funnel = analytics?.funnel ?? null
  const summary = summarizeFunnel(funnel)

  return (
    <div className="mt-8 p-6 rounded-lg" style={{ border: '1px solid var(--gray-border)' }}>
      <div className="flex items-start justify-between mb-6 gap-4 flex-wrap">
        <div>
          <h2
            style={{ fontFamily: 'Space Grotesk, sans-serif', color: 'var(--black-soft)' }}
            className="text-xl font-semibold"
          >
            Pipeline Funnel
          </h2>
          <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
            Message lifecycle over the selected window — received to terminal outcome
          </p>
        </div>
        <div className="flex items-center gap-1 rounded-lg p-1" style={{ backgroundColor: 'var(--bg-surface)' }}>
          {WINDOW_OPTIONS.map((opt) => {
            const active = win === opt.value
            return (
              <button
                key={opt.value}
                onClick={() => onWindowChange(opt.value)}
                className="px-3 py-1.5 text-sm font-medium rounded-md transition-colors"
                style={{
                  backgroundColor: active ? 'var(--red-primary)' : 'transparent',
                  color: active ? 'white' : 'var(--black-soft)',
                  fontFamily: 'Space Grotesk, sans-serif',
                }}
              >
                {opt.label}
              </button>
            )
          })}
        </div>
      </div>

      {error ? (
        <div
          className="p-4 rounded text-sm"
          style={{ border: '1px solid var(--gray-border)', color: 'var(--red-primary)' }}
        >
          {error}
        </div>
      ) : isLoading && !analytics ? (
        <div className="flex items-center justify-center py-10">
          <div className="w-6 h-6 border-4 border-gray-200 border-t-[var(--red-primary)] rounded-full animate-spin" />
        </div>
      ) : summary.received === 0 && summary.delivered === 0 && summary.rejected === 0 ? (
        <div className="flex items-center justify-center py-10">
          <div className="text-center">
            <Activity className="w-10 h-10 mx-auto mb-3" style={{ color: 'var(--gray-secondary)' }} />
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
              No pipeline activity in this window
            </p>
          </div>
        </div>
      ) : (
        <>
          {/* Headline stage tiles */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-8">
            <FunnelTile
              icon={<Inbox className="w-5 h-5" />}
              label="Received"
              value={summary.received}
              sub={`${summary.receivedByTransport['tls'] ?? 0} TLS · ${summary.receivedByTransport['plaintext'] ?? 0} plaintext`}
              color="var(--black-soft)"
            />
            <FunnelTile
              icon={<ShieldCheck className="w-5 h-5" />}
              label="Auth pass"
              value={summary.authPass}
              sub={`${summary.authFail.toLocaleString()} failed`}
              color="var(--black-soft)"
            />
            <FunnelTile
              icon={<CheckCircle2 className="w-5 h-5" />}
              label="Delivered"
              value={summary.delivered}
              sub="delivered or queued"
              color="var(--black-soft)"
            />
            <FunnelTile
              icon={<XCircle className="w-5 h-5" />}
              label="Rejected"
              value={summary.rejected}
              sub="terminal rejects"
              color={summary.rejected > 0 ? 'var(--red-primary)' : 'var(--gray-secondary)'}
              isHighlight={summary.rejected > 0}
            />
          </div>

          {/* Breakdown bar sections */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
            <BarSection
              title="Auth verdicts"
              icon={<ShieldAlert className="w-4 h-4" />}
              rows={(funnel?.auth_verdicts ?? []).map((v) => ({
                label: `${v.mechanism} · ${v.result}`,
                value: v.count,
                emphasis: v.result === 'fail',
              }))}
            />
            <BarSection
              title="Terminal outcomes"
              icon={<CheckCircle2 className="w-4 h-4" />}
              rows={outcomeRows(funnel)}
            />
            <BarSection
              title="Stage decisions"
              icon={<Activity className="w-4 h-4" />}
              rows={(funnel?.stage_decisions ?? []).map((s) => ({
                label: `${s.filter} · ${s.action}`,
                value: s.count,
                emphasis: s.action === 'reject' || s.action === 'discard',
              }))}
            />
            <BarSection
              title="Top reject reasons"
              icon={<XCircle className="w-4 h-4" />}
              rows={(funnel?.top_reject_reasons ?? []).map((r) => ({
                label: r.reason_code || 'unknown',
                value: r.count,
                emphasis: true,
              }))}
            />
          </div>

          {analytics?.window?.since && (
            <p style={{ color: 'var(--gray-secondary)' }} className="text-xs mt-6">
              Window: {new Date(analytics.window.since).toLocaleString()} –{' '}
              {new Date(analytics.window.until).toLocaleString()}
            </p>
          )}
        </>
      )}
    </div>
  )
}

// outcomeRows aggregates terminal outcomes across directions into one bar each,
// so the section reads as a compact outcome distribution.
function outcomeRows(funnel: Funnel | null) {
  const byOutcome: Record<string, number> = {}
  for (const t of funnel?.terminal_outcomes ?? []) {
    byOutcome[t.outcome || 'unknown'] = (byOutcome[t.outcome || 'unknown'] ?? 0) + (t.count || 0)
  }
  return Object.entries(byOutcome)
    .sort((a, b) => b[1] - a[1])
    .map(([outcome, value]) => ({
      label: outcome,
      value,
      emphasis: outcome === 'rejected' || outcome === 'discarded' || outcome === 'deferred',
    }))
}

interface BarRow {
  label: string
  value: number
  emphasis?: boolean
}

function BarSection({ title, icon, rows }: { title: string; icon: React.ReactNode; rows: BarRow[] }) {
  const max = rows.reduce((m, r) => Math.max(m, r.value), 0)

  return (
    <div>
      <div className="flex items-center gap-2 mb-3">
        <div style={{ color: 'var(--gray-secondary)' }}>{icon}</div>
        <h3
          className="text-sm font-semibold"
          style={{ color: 'var(--black-soft)', fontFamily: 'Space Grotesk, sans-serif' }}
        >
          {title}
        </h3>
      </div>
      {rows.length === 0 ? (
        <p style={{ color: 'var(--gray-secondary)' }} className="text-xs py-2">
          No data
        </p>
      ) : (
        <div className="space-y-2">
          {rows.map((row, i) => {
            const pct = max > 0 ? Math.max(2, Math.round((row.value / max) * 100)) : 0
            return (
              <div key={`${row.label}-${i}`}>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs" style={{ color: 'var(--black-soft)' }}>
                    {row.label}
                  </span>
                  <span className="text-xs font-medium tabular-nums" style={{ color: 'var(--gray-secondary)' }}>
                    {row.value.toLocaleString()}
                  </span>
                </div>
                <div className="h-2 rounded-full overflow-hidden" style={{ backgroundColor: 'var(--bg-surface)' }}>
                  <div
                    className="h-full rounded-full"
                    style={{
                      width: `${pct}%`,
                      backgroundColor: row.emphasis ? 'var(--red-primary)' : 'var(--black-soft)',
                      opacity: row.emphasis ? 1 : 0.55,
                    }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

interface FunnelTileProps {
  icon: React.ReactNode
  label: string
  value: number
  sub: string
  color: string
  isHighlight?: boolean
}

function FunnelTile({ icon, label, value, sub, color, isHighlight }: FunnelTileProps) {
  return (
    <div className="p-4 rounded-lg" style={{ border: '1px solid var(--gray-border)' }}>
      <div className="flex items-start justify-between mb-3">
        <div
          className="p-2 rounded-lg"
          style={{ backgroundColor: isHighlight ? 'rgba(228, 35, 19, 0.1)' : 'var(--bg-surface)' }}
        >
          <div style={{ color }}>{icon}</div>
        </div>
      </div>
      <div>
        <p
          style={{ fontFamily: 'Space Grotesk, sans-serif', color }}
          className="text-2xl font-bold mb-1"
        >
          {value.toLocaleString()}
        </p>
        <p style={{ color: 'var(--black-soft)' }} className="text-sm font-medium">
          {label}
        </p>
        <p style={{ color: 'var(--gray-secondary)' }} className="text-xs mt-0.5">
          {sub}
        </p>
      </div>
    </div>
  )
}
