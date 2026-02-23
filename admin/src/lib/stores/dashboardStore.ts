import { create } from 'zustand'
import { apiV1 } from '../api'

interface QueueStats {
  pending: number
  processing: number
  failed: number
}

interface MessageVolumeData {
  date: string
  count: number
}

interface RecentActivity {
  id: string
  type: 'domain_created' | 'mailbox_created' | 'message_sent' | 'message_received'
  description: string
  timestamp: string
}

interface DashboardStats {
  domainCount: number
  mailboxCount: number
  queueStats: QueueStats
  messageVolume: MessageVolumeData[]
  recentActivity: RecentActivity[]
}

interface DashboardState {
  stats: DashboardStats | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchDashboardStats: (accessToken: string) => Promise<void>
  clearError: () => void
}

export const useDashboardStore = create<DashboardState>()((set) => ({
  stats: null,
  isLoading: false,
  error: null,

  fetchDashboardStats: async (accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request('/admin/stats', { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch dashboard stats')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data

      set({
        stats: data,
        isLoading: false,
        error: null,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch dashboard stats',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => {
    set({ error: null })
  },
}))
