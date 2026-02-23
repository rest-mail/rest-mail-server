import { create } from 'zustand'
import { apiV1 } from '../api'

interface MTASTSPolicy {
  id: number
  domain_id: number
  mode: 'none' | 'testing' | 'enforce'
  mx_hosts: string
  max_age: number
  active: boolean
  created_at: string
  updated_at: string
}

interface MTASTSState {
  policies: Map<number, MTASTSPolicy>
  currentPolicy: MTASTSPolicy | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchPolicy: (domainId: number, accessToken: string) => Promise<void>
  setPolicy: (
    domainId: number,
    data: {
      mode: string
      mx_hosts: string
      max_age?: number
      active?: boolean
    },
    accessToken: string
  ) => Promise<void>
  deletePolicy: (domainId: number, accessToken: string) => Promise<void>
  clearCurrentPolicy: () => void
  clearError: () => void
}

export const useMTASTSStore = create<MTASTSState>((set, get) => ({
  policies: new Map(),
  currentPolicy: null,
  isLoading: false,
  error: null,

  fetchPolicy: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        if (response.status === 404) {
          // No policy exists for this domain
          set({ currentPolicy: null, isLoading: false })
          return
        }
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch MTA-STS policy')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      const policies = new Map(get().policies)
      policies.set(domainId, data)
      set({ policies, currentPolicy: data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  setPolicy: async (domainId: number, data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to set MTA-STS policy')
      }
      const result = await response.json()
      const policies = new Map(get().policies)
      policies.set(domainId, result)
      set({ policies, currentPolicy: result, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to set MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  deletePolicy: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: 'DELETE' },
        accessToken
      )
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete MTA-STS policy')
      }
      const policies = new Map(get().policies)
      policies.delete(domainId)
      set({ policies, currentPolicy: null, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  clearCurrentPolicy: () => set({ currentPolicy: null }),
  clearError: () => set({ error: null }),
}))
