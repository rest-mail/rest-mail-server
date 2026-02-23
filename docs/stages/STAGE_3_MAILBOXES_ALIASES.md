# Stage 3: Mailbox & Alias Management - Detailed Implementation Plan

**Status:** 🟡 IN PROGRESS - Mailbox UI complete, alias management not started
**Priority:** HIGH
**Estimated Effort:** 3-4 days (1 day quota viz, 2-3 days alias management)

---

## Overview

Complete the mailbox management feature with quota visualization and implement full alias management from scratch. Both features will integrate with existing backend APIs.

**Current State:**
- ✅ Mailbox backend API complete (`/api/v1/admin/mailboxes`)
- ✅ Mailbox frontend UI complete (list, create, edit, delete)
- ✅ Mailbox Zustand store implemented
- ❌ Quota visualization missing (only basic progress bar exists)
- ❌ Alias management completely missing (backend exists, no frontend)
- ❌ Bulk operations not implemented
- ❌ Password strength indicator missing

---

## Part 1: Quota Visualization Component

### 1.1 Create QuotaBreakdown Component

**File:** `admin/src/components/mailboxes/QuotaBreakdown.tsx` (new file)

A detailed quota visualization showing storage breakdown by category.

```typescript
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
```

### 1.2 Update Backend to Return Quota Usage

**File:** `internal/api/handlers/mailboxes.go`

Update the `List` and `Get` handlers to preload quota usage data:

```go
// In List handler, add Preload for QuotaUsage:
query := h.db.Preload("Domain").Preload("QuotaUsage")

// In Get handler, add Preload for QuotaUsage:
if err := h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id).Error; err != nil {
```

### 1.3 Update Mailbox Store Type Definition

**File:** `admin/src/lib/stores/mailboxStore.ts`

Update the `Mailbox` interface to include quota usage:

```typescript
interface Mailbox {
  id: string
  email: string
  display_name: string | null
  domain: string
  quota_bytes: number
  used_bytes: number  // This should now come from quota_usage
  enabled: boolean
  created_at: string
  updated_at: string
  quota_usage?: {
    subject_bytes: number
    body_bytes: number
    attachment_bytes: number
    message_count: number
  }
}
```

### 1.4 Integrate QuotaBreakdown into Mailbox Detail Page

**File:** `admin/src/routes/mailboxes/$id.tsx`

Add quota visualization to the mailbox detail page:

```typescript
// Import the component
import { QuotaBreakdown } from '../../components/mailboxes/QuotaBreakdown'

// In the component render, add a section for quota:
<div className="bg-white border" style={{ borderColor: 'var(--gray-border)' }}>
  <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
    <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
      Quota & Storage
    </h2>
  </div>
  <div className="p-6">
    {mailbox.quota_usage && (
      <QuotaBreakdown
        quotaUsage={mailbox.quota_usage}
        quotaBytes={mailbox.quota_bytes}
      />
    )}
  </div>
</div>
```

---

## Part 2: Alias Management (Complete Feature)

### 2.1 Create Alias Store

**File:** `admin/src/lib/stores/aliasStore.ts` (new file)

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface Alias {
  id: number
  source_address: string
  destination_address: string
  domain_id: number
  active: boolean
  created_at: string
  domain?: {
    id: number
    name: string
  }
}

interface AliasState {
  aliases: Alias[]
  isLoading: boolean
  error: string | null
  selectedDomain: string | null

  // Actions
  fetchAliases: (token: string, domainId?: string) => Promise<void>
  createAlias: (
    token: string,
    data: {
      source_address: string
      destination_address: string
    }
  ) => Promise<void>
  updateAlias: (
    token: string,
    id: number,
    data: {
      destination_address?: string
      active?: boolean
    }
  ) => Promise<void>
  deleteAlias: (token: string, id: number) => Promise<void>
  setSelectedDomain: (domain: string | null) => void
  clearError: () => void
}

export const useAliasStore = create<AliasState>((set, get) => ({
  aliases: [],
  isLoading: false,
  error: null,
  selectedDomain: null,

  fetchAliases: async (token: string, domainId?: string) => {
    set({ isLoading: true, error: null })

    try {
      const url = domainId
        ? `/admin/aliases?domain_id=${domainId}`
        : '/admin/aliases'

      const response = await apiV1.request(url, { method: 'GET' }, token)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch aliases')
      }

      const data = await response.json()
      set({ aliases: data.data || [], isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch aliases',
        isLoading: false,
      })
      throw error
    }
  },

  createAlias: async (token: string, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/aliases',
        {
          method: 'POST',
          body: JSON.stringify(data),
        },
        token
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create alias')
      }

      // Refresh the list
      await get().fetchAliases(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create alias',
        isLoading: false,
      })
      throw error
    }
  },

  updateAlias: async (token: string, id: number, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/aliases/${id}`,
        {
          method: 'PATCH',
          body: JSON.stringify(data),
        },
        token
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update alias')
      }

      // Refresh the list
      await get().fetchAliases(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update alias',
        isLoading: false,
      })
      throw error
    }
  },

  deleteAlias: async (token: string, id: number) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/aliases/${id}`, { method: 'DELETE' }, token)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete alias')
      }

      // Refresh the list
      await get().fetchAliases(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete alias',
        isLoading: false,
      })
      throw error
    }
  },

  setSelectedDomain: (domain) => {
    set({ selectedDomain: domain })
  },

  clearError: () => {
    set({ error: null })
  },
}))
```

### 2.2 Create Alias List Page

**File:** `admin/src/routes/aliases/index.tsx` (new file)

```typescript
import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState, useMemo } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/')({
  component: AliasesPage,
})

function AliasesPage() {
  const { accessToken } = useAuthStore()
  const { aliases, isLoading, error, fetchAliases, selectedDomain, setSelectedDomain, deleteAlias } =
    useAliasStore()
  const [searchQuery, setSearchQuery] = useState('')
  const [deletingId, setDeletingId] = useState<number | null>(null)

  useEffect(() => {
    if (accessToken) {
      fetchAliases(accessToken)
    }
  }, [accessToken, fetchAliases])

  // Extract unique domains
  const domains = useMemo(() => {
    const uniqueDomains = Array.from(
      new Set(aliases.map((a) => a.domain?.name).filter(Boolean))
    ) as string[]
    return uniqueDomains.sort()
  }, [aliases])

  // Filter aliases
  const filteredAliases = useMemo(() => {
    return aliases.filter((alias) => {
      const matchesSearch =
        searchQuery === '' ||
        alias.source_address.toLowerCase().includes(searchQuery.toLowerCase()) ||
        alias.destination_address.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesDomain = !selectedDomain || alias.domain?.name === selectedDomain

      return matchesSearch && matchesDomain
    })
  }, [aliases, searchQuery, selectedDomain])

  const handleDelete = async (id: number, sourceAddress: string) => {
    if (!accessToken) return
    if (!confirm(`Are you sure you want to delete alias "${sourceAddress}"?`)) return

    setDeletingId(id)
    try {
      await deleteAlias(accessToken, id)
    } catch (err) {
      console.error('Failed to delete alias:', err)
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <AppShell title="Aliases">
      <div>
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Manage email forwarding and aliases
            </p>
          </div>

          <Link
            to="/aliases/new"
            className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Create Alias
          </Link>
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

        {/* Filters */}
        <div className="flex gap-4 mb-6">
          {/* Search */}
          <div className="flex-1">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search aliases..."
                className="w-full outline-none text-sm"
                style={{ color: 'var(--black-soft)' }}
              />
            </div>
          </div>

          {/* Domain Filter */}
          <div className="w-64">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <select
                value={selectedDomain || ''}
                onChange={(e) => setSelectedDomain(e.target.value || null)}
                className="w-full outline-none text-sm bg-transparent"
                style={{ color: 'var(--black-soft)' }}
              >
                <option value="">All Domains</option>
                {domains.map((domain) => (
                  <option key={domain} value={domain}>
                    {domain}
                  </option>
                ))}
              </select>
            </div>
          </div>
        </div>

        {/* Table */}
        {isLoading ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading aliases...
            </div>
          </div>
        ) : filteredAliases.length === 0 ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {searchQuery || selectedDomain ? 'No aliases found' : 'No aliases yet'}
            </div>
          </div>
        ) : (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead>
                <tr
                  className="border-b text-left text-xs font-medium"
                  style={{
                    backgroundColor: 'var(--bg-surface)',
                    borderColor: 'var(--gray-border)',
                    color: 'var(--gray-secondary)',
                  }}
                >
                  <th className="px-6 py-4">Source Address</th>
                  <th className="px-6 py-4">Destination Address</th>
                  <th className="px-6 py-4">Domain</th>
                  <th className="px-6 py-4">Status</th>
                  <th className="px-6 py-4 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {filteredAliases.map((alias) => (
                  <tr
                    key={alias.id}
                    className="border-b transition-colors hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="px-6 py-4">
                      <Link
                        to="/aliases/$id"
                        params={{ id: alias.id.toString() }}
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--black-soft)' }}
                      >
                        {alias.source_address}
                      </Link>
                    </td>
                    <td className="px-6 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {alias.destination_address}
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {alias.domain?.name || '—'}
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <span
                        className="inline-flex items-center px-2 py-1 text-xs font-medium"
                        style={{
                          backgroundColor: alias.active ? '#DCFCE7' : '#F3F4F6',
                          color: alias.active ? '#166534' : '#6B7280',
                        }}
                      >
                        {alias.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="px-6 py-4 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link
                          to="/aliases/$id"
                          params={{ id: alias.id.toString() }}
                          className="text-sm hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          Edit
                        </Link>
                        <button
                          onClick={() => handleDelete(alias.id, alias.source_address)}
                          disabled={deletingId === alias.id}
                          className="text-sm hover:underline disabled:opacity-50"
                          style={{ color: '#DC2626' }}
                        >
                          {deletingId === alias.id ? 'Deleting...' : 'Delete'}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Stats */}
        <div className="mt-6 flex items-center justify-between">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Showing {filteredAliases.length} of {aliases.length} aliases
          </div>
        </div>
      </div>
    </AppShell>
  )
}
```

### 2.3 Create Alias Create Page

**File:** `admin/src/routes/aliases/new.tsx` (new file)

```typescript
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/new')({
  component: CreateAliasPage,
})

function CreateAliasPage() {
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { createAlias } = useAliasStore()

  const [formData, setFormData] = useState({
    source_address: '',
    destination_address: '',
  })
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isSubmitting, setIsSubmitting] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken) return

    setErrors({})
    setIsSubmitting(true)

    try {
      await createAlias(accessToken, formData)
      navigate({ to: '/aliases' })
    } catch (err) {
      console.error('Failed to create alias:', err)
      setErrors({ submit: err instanceof Error ? err.message : 'Failed to create alias' })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <AppShell title="Create Alias">
      <div className="max-w-2xl">
        <div className="mb-6">
          <button
            onClick={() => navigate({ to: '/aliases' })}
            className="text-sm hover:underline"
            style={{ color: 'var(--gray-secondary)' }}
          >
            ← Back to aliases
          </button>
        </div>

        <div className="bg-white border" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
            <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
              Alias Details
            </h2>
          </div>

          <form onSubmit={handleSubmit} className="p-6">
            {/* Error Message */}
            {errors.submit && (
              <div
                className="p-4 mb-6 border text-sm"
                style={{
                  borderColor: '#EF4444',
                  backgroundColor: '#FEF2F2',
                  color: '#DC2626',
                }}
              >
                {errors.submit}
              </div>
            )}

            <div className="space-y-6">
              {/* Source Address */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Source Address
                </label>
                <input
                  type="email"
                  value={formData.source_address}
                  onChange={(e) => setFormData({ ...formData, source_address: e.target.value })}
                  placeholder="alias@example.com"
                  className="w-full h-11 px-4 border text-sm outline-none"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  required
                />
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  The email address that will receive forwarded mail
                </p>
              </div>

              {/* Destination Address */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Destination Address
                </label>
                <input
                  type="email"
                  value={formData.destination_address}
                  onChange={(e) => setFormData({ ...formData, destination_address: e.target.value })}
                  placeholder="user@example.com"
                  className="w-full h-11 px-4 border text-sm outline-none"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  required
                />
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Where mail sent to the source address will be forwarded
                </p>
              </div>

              {/* Actions */}
              <div className="flex gap-3 pt-4">
                <button
                  type="submit"
                  disabled={isSubmitting}
                  className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
                  style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
                >
                  {isSubmitting ? 'Creating...' : 'Create Alias'}
                </button>
                <button
                  type="button"
                  onClick={() => navigate({ to: '/aliases' })}
                  className="h-11 px-6 flex items-center justify-center border text-sm font-medium rounded"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                >
                  Cancel
                </button>
              </div>
            </div>
          </form>
        </div>
      </div>
    </AppShell>
  )
}
```

### 2.4 Create Alias Edit Page

**File:** `admin/src/routes/aliases/$id.tsx` (new file)

```typescript
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/$id')({
  component: EditAliasPage,
})

function EditAliasPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { aliases, fetchAliases, updateAlias } = useAliasStore()

  const alias = aliases.find((a) => a.id.toString() === id)

  const [formData, setFormData] = useState({
    destination_address: '',
    active: true,
  })
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isSubmitting, setIsSubmitting] = useState(false)

  useEffect(() => {
    if (accessToken && !alias) {
      fetchAliases(accessToken)
    }
  }, [accessToken, alias, fetchAliases])

  useEffect(() => {
    if (alias) {
      setFormData({
        destination_address: alias.destination_address,
        active: alias.active,
      })
    }
  }, [alias])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken || !alias) return

    setErrors({})
    setIsSubmitting(true)

    try {
      await updateAlias(accessToken, alias.id, formData)
      navigate({ to: '/aliases' })
    } catch (err) {
      console.error('Failed to update alias:', err)
      setErrors({ submit: err instanceof Error ? err.message : 'Failed to update alias' })
    } finally {
      setIsSubmitting(false)
    }
  }

  if (!alias) {
    return (
      <AppShell title="Edit Alias">
        <div className="flex items-center justify-center py-20">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading alias...
          </div>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title={`Edit Alias: ${alias.source_address}`}>
      <div className="max-w-2xl">
        <div className="mb-6">
          <button
            onClick={() => navigate({ to: '/aliases' })}
            className="text-sm hover:underline"
            style={{ color: 'var(--gray-secondary)' }}
          >
            ← Back to aliases
          </button>
        </div>

        <div className="bg-white border" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
            <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
              Alias Details
            </h2>
          </div>

          <form onSubmit={handleSubmit} className="p-6">
            {/* Error Message */}
            {errors.submit && (
              <div
                className="p-4 mb-6 border text-sm"
                style={{
                  borderColor: '#EF4444',
                  backgroundColor: '#FEF2F2',
                  color: '#DC2626',
                }}
              >
                {errors.submit}
              </div>
            )}

            <div className="space-y-6">
              {/* Source Address (Read-only) */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Source Address
                </label>
                <div
                  className="w-full h-11 px-4 flex items-center border text-sm"
                  style={{ borderColor: 'var(--gray-border)', backgroundColor: '#F9FAFB', color: 'var(--gray-secondary)' }}
                >
                  {alias.source_address}
                </div>
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Source address cannot be changed
                </p>
              </div>

              {/* Destination Address */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Destination Address
                </label>
                <input
                  type="email"
                  value={formData.destination_address}
                  onChange={(e) => setFormData({ ...formData, destination_address: e.target.value })}
                  placeholder="user@example.com"
                  className="w-full h-11 px-4 border text-sm outline-none"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  required
                />
              </div>

              {/* Active Status */}
              <div>
                <label className="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={formData.active}
                    onChange={(e) => setFormData({ ...formData, active: e.target.checked })}
                    className="w-4 h-4"
                    style={{ accentColor: 'var(--red-primary)' }}
                  />
                  <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Active
                  </span>
                </label>
                <p className="mt-1 ml-7 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Inactive aliases will not forward mail
                </p>
              </div>

              {/* Metadata */}
              <div className="pt-4 border-t" style={{ borderColor: 'var(--gray-border)' }}>
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <span style={{ color: 'var(--gray-secondary)' }}>Domain:</span>
                    <span className="ml-2 font-medium" style={{ color: 'var(--black-soft)' }}>
                      {alias.domain?.name || '—'}
                    </span>
                  </div>
                  <div>
                    <span style={{ color: 'var(--gray-secondary)' }}>Created:</span>
                    <span className="ml-2 font-medium" style={{ color: 'var(--black-soft)' }}>
                      {new Date(alias.created_at).toLocaleDateString()}
                    </span>
                  </div>
                </div>
              </div>

              {/* Actions */}
              <div className="flex gap-3 pt-4">
                <button
                  type="submit"
                  disabled={isSubmitting}
                  className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
                  style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
                >
                  {isSubmitting ? 'Saving...' : 'Save Changes'}
                </button>
                <button
                  type="button"
                  onClick={() => navigate({ to: '/aliases' })}
                  className="h-11 px-6 flex items-center justify-center border text-sm font-medium rounded"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                >
                  Cancel
                </button>
              </div>
            </div>
          </form>
        </div>
      </div>
    </AppShell>
  )
}
```

### 2.5 Add Alias Routes to Router

**File:** `admin/src/main.tsx` or router configuration

Ensure these routes are registered:
- `/aliases` → List page
- `/aliases/new` → Create page
- `/aliases/$id` → Edit page

### 2.6 Add Navigation Link

**File:** `admin/src/components/layout/AppShell.tsx` (or sidebar component)

Add alias link to navigation:

```typescript
<Link to="/aliases" className="nav-link">
  Aliases
</Link>
```

---

## Part 3: Bulk Operations for Mailboxes

### 3.1 CSV Import Component

**File:** `admin/src/components/mailboxes/BulkImport.tsx` (new file)

```typescript
import { useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'

interface ImportRow {
  address: string
  password: string
  display_name?: string
  quota_bytes?: number
}

export function BulkImport({ onClose }: { onClose: () => void }) {
  const { accessToken } = useAuthStore()
  const { createMailbox } = useMailboxStore()
  const [file, setFile] = useState<File | null>(null)
  const [isProcessing, setIsProcessing] = useState(false)
  const [results, setResults] = useState<{ success: number; failed: number; errors: string[] }>({
    success: 0,
    failed: 0,
    errors: [],
  })

  const parseCSV = (text: string): ImportRow[] => {
    const lines = text.split('\n').filter((line) => line.trim())
    const headers = lines[0].split(',').map((h) => h.trim())

    return lines.slice(1).map((line) => {
      const values = line.split(',').map((v) => v.trim())
      const row: any = {}
      headers.forEach((header, idx) => {
        row[header] = values[idx]
      })
      return row as ImportRow
    })
  }

  const handleImport = async () => {
    if (!file || !accessToken) return

    setIsProcessing(true)
    const reader = new FileReader()

    reader.onload = async (e) => {
      try {
        const text = e.target?.result as string
        const rows = parseCSV(text)

        let success = 0
        let failed = 0
        const errors: string[] = []

        for (const row of rows) {
          try {
            await createMailbox(accessToken, {
              email: row.address,
              password: row.password,
              display_name: row.display_name,
              quota_bytes: row.quota_bytes ? parseInt(row.quota_bytes.toString()) : undefined,
            })
            success++
          } catch (err) {
            failed++
            errors.push(`${row.address}: ${err instanceof Error ? err.message : 'Unknown error'}`)
          }
        }

        setResults({ success, failed, errors })
      } catch (err) {
        console.error('Failed to parse CSV:', err)
      } finally {
        setIsProcessing(false)
      }
    }

    reader.readAsText(file)
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg max-w-2xl w-full mx-4">
        <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
          <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
            Bulk Import Mailboxes
          </h2>
        </div>

        <div className="p-6">
          {results.success > 0 || results.failed > 0 ? (
            <div className="space-y-4">
              <div className="text-sm">
                <div className="flex items-center justify-between mb-2">
                  <span style={{ color: 'var(--gray-secondary)' }}>Successful:</span>
                  <span className="font-medium" style={{ color: '#10B981' }}>
                    {results.success}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span style={{ color: 'var(--gray-secondary)' }}>Failed:</span>
                  <span className="font-medium" style={{ color: '#EF4444' }}>
                    {results.failed}
                  </span>
                </div>
              </div>

              {results.errors.length > 0 && (
                <div className="mt-4 max-h-64 overflow-y-auto">
                  <div className="text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                    Errors:
                  </div>
                  <ul className="space-y-1 text-xs" style={{ color: '#DC2626' }}>
                    {results.errors.map((error, idx) => (
                      <li key={idx}>{error}</li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          ) : (
            <div className="space-y-4">
              <div>
                <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
                  Upload a CSV file with the following columns:
                </p>
                <div className="bg-gray-50 p-4 rounded text-xs font-mono">
                  address,password,display_name,quota_bytes
                </div>
              </div>

              <div>
                <input
                  type="file"
                  accept=".csv"
                  onChange={(e) => setFile(e.target.files?.[0] || null)}
                  className="w-full text-sm"
                />
              </div>
            </div>
          )}
        </div>

        <div className="px-6 py-4 border-t flex gap-3" style={{ borderColor: 'var(--gray-border)' }}>
          {results.success > 0 || results.failed > 0 ? (
            <button
              onClick={onClose}
              className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
              style={{ backgroundColor: 'var(--red-primary)' }}
            >
              Close
            </button>
          ) : (
            <>
              <button
                onClick={handleImport}
                disabled={!file || isProcessing}
                className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
                style={{ backgroundColor: 'var(--red-primary)' }}
              >
                {isProcessing ? 'Importing...' : 'Import'}
              </button>
              <button
                onClick={onClose}
                className="h-11 px-6 flex items-center justify-center border text-sm font-medium rounded"
                style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
              >
                Cancel
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
```

### 3.2 Bulk Quota Update Component

**File:** `admin/src/components/mailboxes/BulkQuotaUpdate.tsx` (new file)

```typescript
import { useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'

interface BulkQuotaUpdateProps {
  selectedIds: string[]
  onClose: () => void
}

export function BulkQuotaUpdate({ selectedIds, onClose }: BulkQuotaUpdateProps) {
  const { accessToken } = useAuthStore()
  const { updateMailbox } = useMailboxStore()
  const [quotaBytes, setQuotaBytes] = useState<number>(1073741824) // 1GB default
  const [isProcessing, setIsProcessing] = useState(false)

  const handleUpdate = async () => {
    if (!accessToken) return

    setIsProcessing(true)

    for (const id of selectedIds) {
      try {
        await updateMailbox(accessToken, id, { quota_bytes: quotaBytes })
      } catch (err) {
        console.error(`Failed to update mailbox ${id}:`, err)
      }
    }

    setIsProcessing(false)
    onClose()
  }

  const formatBytes = (bytes: number): string => {
    const gb = bytes / 1073741824
    return `${gb.toFixed(2)} GB`
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg max-w-md w-full mx-4">
        <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
          <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
            Bulk Update Quota
          </h2>
        </div>

        <div className="p-6">
          <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
            Update quota for {selectedIds.length} selected mailbox{selectedIds.length !== 1 ? 'es' : ''}
          </p>

          <div>
            <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
              New Quota Size
            </label>
            <input
              type="number"
              value={quotaBytes}
              onChange={(e) => setQuotaBytes(parseInt(e.target.value))}
              className="w-full h-11 px-4 border text-sm outline-none"
              style={{ borderColor: 'var(--gray-border)' }}
            />
            <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
              {formatBytes(quotaBytes)}
            </p>
          </div>

          <div className="mt-4 grid grid-cols-4 gap-2">
            {[
              { label: '500MB', bytes: 524288000 },
              { label: '1GB', bytes: 1073741824 },
              { label: '5GB', bytes: 5368709120 },
              { label: '10GB', bytes: 10737418240 },
            ].map((preset) => (
              <button
                key={preset.label}
                onClick={() => setQuotaBytes(preset.bytes)}
                className="h-9 px-3 text-xs border rounded hover:bg-gray-50"
                style={{ borderColor: 'var(--gray-border)' }}
              >
                {preset.label}
              </button>
            ))}
          </div>
        </div>

        <div className="px-6 py-4 border-t flex gap-3" style={{ borderColor: 'var(--gray-border)' }}>
          <button
            onClick={handleUpdate}
            disabled={isProcessing}
            className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
            style={{ backgroundColor: 'var(--red-primary)' }}
          >
            {isProcessing ? 'Updating...' : 'Update Quota'}
          </button>
          <button
            onClick={onClose}
            className="h-11 px-6 flex items-center justify-center border text-sm font-medium rounded"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}
```

---

## Part 4: Password Strength Indicator

### 4.1 Create Password Strength Component

**File:** `admin/src/components/common/PasswordStrength.tsx` (new file)

```typescript
import { useMemo } from 'react'

interface PasswordStrengthProps {
  password: string
}

export function PasswordStrength({ password }: PasswordStrengthProps) {
  const strength = useMemo(() => {
    if (!password) return { score: 0, label: '', color: '' }

    let score = 0

    // Length
    if (password.length >= 8) score++
    if (password.length >= 12) score++
    if (password.length >= 16) score++

    // Character variety
    if (/[a-z]/.test(password)) score++
    if (/[A-Z]/.test(password)) score++
    if (/[0-9]/.test(password)) score++
    if (/[^a-zA-Z0-9]/.test(password)) score++

    // Determine strength
    if (score <= 2) return { score, label: 'Weak', color: '#EF4444' }
    if (score <= 4) return { score, label: 'Fair', color: '#F59E0B' }
    if (score <= 5) return { score, label: 'Good', color: '#3B82F6' }
    return { score, label: 'Strong', color: '#10B981' }
  }, [password])

  if (!password) return null

  return (
    <div className="mt-2">
      <div className="flex items-center gap-2 mb-1">
        <div className="flex-1 h-1.5 bg-gray-100 rounded-full overflow-hidden">
          <div
            className="h-full transition-all duration-300"
            style={{
              width: `${(strength.score / 7) * 100}%`,
              backgroundColor: strength.color,
            }}
          />
        </div>
        <span className="text-xs font-medium" style={{ color: strength.color }}>
          {strength.label}
        </span>
      </div>
      <div className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
        Use at least 8 characters with uppercase, lowercase, numbers, and symbols
      </div>
    </div>
  )
}
```

### 4.2 Integrate into Mailbox Forms

**File:** `admin/src/routes/mailboxes/new.tsx` (update)

```typescript
import { PasswordStrength } from '../../components/common/PasswordStrength'

// In the password input section:
<div>
  <label className="block text-sm font-medium mb-2">Password</label>
  <input
    type="password"
    value={formData.password}
    onChange={(e) => setFormData({ ...formData, password: e.target.value })}
    className="w-full h-11 px-4 border text-sm outline-none"
    style={{ borderColor: 'var(--gray-border)' }}
    required
  />
  <PasswordStrength password={formData.password} />
</div>
```

Do the same for the edit page when changing passwords.

---

## Testing Checklist

### Quota Visualization Tests:
- [ ] Backend returns quota_usage with mailbox data
- [ ] QuotaBreakdown component displays correctly
- [ ] Breakdown chart shows correct percentages
- [ ] Colors change based on usage levels (green, amber, red)
- [ ] Formatted byte values are human-readable
- [ ] Message count displays correctly
- [ ] Empty state (0 usage) renders without errors

### Alias Management Tests:
- [ ] Alias store fetches aliases from API
- [ ] Alias list displays all aliases with correct data
- [ ] Domain filter works correctly
- [ ] Search filter works for source and destination
- [ ] Create alias form validates input
- [ ] Create alias submits successfully
- [ ] Edit alias loads existing data
- [ ] Edit alias updates destination and status
- [ ] Delete alias shows confirmation and removes record
- [ ] Navigation links work correctly
- [ ] Error messages display for failed operations

### Bulk Operations Tests:
- [ ] CSV import parses file correctly
- [ ] CSV import creates mailboxes in batch
- [ ] CSV import shows success/failure counts
- [ ] CSV import displays error details
- [ ] Bulk quota update applies to selected mailboxes
- [ ] Bulk quota update shows progress
- [ ] Preset quota buttons work
- [ ] Custom quota input accepts valid values

### Password Strength Tests:
- [ ] Password strength indicator shows for all inputs
- [ ] Weak passwords show red indicator
- [ ] Strong passwords show green indicator
- [ ] Strength score updates in real-time
- [ ] Tooltip/help text displays requirements
- [ ] Component doesn't break with empty input

### Integration Tests:
- [ ] Mailbox detail page shows quota breakdown
- [ ] Alias routes are accessible from navigation
- [ ] Bulk operations integrate with mailbox list
- [ ] Password strength works in create/edit forms
- [ ] All API calls use correct endpoints
- [ ] Error handling displays user-friendly messages
- [ ] Loading states show during async operations

---

## Success Criteria

1. ✅ Quota visualization component displays storage breakdown
2. ✅ Quota chart shows attachments, bodies, and metadata separately
3. ✅ Alias management fully functional (CRUD operations)
4. ✅ Alias filtering by domain and search works
5. ✅ CSV import creates mailboxes in bulk
6. ✅ Bulk quota update applies to selected mailboxes
7. ✅ Password strength indicator shows real-time feedback
8. ✅ All features integrate seamlessly with existing UI
9. ✅ Error handling with user-friendly messages
10. ✅ Responsive design works on all screen sizes

---

## API Endpoints Reference

### Mailboxes (Already Implemented)
- `GET /api/v1/admin/mailboxes` - List mailboxes
- `POST /api/v1/admin/mailboxes` - Create mailbox
- `GET /api/v1/admin/mailboxes/{id}` - Get mailbox
- `PATCH /api/v1/admin/mailboxes/{id}` - Update mailbox
- `DELETE /api/v1/admin/mailboxes/{id}` - Delete mailbox

### Aliases (Already Implemented)
- `GET /api/v1/admin/aliases` - List aliases
- `POST /api/v1/admin/aliases` - Create alias
- `GET /api/v1/admin/aliases/{id}` - Get alias
- `PATCH /api/v1/admin/aliases/{id}` - Update alias
- `DELETE /api/v1/admin/aliases/{id}` - Delete alias

---

## File Structure Summary

```
admin/src/
├── components/
│   ├── common/
│   │   └── PasswordStrength.tsx         (new)
│   └── mailboxes/
│       ├── QuotaBreakdown.tsx           (new)
│       ├── BulkImport.tsx               (new)
│       └── BulkQuotaUpdate.tsx          (new)
├── lib/
│   └── stores/
│       ├── mailboxStore.ts              (update)
│       └── aliasStore.ts                (new)
└── routes/
    ├── mailboxes/
    │   ├── index.tsx                    (update - add bulk ops)
    │   ├── new.tsx                      (update - add password strength)
    │   └── $id.tsx                      (update - add quota viz)
    └── aliases/
        ├── index.tsx                    (new)
        ├── new.tsx                      (new)
        └── $id.tsx                      (new)

internal/api/handlers/
└── mailboxes.go                         (update - preload quota_usage)
```

---

## Next Steps After Completion

1. Add batch alias import (similar to mailbox CSV import)
2. Implement quota alerts/warnings system
3. Add alias wildcard support (e.g., `*@domain.com`)
4. Create mailbox usage reports
5. Add domain-level quota management
6. Implement alias groups (one source to multiple destinations)
