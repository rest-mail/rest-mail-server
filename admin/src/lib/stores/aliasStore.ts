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

      const response_data = await response.json()
      const data = response_data.data || response_data
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
