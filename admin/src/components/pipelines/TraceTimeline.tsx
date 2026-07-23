import type { TraceStage } from '../../lib/stores/pipelineStore'

// actionColor maps a resolved stage/terminal action to a badge style, reused for
// the per-stage action chips in the timeline.
export function actionColor(action: string): { backgroundColor: string; color: string } {
  switch (action) {
    case 'continue':
    case 'accept':
    case 'pass':
    case 'delivered':
    case 'queued':
      return { backgroundColor: '#ECFDF5', color: '#10B981' }
    case 'reject':
    case 'rejected':
    case 'fail':
      return { backgroundColor: '#FEE2E2', color: '#DC2626' }
    case 'quarantine':
    case 'quarantined':
    case 'defer':
    case 'deferred':
      return { backgroundColor: '#FEF3C7', color: '#D97706' }
    case 'discard':
    case 'discarded':
      return { backgroundColor: '#F3F4F6', color: '#6B7280' }
    default:
      return { backgroundColor: '#F3F4F6', color: '#6B7280' }
  }
}

// TraceTimeline renders a message's ordered pipeline stages as a simple vertical
// timeline: filter → action/verdict → detail → duration. Dependency-free.
export function TraceTimeline({ stages }: { stages: TraceStage[] | null | undefined }) {
  if (!stages || stages.length === 0) {
    return (
      <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
        No stages recorded for this message.
      </p>
    )
  }

  return (
    <div className="relative">
      {/* Vertical spine */}
      <div
        className="absolute top-1 bottom-1 w-px"
        style={{ left: '7px', backgroundColor: 'var(--gray-border)' }}
      />
      <ol className="space-y-4">
        {stages.map((stage, index) => {
          const result = stage.log?.result || stage.action || (stage.skipped ? 'skipped' : '')
          const detail = stage.log?.detail || stage.skip_reason || stage.error || ''
          const durationMs = stage.duration_ms ?? stage.log?.duration_ms
          return (
            <li key={`${stage.filter_name}-${index}`} className="relative pl-8">
              {/* Node dot */}
              <span
                className="absolute left-0 top-1 w-4 h-4 rounded-full border-2 flex items-center justify-center"
                style={{
                  borderColor: stage.skipped ? 'var(--gray-border)' : 'var(--red-primary)',
                  backgroundColor: 'white',
                }}
              >
                <span
                  className="w-1.5 h-1.5 rounded-full"
                  style={{ backgroundColor: stage.skipped ? 'var(--gray-secondary)' : 'var(--red-primary)' }}
                />
              </span>

              <div className="flex items-center gap-2 flex-wrap">
                <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  {index + 1}. {stage.filter_name || 'stage'}
                </span>
                {stage.filter_type && (
                  <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                    {stage.filter_type}
                  </span>
                )}
                {result && (
                  <span
                    className="inline-flex items-center h-5 px-2 text-xs font-medium rounded"
                    style={actionColor(result)}
                  >
                    {result}
                  </span>
                )}
                {stage.skipped && (
                  <span
                    className="inline-flex items-center h-5 px-2 text-xs font-medium rounded"
                    style={{ backgroundColor: 'var(--bg-surface)', color: 'var(--gray-secondary)' }}
                  >
                    skipped
                  </span>
                )}
                {typeof durationMs === 'number' && (
                  <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                    {durationMs}ms
                  </span>
                )}
              </div>

              {detail && (
                <p className="text-xs mt-1 break-words" style={{ color: 'var(--gray-secondary)' }}>
                  {detail}
                </p>
              )}
            </li>
          )
        })}
      </ol>
    </div>
  )
}
