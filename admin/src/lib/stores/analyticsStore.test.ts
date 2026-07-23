import { describe, it, expect } from 'vitest'
import { summarizeFunnel, type PipelineFunnel } from './analyticsStore'

const funnel: PipelineFunnel = {
  received: [
    { transport: 'tls', count: 80 },
    { transport: 'plaintext', count: 20 },
  ],
  auth_verdicts: [
    { mechanism: 'spf', result: 'pass', count: 70 },
    { mechanism: 'spf', result: 'fail', count: 10 },
    { mechanism: 'dkim', result: 'pass', count: 65 },
    { mechanism: 'dmarc', result: 'fail', count: 5 },
  ],
  stage_decisions: [
    { filter: 'spf_check', action: 'continue', count: 90 },
    { filter: 'spam_filter', action: 'reject', count: 8 },
  ],
  terminal_outcomes: [
    { direction: 'inbound', outcome: 'delivered', count: 60 },
    { direction: 'inbound', outcome: 'queued', count: 12 },
    { direction: 'inbound', outcome: 'rejected', count: 8 },
    { direction: 'outbound', outcome: 'delivered', count: 15 },
  ],
  top_reject_reasons: [{ reason_code: 'dmarc_fail', count: 5 }],
}

describe('summarizeFunnel', () => {
  it('returns a zeroed summary for null/undefined', () => {
    const s = summarizeFunnel(null)
    expect(s.received).toBe(0)
    expect(s.delivered).toBe(0)
    expect(s.rejected).toBe(0)
    expect(s.receivedByTransport).toEqual({})
  })

  it('sums received counts and splits by transport', () => {
    const s = summarizeFunnel(funnel)
    expect(s.received).toBe(100)
    expect(s.receivedByTransport['tls']).toBe(80)
    expect(s.receivedByTransport['plaintext']).toBe(20)
  })

  it('aggregates auth verdicts by result', () => {
    const s = summarizeFunnel(funnel)
    expect(s.authPass).toBe(135) // 70 + 65
    expect(s.authFail).toBe(15) // 10 + 5
  })

  it('aggregates terminal outcomes across directions', () => {
    const s = summarizeFunnel(funnel)
    // delivered = delivered(60+15) + queued(12) = 87
    expect(s.delivered).toBe(87)
    expect(s.rejected).toBe(8)
    expect(s.terminalByOutcome['delivered']).toBe(75)
    expect(s.terminalByOutcome['queued']).toBe(12)
  })

  it('tolerates missing series arrays', () => {
    const partial = { received: [{ transport: 'tls', count: 3 }] } as unknown as PipelineFunnel
    const s = summarizeFunnel(partial)
    expect(s.received).toBe(3)
    expect(s.authPass).toBe(0)
    expect(s.delivered).toBe(0)
  })
})
