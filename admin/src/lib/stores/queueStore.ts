import { create } from 'zustand'
import { apiV1 } from '../api'

export type QueueStatus = 'pending' | 'deferred' | 'bounced'

interface QueueEntry {
  id: string
  recipient: string
  sender: string
  subject: string
  status: QueueStatus
  attempts: number
  next_attempt_at: string | null
  error_message: string | null
  created_at: string
  updated_at: string
  raw_message?: string
}

interface QueueState {
  entries: QueueEntry[]
  currentEntry: QueueEntry | null
  isLoading: boolean
  error: string | null
  filter: QueueStatus | 'all'
  selectedIds: string[]

  // Actions
  fetchQueue: (filter?: QueueStatus | 'all') => Promise<void>
  fetchEntry: (id: string) => Promise<void>
  retryEntry: (id: string) => Promise<void>
  deleteEntry: (id: string) => Promise<void>
  setFilter: (filter: QueueStatus | 'all') => void
  clearError: () => void
  clearCurrentEntry: () => void
  toggleSelection: (id: string) => void
  selectAll: (ids: string[]) => void
  clearSelection: () => void
  retryBulk: (ids: string[]) => Promise<void>
  deleteBulk: (ids: string[]) => Promise<void>
}

export const useQueueStore = create<QueueState>((set, get) => ({
  entries: [],
  currentEntry: null,
  isLoading: false,
  error: null,
  filter: 'all',
  selectedIds: [],

  fetchQueue: async (filter?: QueueStatus | 'all') => {
    set({ isLoading: true, error: null })

    try {
      const filterParam = filter || get().filter
      const path = filterParam === 'all'
        ? '/admin/queue'
        : `/admin/queue?status=${filterParam}`

      const response = await apiV1.request(path, { method: 'GET' })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch queue entries')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      const entries = Array.isArray(data) ? data : (data.entries || [])
      set({
        entries,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch queue entries',
        isLoading: false,
      })
      throw error
    }
  },

  fetchEntry: async (id: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/queue/${id}`, { method: 'GET' })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch queue entry')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        currentEntry: data,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch queue entry',
        isLoading: false,
      })
      throw error
    }
  },

  retryEntry: async (id: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/queue/${id}/retry`, { method: 'POST' })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to retry queue entry')
      }

      // Refresh the queue list and current entry if viewing details
      const currentFilter = get().filter
      await get().fetchQueue(currentFilter)

      if (get().currentEntry?.id === id) {
        await get().fetchEntry(id)
      }

      set({
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to retry queue entry',
        isLoading: false,
      })
      throw error
    }
  },

  deleteEntry: async (id: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/queue/${id}`, { method: 'DELETE' })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete queue entry')
      }

      set({
        entries: get().entries.filter((e) => e.id !== id),
        currentEntry: get().currentEntry?.id === id ? null : get().currentEntry,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete queue entry',
        isLoading: false,
      })
      throw error
    }
  },

  setFilter: (filter: QueueStatus | 'all') => {
    set({ filter })
  },

  clearError: () => {
    set({ error: null })
  },

  clearCurrentEntry: () => {
    set({ currentEntry: null })
  },

  toggleSelection: (id: string) => {
    set((state) => ({
      selectedIds: state.selectedIds.includes(id)
        ? state.selectedIds.filter((i) => i !== id)
        : [...state.selectedIds, id],
    }))
  },

  selectAll: (ids: string[]) => {
    set({ selectedIds: ids })
  },

  clearSelection: () => {
    set({ selectedIds: [] })
  },

  retryBulk: async (ids: string[]) => {
    set({ isLoading: true, error: null })

    try {
      // Retry each entry sequentially
      for (const id of ids) {
        await apiV1.request(`/admin/queue/${id}/retry`, { method: 'POST' })
      }

      // Refresh queue and clear selection
      await get().fetchQueue(get().filter)
      set({ selectedIds: [], isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Bulk retry failed',
        isLoading: false,
      })
      throw error
    }
  },

  deleteBulk: async (ids: string[]) => {
    set({ isLoading: true, error: null })

    try {
      // Delete each entry sequentially
      for (const id of ids) {
        await apiV1.request(`/admin/queue/${id}`, { method: 'DELETE' })
      }

      // Refresh queue and clear selection
      await get().fetchQueue(get().filter)
      set({ selectedIds: [], isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Bulk delete failed',
        isLoading: false,
      })
      throw error
    }
  },
}))
