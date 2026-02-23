import { useState } from 'react'

interface RawMessageViewerProps {
  rawMessage: string
}

export function RawMessageViewer({ rawMessage }: RawMessageViewerProps) {
  const [isExpanded, setIsExpanded] = useState(false)
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(rawMessage)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center justify-between px-6 py-4 hover:bg-gray-50 transition-colors"
        style={{ backgroundColor: 'var(--bg-surface)' }}
      >
        <h2 className="text-lg font-semibold" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
          Raw Message
        </h2>
        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          {isExpanded ? '▼' : '▶'}
        </span>
      </button>

      {isExpanded && (
        <div className="p-6 border-t" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="flex justify-end mb-2">
            <button
              onClick={handleCopy}
              className="px-3 py-1 text-xs font-medium border hover:bg-gray-50 transition-colors"
              style={{
                color: 'var(--black-soft)',
                borderColor: 'var(--gray-border)',
                fontFamily: 'Space Grotesk',
              }}
            >
              {copied ? 'Copied!' : 'Copy to Clipboard'}
            </button>
          </div>
          <div
            className="overflow-auto p-4 border text-xs font-mono"
            style={{
              maxHeight: '500px',
              backgroundColor: '#F9FAFB',
              borderColor: 'var(--gray-border)',
              color: 'var(--black-soft)',
            }}
          >
            <pre className="whitespace-pre-wrap">{rawMessage}</pre>
          </div>
        </div>
      )}
    </div>
  )
}
