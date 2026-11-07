import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { TraceTimeline } from './TraceTimeline'

const requestId = '4f9c2d7e1a0b3c5d6e7f8091a2b3c4d5'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('trace request ID', () => {
  it('shows the request ID the log lines carry and copies it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render(<TraceTimeline stages={[]} requestId={requestId} />)
    expect(screen.getByText('Request ID')).toBeTruthy()
    expect(screen.getByText(requestId)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Copy request ID' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(requestId))
    expect(await screen.findByText('Copied')).toBeTruthy()
  })

  it('shows no request line for a trace written outside a request', () => {
    render(<TraceTimeline stages={[]} requestId={null} />)
    expect(screen.queryByText('Request ID')).toBeNull()
    expect(screen.getByText('No stages recorded for this message.')).toBeTruthy()
  })
})
