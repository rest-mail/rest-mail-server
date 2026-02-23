import { create } from 'zustand'
import { apiV1 } from '../api'

interface TLSReport {
  id: number
  domain_id: number
  reporting_org: string
  start_date: string
  end_date: string
  policy_type: 'sts' | 'tlsa' | 'no-policy'
  policy_domain: string
  total_successful: number
  total_failure: number
  failure_details: any
  raw_report: string
  received_at: string
}

interface TLSReportState {
  reports: TLSReport[]
  currentReport: TLSReport | null
  isLoading: boolean
  error: string | null
  pagination: {
    total: number
    hasMore: boolean
  }

  // Actions
  fetchReports: (
    filters: {
      domain_id?: number
      policy_type?: string
      reporting_org?: string
      limit?: number
      offset?: number
    },
    accessToken: string
  ) => Promise<void>
  selectReport: (report: TLSReport) => void
  clearCurrentReport: () => void
  clearError: () => void
}

export const useTLSReportStore = create<TLSReportState>((set) => ({
  reports: [],
  currentReport: null,
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchReports: async (filters, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const params = new URLSearchParams()
      if (filters.domain_id) params.set('domain_id', String(filters.domain_id))
      if (filters.policy_type) params.set('policy_type', filters.policy_type)
      if (filters.reporting_org) params.set('reporting_org', filters.reporting_org)
      if (filters.limit) params.set('limit', String(filters.limit))
      if (filters.offset) params.set('offset', String(filters.offset))

      const response = await apiV1.request(
        `/admin/tls-reports?${params.toString()}`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch TLS reports')
      }
      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        reports: data.items || data,
        pagination: {
          total: data.pagination?.total || 0,
          hasMore: data.pagination?.has_more || false,
        },
        isLoading: false,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch TLS reports',
        isLoading: false,
      })
      throw error
    }
  },

  selectReport: (report: TLSReport) => set({ currentReport: report }),
  clearCurrentReport: () => set({ currentReport: null }),
  clearError: () => set({ error: null }),
}))
