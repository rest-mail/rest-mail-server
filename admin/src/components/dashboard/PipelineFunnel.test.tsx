import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { PipelineFunnel } from './PipelineFunnel'
import {
  useAnalyticsStore,
  type PipelineAnalyticsResponse,
} from '../../lib/stores/analyticsStore'

const analytics: PipelineAnalyticsResponse = {
  window: { since: '2026-07-23T00:00:00Z', until: '2026-07-23T23:59:59Z', label: '24h' },
  funnel: {
    received: [
      { transport: 'tls', count: 80 },
      { transport: 'plaintext', count: 20 },
    ],
    auth_verdicts: [
      { mechanism: 'spf', result: 'pass', count: 70 },
      { mechanism: 'spf', result: 'fail', count: 10 },
    ],
    stage_decisions: [{ filter: 'spam_filter', action: 'reject', count: 8 }],
    terminal_outcomes: [
      { direction: 'inbound', outcome: 'delivered', count: 60 },
      { direction: 'inbound', outcome: 'rejected', count: 8 },
    ],
    top_reject_reasons: [{ reason_code: 'dmarc_fail', count: 8 }],
  },
}

// The store is a real zustand store; reset it to its initial shape before each
// test and drive the render by seeding state directly (no network).
beforeEach(() => {
  useAnalyticsStore.setState({ analytics: null, window: '24h', isLoading: false, error: null })
})
afterEach(() => cleanup())

describe('<PipelineFunnel />', () => {
  it('renders the empty state when there is no analytics data', () => {
    render(<PipelineFunnel accessToken={null} />)
    expect(screen.getByText('Pipeline Funnel')).toBeTruthy()
    expect(screen.getByText('No pipeline activity in this window')).toBeTruthy()
  })

  it('renders headline tiles derived from the funnel when data is present', () => {
    useAnalyticsStore.setState({ analytics })
    render(<PipelineFunnel accessToken={null} />)

    // Headline stage tiles.
    expect(screen.getByText('Received')).toBeTruthy()
    expect(screen.getByText('Auth pass')).toBeTruthy()
    expect(screen.getByText('Delivered')).toBeTruthy()
    expect(screen.getByText('Rejected')).toBeTruthy()

    // Derived totals surfaced by summarizeFunnel: received = 80 + 20.
    expect(screen.getByText('100')).toBeTruthy()

    // Breakdown section headings render.
    expect(screen.getByText('Auth verdicts')).toBeTruthy()
    expect(screen.getByText('Terminal outcomes')).toBeTruthy()

    // Empty state must be gone once there is activity.
    expect(screen.queryByText('No pipeline activity in this window')).toBeNull()
  })
})
