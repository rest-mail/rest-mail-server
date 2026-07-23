import { create } from 'zustand'
import { apiV1 } from '../api'

// ── Pipeline analytics (PR5 read surface) ────────────────────────────
//
// Consumes GET /admin/pipelines/analytics?window=24h (or ?since=RFC3339),
// which returns the durable windowed message-lifecycle funnel read from the
// pipeline_rollups aggregate — received → auth verdicts → stage decisions →
// terminal outcomes, plus the top reject reasons. Optionally carries a
// live_totals snapshot (cumulative since process start) of the same shape.
//
// The visualization is deliberately dependency-free (stat tiles + CSS bars):
// operators wanting rich over-time charts are pointed at Grafana.

interface TransportCount {
  transport: string
  count: number
}

interface AuthVerdictCount {
  mechanism: string
  result: string
  count: number
}

interface StageDecisionCount {
  filter: string
  action: string
  count: number
}

interface TerminalCount {
  direction: string
  outcome: string
  count: number
}

interface RejectReasonCount {
  reason_code: string
  count: number
}

interface PipelineFunnel {
  received: TransportCount[]
  auth_verdicts: AuthVerdictCount[]
  stage_decisions: StageDecisionCount[]
  terminal_outcomes: TerminalCount[]
  top_reject_reasons: RejectReasonCount[]
}

interface AnalyticsWindow {
  since: string
  until: string
  label?: string
}

interface PipelineAnalyticsResponse {
  window: AnalyticsWindow
  funnel: PipelineFunnel
  live_totals?: PipelineFunnel
}

// FunnelSummary is a flat, render-ready rollup of a PipelineFunnel: the derived
// stage totals the widget shows as headline tiles. Pure & deterministic so it
// is unit-testable without a store or DOM.
interface FunnelSummary {
  received: number
  receivedByTransport: Record<string, number>
  authPass: number
  authFail: number
  authByResult: Record<string, number>
  terminalByOutcome: Record<string, number>
  delivered: number
  rejected: number
}

function sumCounts<T extends { count: number }>(rows: T[]): number {
  return rows.reduce((acc, r) => acc + (r.count || 0), 0)
}

// summarizeFunnel collapses the per-label funnel series into headline totals.
export function summarizeFunnel(f: PipelineFunnel | null | undefined): FunnelSummary {
  const empty: FunnelSummary = {
    received: 0,
    receivedByTransport: {},
    authPass: 0,
    authFail: 0,
    authByResult: {},
    terminalByOutcome: {},
    delivered: 0,
    rejected: 0,
  }
  if (!f) return empty

  const receivedByTransport: Record<string, number> = {}
  for (const r of f.received ?? []) {
    receivedByTransport[r.transport || 'unknown'] =
      (receivedByTransport[r.transport || 'unknown'] ?? 0) + (r.count || 0)
  }

  const authByResult: Record<string, number> = {}
  for (const a of f.auth_verdicts ?? []) {
    authByResult[a.result || 'unknown'] = (authByResult[a.result || 'unknown'] ?? 0) + (a.count || 0)
  }

  const terminalByOutcome: Record<string, number> = {}
  for (const t of f.terminal_outcomes ?? []) {
    terminalByOutcome[t.outcome || 'unknown'] =
      (terminalByOutcome[t.outcome || 'unknown'] ?? 0) + (t.count || 0)
  }

  return {
    received: sumCounts(f.received ?? []),
    receivedByTransport,
    authPass: authByResult['pass'] ?? 0,
    authFail: authByResult['fail'] ?? 0,
    authByResult,
    terminalByOutcome,
    delivered: (terminalByOutcome['delivered'] ?? 0) + (terminalByOutcome['queued'] ?? 0),
    rejected: terminalByOutcome['rejected'] ?? 0,
  }
}

interface AnalyticsState {
  analytics: PipelineAnalyticsResponse | null
  window: string
  isLoading: boolean
  error: string | null

  fetchAnalytics: (accessToken: string, window?: string) => Promise<void>
  setWindow: (window: string) => void
  clearError: () => void
}

export const useAnalyticsStore = create<AnalyticsState>((set, get) => ({
  analytics: null,
  window: '24h',
  isLoading: false,
  error: null,

  fetchAnalytics: async (accessToken: string, window?: string) => {
    const w = window ?? get().window
    set({ isLoading: true, error: null, window: w })

    try {
      const response = await apiV1.request(
        `/admin/pipelines/analytics?window=${encodeURIComponent(w)}`,
        { method: 'GET' },
        accessToken
      )

      if (!response.ok) {
        const err = await response.json().catch(() => ({}))
        throw new Error(err?.error?.message || err?.error || 'Failed to fetch pipeline analytics')
      }

      const body = await response.json()
      const data: PipelineAnalyticsResponse = body.data ?? body

      set({ analytics: data, isLoading: false, error: null })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch pipeline analytics',
        isLoading: false,
      })
      throw error
    }
  },

  setWindow: (window: string) => set({ window }),
  clearError: () => set({ error: null }),
}))

export type {
  PipelineFunnel,
  PipelineAnalyticsResponse,
  AnalyticsWindow,
  FunnelSummary,
  TransportCount,
  AuthVerdictCount,
  StageDecisionCount,
  TerminalCount,
  RejectReasonCount,
}
