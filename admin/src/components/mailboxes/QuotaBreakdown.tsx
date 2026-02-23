import { useMemo } from 'react'

interface QuotaBreakdownProps {
  quotaUsage: {
    subject_bytes: number
    body_bytes: number
    attachment_bytes: number
    message_count: number
  }
  quotaBytes: number
}

export function QuotaBreakdown({ quotaUsage, quotaBytes }: QuotaBreakdownProps) {
  const totalUsed = quotaUsage.subject_bytes + quotaUsage.body_bytes + quotaUsage.attachment_bytes
  const percentage = quotaBytes > 0 ? (totalUsed / quotaBytes) * 100 : 0

  const breakdown = useMemo(() => {
    if (totalUsed === 0) return []

    return [
      {
        label: 'Attachments',
        bytes: quotaUsage.attachment_bytes,
        percentage: (quotaUsage.attachment_bytes / totalUsed) * 100,
        color: '#3B82F6', // blue
      },
      {
        label: 'Message Bodies',
        bytes: quotaUsage.body_bytes,
        percentage: (quotaUsage.body_bytes / totalUsed) * 100,
        color: '#10B981', // green
      },
      {
        label: 'Headers/Metadata',
        bytes: quotaUsage.subject_bytes,
        percentage: (quotaUsage.subject_bytes / totalUsed) * 100,
        color: '#F59E0B', // amber
      },
    ]
  }, [quotaUsage, totalUsed])

  const formatBytes = (bytes: number): string => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
  }

  return (
    <div className="space-y-4">
      {/* Overall Usage */}
      <div>
        <div className="flex items-center justify-between mb-2">
          <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
            Storage Usage
          </span>
          <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            {formatBytes(totalUsed)} / {formatBytes(quotaBytes)} ({percentage.toFixed(1)}%)
          </span>
        </div>
        <div className="h-3 bg-gray-100 rounded-full overflow-hidden">
          <div
            className="h-full transition-all duration-300"
            style={{
              width: `${Math.min(percentage, 100)}%`,
              backgroundColor: percentage > 95 ? '#EF4444' : percentage > 80 ? '#F59E0B' : 'var(--success)',
            }}
          />
        </div>
      </div>

      {/* Breakdown Chart */}
      {totalUsed > 0 && (
        <div>
          <div className="text-sm font-medium mb-3" style={{ color: 'var(--black-soft)' }}>
            Storage Breakdown
          </div>

          {/* Stacked Bar */}
          <div className="h-6 bg-gray-50 rounded overflow-hidden flex mb-4">
            {breakdown.map((item, idx) => (
              <div
                key={idx}
                className="h-full transition-all duration-300"
                style={{
                  width: `${item.percentage}%`,
                  backgroundColor: item.color,
                }}
                title={`${item.label}: ${formatBytes(item.bytes)}`}
              />
            ))}
          </div>

          {/* Legend */}
          <div className="space-y-2">
            {breakdown.map((item, idx) => (
              <div key={idx} className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <div
                    className="w-3 h-3 rounded-sm"
                    style={{ backgroundColor: item.color }}
                  />
                  <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    {item.label}
                  </span>
                </div>
                <div className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  {formatBytes(item.bytes)} ({item.percentage.toFixed(1)}%)
                </div>
              </div>
            ))}
          </div>

          {/* Message Count */}
          <div className="mt-4 pt-4 border-t" style={{ borderColor: 'var(--gray-border)' }}>
            <div className="flex items-center justify-between">
              <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                Total Messages
              </span>
              <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                {quotaUsage.message_count.toLocaleString()}
              </span>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
