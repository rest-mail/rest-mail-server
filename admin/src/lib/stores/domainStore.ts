import { create } from 'zustand'
import { apiV1 } from '../api'

interface DnsRecord {
  type: string
  name: string
  value: string
  verified: boolean
}

interface Domain {
  id: number
  name: string
  server_type: string
  active: boolean
  created_at: string
  default_quota_bytes?: number
  dns_records?: DnsRecord[]
}

interface DomainState {
  domains: Domain[]
  currentDomain: Domain | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchDomains: (accessToken: string) => Promise<void>
  fetchDomain: (id: string, accessToken: string) => Promise<void>
  createDomain: (domain: Partial<Domain>, accessToken: string) => Promise<void>
  updateDomain: (id: string, domain: Partial<Domain>, accessToken: string) => Promise<void>
  deleteDomain: (id: string, accessToken: string) => Promise<void>
  clearError: () => void
  clearCurrentDomain: () => void
}

export const useDomainStore = create<DomainState>((set, get) => ({
  domains: [],
  currentDomain: null,
  isLoading: false,
  error: null,

  fetchDomains: async (accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request('/admin/domains', { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch domains')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        domains: Array.isArray(data) ? data : (data.domains || []),
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch domains',
        isLoading: false,
      })
      throw error
    }
  },

  fetchDomain: async (id: string, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/domains/${id}`, { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch domain')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        currentDomain: data,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch domain',
        isLoading: false,
      })
      throw error
    }
  },

  createDomain: async (domain: Partial<Domain>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/domains',
        {
          method: 'POST',
          body: JSON.stringify(domain),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create domain')
      }

      const data = await response.json()
      set({
        domains: [...get().domains, data],
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create domain',
        isLoading: false,
      })
      throw error
    }
  },

  updateDomain: async (id: string, domain: Partial<Domain>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/domains/${id}`,
        {
          method: 'PUT',
          body: JSON.stringify(domain),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update domain')
      }

      const data = await response.json()
      set({
        domains: get().domains.map((d) => (d.id === parseInt(id) ? data : d)),
        currentDomain: data,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update domain',
        isLoading: false,
      })
      throw error
    }
  },

  deleteDomain: async (id: string, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/domains/${id}`, { method: 'DELETE' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete domain')
      }

      set({
        domains: get().domains.filter((d) => d.id !== parseInt(id)),
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete domain',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => {
    set({ error: null })
  },

  clearCurrentDomain: () => {
    set({ currentDomain: null })
  },
}))
