import { create } from 'zustand'
import { apiV1 } from '../api'

interface Ban {
  id: number
  ip: string
  reason: string
  protocol: 'smtp' | 'imap' | 'pop3' | 'all'
  created_by: string
  expires_at: string | null
  created_at: string
  updated_at: string
}

interface BanState {
  bans: Ban[]
  isLoading: boolean
  error: string | null
  pagination: {
    total: number
    hasMore: boolean
  }

  // Actions
  fetchBans: (
    filters: {
      protocol?: string
      active?: boolean
      limit?: number
      offset?: number
    },
    accessToken: string
  ) => Promise<void>
  createBan: (
    data: {
      ip: string
      reason: string
      protocol: string
      duration?: string
      created_by: string
    },
    accessToken: string
  ) => Promise<void>
  deleteBan: (id: number, accessToken: string) => Promise<void>
  deleteBanByIP: (ip: string, accessToken: string) => Promise<void>
  isExpired: (ban: Ban) => boolean
  clearError: () => void
}

export const useBanStore = create<BanState>((set, get) => ({
  bans: [],
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchBans: async (filters, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const params = new URLSearchParams()
      if (filters.protocol) params.set('protocol', filters.protocol)
      if (filters.active !== undefined) params.set('active', String(filters.active))
      if (filters.limit) params.set('limit', String(filters.limit))
      if (filters.offset) params.set('offset', String(filters.offset))

      const response = await apiV1.request(
        `/admin/bans?${params.toString()}`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch bans')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        bans: data.items || data,
        pagination: {
          total: data.pagination?.total || 0,
          hasMore: data.pagination?.has_more || false,
        },
        isLoading: false,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch bans',
        isLoading: false,
      })
      throw error
    }
  },

  createBan: async (data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        '/admin/bans',
        {
          method: 'POST',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create ban')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create ban',
        isLoading: false,
      })
      throw error
    }
  },

  deleteBan: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/bans/${id}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete ban')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete ban',
        isLoading: false,
      })
      throw error
    }
  },

  deleteBanByIP: async (ip: string, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/bans/ip/${ip}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to unban IP')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to unban IP',
        isLoading: false,
      })
      throw error
    }
  },

  isExpired: (ban: Ban) => {
    if (!ban.expires_at) return false
    return new Date(ban.expires_at) < new Date()
  },

  clearError: () => set({ error: null }),
}))
