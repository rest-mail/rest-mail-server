import { create } from 'zustand'
import { apiV1 } from '../api'

interface DkimEntry {
  domain_id: number
  domain: string
  selector: string
  has_key: boolean
}

interface DkimState {
  entries: DkimEntry[]
  currentEntry: DkimEntry | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchDkimKeys: (accessToken: string) => Promise<void>
  setDkimKey: (
    domainId: number,
    data: { selector: string; private_key: string },
    accessToken: string
  ) => Promise<void>
  deleteDkimKey: (domainId: number, accessToken: string) => Promise<void>
  clearError: () => void
}

export const useDkimStore = create<DkimState>((set) => ({
  entries: [],
  currentEntry: null,
  isLoading: false,
  error: null,

  fetchDkimKeys: async (accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request('/admin/dkim', { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch DKIM keys')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ entries: data.items || data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch DKIM keys',
        isLoading: false,
      })
      throw error
    }
  },

  setDkimKey: async (
    domainId: number,
    data: { selector: string; private_key: string },
    accessToken: string
  ) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/dkim/${domainId}`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to set DKIM key')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to set DKIM key',
        isLoading: false,
      })
      throw error
    }
  },

  deleteDkimKey: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/dkim/${domainId}`,
        { method: 'DELETE' },
        accessToken
      )
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete DKIM key')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete DKIM key',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => set({ error: null }),
}))
