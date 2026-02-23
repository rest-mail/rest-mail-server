import { create } from 'zustand'
import { apiV1 } from '../api'

interface CustomFilter {
  id: number
  domain_id: number
  name: string
  description: string
  filter_type: 'action' | 'transform'
  direction: 'inbound' | 'outbound' | 'both'
  config: {
    script: string
  }
  enabled: boolean
  created_at: string
  updated_at: string
}

interface ValidationResult {
  valid: boolean
  errors?: string[]
  warnings?: string[]
}

interface FilterTestResult {
  action: 'continue' | 'reject' | 'quarantine' | 'discard'
  result: string
  detail?: string
  duration_ms?: number
  message?: any
  errors?: string[]
}

interface CustomFilterState {
  filters: CustomFilter[]
  currentFilter: CustomFilter | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchFilters: (accessToken: string, domainId?: number) => Promise<void>
  fetchFilter: (id: number, accessToken: string) => Promise<CustomFilter>
  createFilter: (data: Partial<CustomFilter>, accessToken: string) => Promise<CustomFilter>
  updateFilter: (id: number, data: Partial<CustomFilter>, accessToken: string) => Promise<CustomFilter>
  deleteFilter: (id: number, accessToken: string) => Promise<void>
  validateScript: (script: string, accessToken: string, email?: any) => Promise<ValidationResult>
  testFilter: (id: number, accessToken: string, email?: any) => Promise<FilterTestResult>
  clearError: () => void
  clearCurrentFilter: () => void
}

export const useCustomFilterStore = create<CustomFilterState>((set, get) => ({
  filters: [],
  currentFilter: null,
  isLoading: false,
  error: null,

  fetchFilters: async (accessToken: string, domainId?: number) => {
    set({ isLoading: true, error: null })

    try {
      const url = domainId
        ? `/admin/custom-filters?domain_id=${domainId}`
        : '/admin/custom-filters'

      const response = await apiV1.request(url, { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch custom filters')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        filters: Array.isArray(data) ? data : data.filters || [],
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch custom filters',
        isLoading: false,
      })
      throw error
    }
  },

  fetchFilter: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/custom-filters/${id}`,
        { method: 'GET' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch custom filter')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        currentFilter: data,
        isLoading: false,
        error: null,
      })
      return data
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch custom filter',
        isLoading: false,
      })
      throw error
    }
  },

  createFilter: async (data: Partial<CustomFilter>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/custom-filters',
        {
          method: 'POST',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create custom filter')
      }

      const filter = await response.json()
      set({
        filters: [...get().filters, filter],
        isLoading: false,
        error: null,
      })
      return filter
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create custom filter',
        isLoading: false,
      })
      throw error
    }
  },

  updateFilter: async (id: number, data: Partial<CustomFilter>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/custom-filters/${id}`,
        {
          method: 'PATCH',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update custom filter')
      }

      const filter = await response.json()
      set({
        filters: get().filters.map((f) => (f.id === id ? filter : f)),
        currentFilter: filter,
        isLoading: false,
        error: null,
      })
      return filter
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update custom filter',
        isLoading: false,
      })
      throw error
    }
  },

  deleteFilter: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/custom-filters/${id}`,
        { method: 'DELETE' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete custom filter')
      }

      set({
        filters: get().filters.filter((f) => f.id !== id),
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete custom filter',
        isLoading: false,
      })
      throw error
    }
  },

  validateScript: async (script: string, accessToken: string, email?: any) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/custom-filters/validate',
        {
          method: 'POST',
          body: JSON.stringify({ script, email }),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to validate script')
      }

      const result = await response.json()
      set({ isLoading: false, error: null })
      return result
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to validate script',
        isLoading: false,
      })
      throw error
    }
  },

  testFilter: async (id: number, accessToken: string, email?: any) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/custom-filters/${id}/test`,
        {
          method: 'POST',
          body: JSON.stringify({ email }),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to test custom filter')
      }

      const result = await response.json()
      set({ isLoading: false, error: null })
      return result
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to test custom filter',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => {
    set({ error: null })
  },

  clearCurrentFilter: () => {
    set({ currentFilter: null })
  },
}))

// Export types for use in components
export type { CustomFilter, ValidationResult, FilterTestResult }
