import { create } from 'zustand'
import { apiV1 } from '../api'

interface FilterConfig {
  name: string
  type: 'action' | 'transform'
  enabled: boolean
  unskippable?: boolean
  config?: Record<string, any>
}

interface Pipeline {
  id: number
  domain_id: number
  direction: 'inbound' | 'outbound'
  filters: FilterConfig[]
  active: boolean
  created_at: string
  updated_at: string
  domain?: {
    id: number
    name: string
  }
}

interface FilterLogStep {
  filter: string
  result: string
  detail?: string
  duration_ms?: number
}

interface PipelineLog {
  id: number
  pipeline_id: number
  message_id?: number
  direction: 'inbound' | 'outbound'
  action: 'continue' | 'reject' | 'quarantine' | 'discard'
  steps: FilterLogStep[]
  duration_ms: number
  created_at: string
}

interface PipelineTestResult {
  action: 'continue' | 'reject' | 'quarantine' | 'discard'
  steps: FilterLogStep[]
  duration_ms: number
  message?: any
}

interface FilterTestResult {
  action: 'continue' | 'reject' | 'quarantine' | 'discard'
  result: string
  detail?: string
  duration_ms?: number
  message?: any
}

interface LogQueryParams {
  pipeline_id?: number
  direction?: 'inbound' | 'outbound'
  action?: 'continue' | 'reject' | 'quarantine' | 'discard'
  limit?: number
  offset?: number
}

interface PipelineState {
  pipelines: Pipeline[]
  currentPipeline: Pipeline | null
  logs: PipelineLog[]
  isLoading: boolean
  error: string | null

  // Actions
  fetchPipelines: (accessToken: string, domainId?: number) => Promise<void>
  fetchPipeline: (id: number, accessToken: string) => Promise<Pipeline>
  createPipeline: (data: Partial<Pipeline>, accessToken: string) => Promise<Pipeline>
  updatePipeline: (id: number, data: Partial<Pipeline>, accessToken: string) => Promise<Pipeline>
  deletePipeline: (id: number, accessToken: string) => Promise<void>
  testPipeline: (pipelineId: number, email: any, accessToken: string) => Promise<PipelineTestResult>
  testFilter: (filterName: string, config: any, email: any, accessToken: string) => Promise<FilterTestResult>
  fetchLogs: (params: LogQueryParams, accessToken: string) => Promise<void>
  clearError: () => void
  clearCurrentPipeline: () => void
}

export const usePipelineStore = create<PipelineState>((set, get) => ({
  pipelines: [],
  currentPipeline: null,
  logs: [],
  isLoading: false,
  error: null,

  fetchPipelines: async (accessToken: string, domainId?: number) => {
    set({ isLoading: true, error: null })

    try {
      const url = domainId
        ? `/admin/pipelines?domain_id=${domainId}`
        : '/admin/pipelines'

      const response = await apiV1.request(url, { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch pipelines')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        pipelines: Array.isArray(data) ? data : data.pipelines || [],
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch pipelines',
        isLoading: false,
      })
      throw error
    }
  },

  fetchPipeline: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/pipelines/${id}`,
        { method: 'GET' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch pipeline')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        currentPipeline: data,
        isLoading: false,
        error: null,
      })
      return data
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch pipeline',
        isLoading: false,
      })
      throw error
    }
  },

  createPipeline: async (data: Partial<Pipeline>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/pipelines',
        {
          method: 'POST',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create pipeline')
      }

      const pipeline = await response.json()
      set({
        pipelines: [...get().pipelines, pipeline],
        isLoading: false,
        error: null,
      })
      return pipeline
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create pipeline',
        isLoading: false,
      })
      throw error
    }
  },

  updatePipeline: async (id: number, data: Partial<Pipeline>, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/pipelines/${id}`,
        {
          method: 'PATCH',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update pipeline')
      }

      const pipeline = await response.json()
      set({
        pipelines: get().pipelines.map((p) => (p.id === id ? pipeline : p)),
        currentPipeline: pipeline,
        isLoading: false,
        error: null,
      })
      return pipeline
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update pipeline',
        isLoading: false,
      })
      throw error
    }
  },

  deletePipeline: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/pipelines/${id}`,
        { method: 'DELETE' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete pipeline')
      }

      set({
        pipelines: get().pipelines.filter((p) => p.id !== id),
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete pipeline',
        isLoading: false,
      })
      throw error
    }
  },

  testPipeline: async (pipelineId: number, email: any, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/pipelines/test',
        {
          method: 'POST',
          body: JSON.stringify({ pipeline_id: pipelineId, email }),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to test pipeline')
      }

      const result = await response.json()
      set({ isLoading: false, error: null })
      return result
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to test pipeline',
        isLoading: false,
      })
      throw error
    }
  },

  testFilter: async (filterName: string, config: any, email: any, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/pipelines/test-filter',
        {
          method: 'POST',
          body: JSON.stringify({ filter_name: filterName, config, email }),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to test filter')
      }

      const result = await response.json()
      set({ isLoading: false, error: null })
      return result
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to test filter',
        isLoading: false,
      })
      throw error
    }
  },

  fetchLogs: async (params: LogQueryParams, accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const queryParams = new URLSearchParams()
      if (params.pipeline_id) queryParams.set('pipeline_id', params.pipeline_id.toString())
      if (params.direction) queryParams.set('direction', params.direction)
      if (params.action) queryParams.set('action', params.action)
      if (params.limit) queryParams.set('limit', params.limit.toString())
      if (params.offset) queryParams.set('offset', params.offset.toString())

      const url = `/admin/pipelines/logs${queryParams.toString() ? '?' + queryParams.toString() : ''}`
      const response = await apiV1.request(url, { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch logs')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        logs: Array.isArray(data) ? data : data.logs || [],
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch logs',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => {
    set({ error: null })
  },

  clearCurrentPipeline: () => {
    set({ currentPipeline: null })
  },
}))

// Export types for use in components
export type { Pipeline, FilterConfig, PipelineLog, FilterLogStep, PipelineTestResult, FilterTestResult, LogQueryParams }
