import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'
import { AppShell } from '../../components/layout/AppShell'
import { QuotaBreakdown } from '../../components/mailboxes/QuotaBreakdown'
import { apiV1 } from '../../lib/api'

export const Route = createFileRoute('/mailboxes/$id')({
  component: MailboxDetailsPage,
})

interface Domain {
  id: number
  name: string
}

interface Mailbox {
  id: number
  domain_id: number
  local_part: string
  address: string
  display_name: string | null
  domain: Domain
  quota_bytes: number
  quota_used_bytes: number
  active: boolean
  last_login_at: string | null
  created_at: string
  updated_at: string
  quota_usage?: {
    mailbox_id: number
    subject_bytes: number
    body_bytes: number
    attachment_bytes: number
    message_count: number
    updated_at: string
  }
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
}

function formatDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function QuotaChart({ used, total }: { used: number; total: number }) {
  const percentage = total > 0 ? (used / total) * 100 : 0
  const isNearLimit = percentage > 80
  const isFull = percentage > 95

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-end gap-6">
        {/* Circular Chart */}
        <div className="relative w-32 h-32">
          <svg className="w-32 h-32 transform -rotate-90">
            <circle
              cx="64"
              cy="64"
              r="56"
              stroke="var(--gray-border)"
              strokeWidth="12"
              fill="none"
            />
            <circle
              cx="64"
              cy="64"
              r="56"
              stroke={isFull ? '#EF4444' : isNearLimit ? '#F59E0B' : 'var(--success)'}
              strokeWidth="12"
              fill="none"
              strokeDasharray={`${(percentage / 100) * 351.86} 351.86`}
              className="transition-all duration-500"
            />
          </svg>
          <div className="absolute inset-0 flex items-center justify-center">
            <div className="text-center">
              <div className="text-2xl font-semibold" style={{ color: 'var(--black-soft)' }}>
                {percentage.toFixed(0)}%
              </div>
            </div>
          </div>
        </div>

        {/* Stats */}
        <div className="flex-1 grid grid-cols-2 gap-4">
          <div>
            <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
              Used
            </div>
            <div className="text-xl font-semibold" style={{ color: 'var(--black-soft)' }}>
              {formatBytes(used)}
            </div>
          </div>
          <div>
            <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
              Total
            </div>
            <div className="text-xl font-semibold" style={{ color: 'var(--black-soft)' }}>
              {formatBytes(total)}
            </div>
          </div>
          <div>
            <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
              Available
            </div>
            <div className="text-xl font-semibold" style={{ color: 'var(--success)' }}>
              {formatBytes(total - used)}
            </div>
          </div>
          <div>
            <div className="text-xs mb-1" style={{ color: 'var(--gray-secondary)' }}>
              Percentage
            </div>
            <div
              className="text-xl font-semibold"
              style={{
                color: isFull ? '#EF4444' : isNearLimit ? '#F59E0B' : 'var(--success)',
              }}
            >
              {percentage.toFixed(1)}%
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function MailboxDetailsPage() {
  const { id } = Route.useParams()
  const { accessToken } = useAuthStore()
  const { deleteMailbox } = useMailboxStore()
  const navigate = useNavigate()

  const [mailbox, setMailbox] = useState<Mailbox | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [isEditing, setIsEditing] = useState(false)
  const [formData, setFormData] = useState({
    display_name: '',
    quota_bytes: 0,
    enabled: true,
    password: '',
  })
  const [isSaving, setIsSaving] = useState(false)

  useEffect(() => {
    if (accessToken) {
      fetchMailbox()
    }
  }, [id, accessToken])

  const fetchMailbox = async () => {
    setIsLoading(true)
    setError(null)

    try {
      const response = await apiV1.request(`/admin/mailboxes/${id}`, { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch mailbox')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      setMailbox(data)
      setFormData({
        display_name: data.display_name || '',
        quota_bytes: data.quota_bytes,
        enabled: data.active,
        password: '',
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch mailbox')
    } finally {
      setIsLoading(false)
    }
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken) return

    setIsSaving(true)
    setError(null)

    try {
      const updateData: any = {
        display_name: formData.display_name || null,
        quota_bytes: formData.quota_bytes,
        active: formData.enabled,
      }

      if (formData.password) {
        updateData.password = formData.password
      }

      const response = await apiV1.request(
        `/admin/mailboxes/${id}`,
        {
          method: 'PUT',
          body: JSON.stringify(updateData),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update mailbox')
      }

      await fetchMailbox()
      setIsEditing(false)
      setFormData((prev) => ({ ...prev, password: '' }))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update mailbox')
    } finally {
      setIsSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!accessToken || !mailbox) return
    if (!confirm(`Are you sure you want to delete mailbox "${mailbox.address}"?`)) return

    try {
      await deleteMailbox(accessToken, mailbox.id)
      navigate({ to: '/mailboxes' })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete mailbox')
    }
  }

  if (isLoading) {
    return (
      <AppShell title="Mailbox Details">
        <div className="flex items-center justify-center py-20">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading mailbox...
          </div>
        </div>
      </AppShell>
    )
  }

  if (error && !mailbox) {
    return (
      <AppShell title="Mailbox Details">
        <div>
          <div
            className="p-4 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
          <div className="mt-6">
            <Link
              to="/mailboxes"
              className="text-sm hover:underline"
              style={{ color: 'var(--red-primary)' }}
            >
              Back to Mailboxes
            </Link>
          </div>
        </div>
      </AppShell>
    )
  }

  if (!mailbox) {
    return null
  }

  return (
    <AppShell title={mailbox.address}>
      <div>
        {/* Header */}
        <div className="mb-8">
          <Link
            to="/mailboxes"
            className="text-sm mb-4 inline-block hover:underline"
            style={{ color: 'var(--gray-secondary)' }}
          >
            ← Back to Mailboxes
          </Link>
          <div className="flex items-start justify-between">
            <div>
              <h1
                className="text-3xl font-semibold mb-2"
                style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
              >
                {mailbox.address}
              </h1>
              <div className="flex items-center gap-3">
                <span
                  className="inline-flex items-center px-2 py-1 text-xs font-medium"
                  style={{
                    backgroundColor: mailbox.active ? '#DCFCE7' : '#F3F4F6',
                    color: mailbox.active ? '#166534' : '#6B7280',
                  }}
                >
                  {mailbox.active ? 'Enabled' : 'Disabled'}
                </span>
                <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                  {mailbox.display_name || 'No display name'}
                </span>
              </div>
            </div>

            <div className="flex gap-2">
              <button
                onClick={() => setIsEditing(!isEditing)}
                className="h-11 px-6 flex items-center justify-center text-sm font-medium border"
                style={{
                  borderColor: 'var(--gray-border)',
                  color: 'var(--black-soft)',
                  fontFamily: 'Space Grotesk',
                }}
              >
                {isEditing ? 'Cancel' : 'Edit'}
              </button>
              <button
                onClick={handleDelete}
                className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium"
                style={{
                  backgroundColor: '#DC2626',
                  fontFamily: 'Space Grotesk',
                }}
              >
                Delete
              </button>
            </div>
          </div>
        </div>

        {/* Error Message */}
        {error && (
          <div
            className="p-4 mb-6 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
        )}

        {/* Content */}
        {isEditing ? (
          <form onSubmit={handleSave} className="space-y-6">
            <div
              className="p-6 border"
              style={{
                borderColor: 'var(--gray-border)',
                backgroundColor: 'var(--bg-surface)',
              }}
            >
              <h2
                className="text-lg font-semibold mb-6"
                style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
              >
                Edit Mailbox
              </h2>

              <div className="space-y-5">
                {/* Display Name */}
                <div className="flex flex-col gap-2">
                  <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Display Name
                  </label>
                  <div
                    className="h-11 px-4 flex items-center border"
                    style={{ borderColor: 'var(--gray-border)', backgroundColor: 'white' }}
                  >
                    <input
                      type="text"
                      value={formData.display_name}
                      onChange={(e) =>
                        setFormData((prev) => ({ ...prev, display_name: e.target.value }))
                      }
                      placeholder="John Doe"
                      className="w-full outline-none text-sm"
                      style={{ color: 'var(--black-soft)' }}
                    />
                  </div>
                </div>

                {/* Quota */}
                <div className="flex flex-col gap-2">
                  <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Quota (bytes)
                  </label>
                  <div
                    className="h-11 px-4 flex items-center border"
                    style={{ borderColor: 'var(--gray-border)', backgroundColor: 'white' }}
                  >
                    <input
                      type="number"
                      value={formData.quota_bytes}
                      onChange={(e) =>
                        setFormData((prev) => ({ ...prev, quota_bytes: parseInt(e.target.value) }))
                      }
                      min="0"
                      required
                      className="w-full outline-none text-sm"
                      style={{ color: 'var(--black-soft)' }}
                    />
                  </div>
                  <div className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                    Current: {formatBytes(formData.quota_bytes)}
                  </div>
                </div>

                {/* Password */}
                <div className="flex flex-col gap-2">
                  <label className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    New Password
                  </label>
                  <div
                    className="h-11 px-4 flex items-center border"
                    style={{ borderColor: 'var(--gray-border)', backgroundColor: 'white' }}
                  >
                    <input
                      type="password"
                      value={formData.password}
                      onChange={(e) =>
                        setFormData((prev) => ({ ...prev, password: e.target.value }))
                      }
                      placeholder="Leave blank to keep current"
                      className="w-full outline-none text-sm"
                      style={{ color: 'var(--black-soft)' }}
                    />
                  </div>
                  <div className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                    Leave blank to keep the current password
                  </div>
                </div>

                {/* Enabled */}
                <div className="flex items-center gap-3">
                  <input
                    type="checkbox"
                    id="enabled"
                    checked={formData.enabled}
                    onChange={(e) =>
                      setFormData((prev) => ({ ...prev, enabled: e.target.checked }))
                    }
                    className="w-4 h-4"
                  />
                  <label htmlFor="enabled" className="text-sm" style={{ color: 'var(--black-soft)' }}>
                    Enable mailbox
                  </label>
                </div>
              </div>

              <div className="mt-6 flex gap-3">
                <button
                  type="submit"
                  disabled={isSaving}
                  className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium"
                  style={{
                    backgroundColor: 'var(--red-primary)',
                    fontFamily: 'Space Grotesk',
                    opacity: isSaving ? 0.6 : 1,
                    cursor: isSaving ? 'not-allowed' : 'pointer',
                  }}
                >
                  {isSaving ? 'Saving...' : 'Save Changes'}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setIsEditing(false)
                    setFormData({
                      display_name: mailbox.display_name || '',
                      quota_bytes: mailbox.quota_bytes,
                      enabled: mailbox.active,
                      password: '',
                    })
                  }}
                  className="h-11 px-6 flex items-center justify-center text-sm font-medium border"
                  style={{
                    borderColor: 'var(--gray-border)',
                    color: 'var(--black-soft)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  Cancel
                </button>
              </div>
            </div>
          </form>
        ) : (
          <div className="space-y-6">
            {/* Quota Usage */}
            <div
              className="p-6 border"
              style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)' }}
            >
              <h2
                className="text-lg font-semibold mb-6"
                style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
              >
                Quota Usage
              </h2>
              <QuotaChart used={mailbox.quota_used_bytes} total={mailbox.quota_bytes} />
            </div>

            {/* Quota Breakdown */}
            {mailbox.quota_usage && (
              <div
                className="p-6 border"
                style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)' }}
              >
                <h2
                  className="text-lg font-semibold mb-6"
                  style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
                >
                  Storage Breakdown
                </h2>
                <QuotaBreakdown
                  quotaUsage={mailbox.quota_usage}
                  quotaBytes={mailbox.quota_bytes}
                />
              </div>
            )}

            {/* Mailbox Details */}
            <div
              className="p-6 border"
              style={{ borderColor: 'var(--gray-border)', backgroundColor: 'var(--bg-surface)' }}
            >
              <h2
                className="text-lg font-semibold mb-6"
                style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
              >
                Details
              </h2>
              <div className="space-y-4">
                <div className="grid grid-cols-3 gap-4">
                  <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    Email
                  </div>
                  <div className="col-span-2 text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    {mailbox.address}
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-4">
                  <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    Domain
                  </div>
                  <div className="col-span-2 text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    {mailbox.domain.name}
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-4">
                  <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    Display Name
                  </div>
                  <div className="col-span-2 text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    {mailbox.display_name || '—'}
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-4">
                  <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    Created
                  </div>
                  <div className="col-span-2 text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    {formatDate(mailbox.created_at)}
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-4">
                  <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    Last Updated
                  </div>
                  <div className="col-span-2 text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    {formatDate(mailbox.updated_at)}
                  </div>
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
    </AppShell>
  )
}
