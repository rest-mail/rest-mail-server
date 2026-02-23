/**
 * Filter Registry Store - Static data for built-in filters
 * This provides metadata about all available built-in filters for the UI
 */

export type FilterCategory =
  | 'authentication'
  | 'spam'
  | 'access'
  | 'content'
  | 'delivery'
  | 'technical'
  | 'custom'

export interface ConfigField {
  type: 'text' | 'number' | 'select' | 'boolean'
  label: string
  options?: string[]
  default?: any
  required?: boolean
  min?: number
  max?: number
  description?: string
}

export interface FilterDefinition {
  name: string
  displayName: string
  description: string
  type: 'action' | 'transform'
  direction: 'inbound' | 'outbound' | 'both'
  unskippable: boolean
  category: FilterCategory
  icon?: string
  configSchema?: Record<string, ConfigField>
}

// Built-in filter definitions
export const BUILTIN_FILTERS: FilterDefinition[] = [
  // Inbound Action Filters
  {
    name: 'size_check',
    displayName: 'Size Check',
    description: 'Reject messages exceeding maximum size',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'content',
    icon: 'FileCheck',
    configSchema: {
      max_size_mb: {
        type: 'number',
        label: 'Maximum Size (MB)',
        default: 25,
        required: true,
        min: 1,
        max: 100,
        description: 'Maximum allowed message size in megabytes',
      },
    },
  },
  {
    name: 'spf_check',
    displayName: 'SPF Verification',
    description: "Validates sender's IP against SPF records",
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'authentication',
    icon: 'ShieldCheck',
    configSchema: {
      fail_action: {
        type: 'select',
        label: 'Action on SPF Fail',
        options: ['tag', 'reject', 'quarantine'],
        default: 'tag',
        required: true,
        description: 'What to do when SPF verification fails',
      },
    },
  },
  {
    name: 'dmarc_check',
    displayName: 'DMARC Verification',
    description: 'Validates DMARC policy compliance',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'authentication',
    icon: 'ShieldCheck',
    configSchema: {
      fail_action: {
        type: 'select',
        label: 'Action on DMARC Fail',
        options: ['tag', 'quarantine', 'reject'],
        default: 'quarantine',
        required: true,
        description: 'What to do when DMARC verification fails',
      },
    },
  },
  {
    name: 'domain_allowlist',
    displayName: 'Domain Allowlist',
    description: 'Check sender against domain allow/block rules',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'access',
    icon: 'List',
    configSchema: {},
  },
  {
    name: 'contact_whitelist',
    displayName: 'Contact Whitelist',
    description: 'Skip spam checks for trusted contacts',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'access',
    icon: 'UserCheck',
    configSchema: {},
  },
  {
    name: 'greylist',
    displayName: 'Greylisting',
    description: 'Temporary rejection for unknown senders',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'spam',
    icon: 'Clock',
    configSchema: {
      delay_minutes: {
        type: 'number',
        label: 'Delay (minutes)',
        default: 5,
        required: true,
        min: 1,
        max: 60,
        description: 'How long to delay unknown senders',
      },
      ttl_days: {
        type: 'number',
        label: 'TTL (days)',
        default: 36,
        required: true,
        min: 1,
        max: 90,
        description: 'How long to remember a sender',
      },
    },
  },
  {
    name: 'header_validate',
    displayName: 'Header Validation',
    description: 'Validate required email headers',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'content',
    icon: 'FileCheck',
    configSchema: {},
  },
  {
    name: 'recipient_check',
    displayName: 'Recipient Check',
    description: 'Verify recipient exists in mailbox',
    type: 'action',
    direction: 'inbound',
    unskippable: true,
    category: 'delivery',
    icon: 'UserCheck',
    configSchema: {},
  },
  {
    name: 'rspamd',
    displayName: 'Rspamd Spam Filter',
    description: 'Spam scoring via Rspamd',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'spam',
    icon: 'ShieldAlert',
    configSchema: {
      host: {
        type: 'text',
        label: 'Rspamd Host',
        default: 'localhost:11333',
        required: true,
        description: 'Rspamd server address',
      },
      threshold: {
        type: 'number',
        label: 'Spam Threshold',
        default: 15,
        required: true,
        min: 1,
        max: 100,
        description: 'Score threshold for spam detection',
      },
    },
  },
  {
    name: 'clamav',
    displayName: 'ClamAV Virus Scanner',
    description: 'Virus scanning with ClamAV',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'spam',
    icon: 'ShieldAlert',
    configSchema: {
      host: {
        type: 'text',
        label: 'ClamAV Host',
        default: 'localhost',
        required: true,
        description: 'ClamAV server address',
      },
      port: {
        type: 'number',
        label: 'ClamAV Port',
        default: 3310,
        required: true,
        min: 1,
        max: 65535,
        description: 'ClamAV server port',
      },
    },
  },
  {
    name: 'duplicate',
    displayName: 'Duplicate Detection',
    description: 'Detect duplicate messages',
    type: 'action',
    direction: 'inbound',
    unskippable: false,
    category: 'content',
    icon: 'Copy',
    configSchema: {
      window_hours: {
        type: 'number',
        label: 'Time Window (hours)',
        default: 24,
        required: true,
        min: 1,
        max: 168,
        description: 'How far back to check for duplicates',
      },
    },
  },

  // Inbound Transform Filters
  {
    name: 'dkim_verify',
    displayName: 'DKIM Verification',
    description: 'Verify DKIM signatures',
    type: 'transform',
    direction: 'inbound',
    unskippable: false,
    category: 'authentication',
    icon: 'Key',
    configSchema: {
      fail_action: {
        type: 'select',
        label: 'Action on DKIM Fail',
        options: ['tag', 'reject'],
        default: 'tag',
        required: true,
        description: 'What to do when DKIM verification fails',
      },
    },
  },
  {
    name: 'arc_verify',
    displayName: 'ARC Verification',
    description: 'Verify ARC chain',
    type: 'transform',
    direction: 'inbound',
    unskippable: false,
    category: 'authentication',
    icon: 'Link',
    configSchema: {},
  },
  {
    name: 'extract_attachments',
    displayName: 'Extract Attachments',
    description: 'Extract and store attachments',
    type: 'transform',
    direction: 'inbound',
    unskippable: false,
    category: 'content',
    icon: 'Paperclip',
    configSchema: {
      storage_dir: {
        type: 'text',
        label: 'Storage Directory',
        default: '/var/mail/attachments',
        required: true,
        description: 'Where to store extracted attachments',
      },
    },
  },
  {
    name: 'sieve',
    displayName: 'Sieve Scripts',
    description: 'Apply user Sieve scripts',
    type: 'transform',
    direction: 'inbound',
    unskippable: false,
    category: 'delivery',
    icon: 'Filter',
    configSchema: {},
  },
  {
    name: 'vacation',
    displayName: 'Vacation Auto-Reply',
    description: 'Auto-reply for out-of-office',
    type: 'transform',
    direction: 'inbound',
    unskippable: false,
    category: 'delivery',
    icon: 'Reply',
    configSchema: {},
  },

  // Outbound Action Filters
  {
    name: 'sender_verify',
    displayName: 'Sender Verification',
    description: 'Verify sender is authorized',
    type: 'action',
    direction: 'outbound',
    unskippable: true,
    category: 'access',
    icon: 'UserCheck',
    configSchema: {},
  },
  {
    name: 'rate_limit',
    displayName: 'Rate Limiting',
    description: 'Limit sending rate per sender',
    type: 'action',
    direction: 'outbound',
    unskippable: false,
    category: 'delivery',
    icon: 'Gauge',
    configSchema: {
      per_sender_per_hour: {
        type: 'number',
        label: 'Messages per Hour',
        default: 100,
        required: true,
        min: 1,
        max: 10000,
        description: 'Maximum messages per sender per hour',
      },
    },
  },

  // Outbound Transform Filters
  {
    name: 'header_cleanup',
    displayName: 'Header Cleanup',
    description: 'Remove internal headers before sending',
    type: 'transform',
    direction: 'outbound',
    unskippable: false,
    category: 'technical',
    icon: 'Eraser',
    configSchema: {},
  },
  {
    name: 'arc_seal',
    displayName: 'ARC Seal',
    description: 'Add ARC-Seal headers',
    type: 'transform',
    direction: 'outbound',
    unskippable: false,
    category: 'authentication',
    icon: 'Stamp',
    configSchema: {},
  },
  {
    name: 'dkim_sign',
    displayName: 'DKIM Signing',
    description: 'Sign message with DKIM',
    type: 'transform',
    direction: 'outbound',
    unskippable: true,
    category: 'authentication',
    icon: 'Key',
    configSchema: {},
  },
]

// Helper functions
export function getFiltersByDirection(direction: 'inbound' | 'outbound'): FilterDefinition[] {
  return BUILTIN_FILTERS.filter((f) => f.direction === direction || f.direction === 'both')
}

export function getFiltersByType(type: 'action' | 'transform'): FilterDefinition[] {
  return BUILTIN_FILTERS.filter((f) => f.type === type)
}

export function getFiltersByCategory(category: FilterCategory): FilterDefinition[] {
  return BUILTIN_FILTERS.filter((f) => f.category === category)
}

export function getFilterDefinition(name: string): FilterDefinition | undefined {
  return BUILTIN_FILTERS.find((f) => f.name === name)
}

export function getFiltersByDirectionAndType(
  direction: 'inbound' | 'outbound',
  type: 'action' | 'transform'
): FilterDefinition[] {
  return BUILTIN_FILTERS.filter(
    (f) => (f.direction === direction || f.direction === 'both') && f.type === type
  )
}
