import { create } from 'zustand'
import { apiV1 } from '../api'

interface Domain {
  id: number
  name: string
  server_type: string
  active: boolean
  default_quota_bytes: number
  dkim_selector: string
  created_at: string
  updated_at: string
}

interface Mailbox {
  id: number
  domain_id: number
  local_part: string
  address: string
  display_name: string | null
  domain: Domain
  quota_bytes: number
  quota_used_bytes: number
  active: boolean
  last_login_at: string | null
  created_at: string
  updated_at: string
  quota_usage?: {
    mailbox_id: number
    subject_bytes: number
    body_bytes: number
    attachment_bytes: number
    message_count: number
    updated_at: string
  }
}

interface MailboxState {
  mailboxes: Mailbox[]
  isLoading: boolean
  error: string | null
  selectedDomain: string | null

  // Actions
  fetchMailboxes: (token: string) => Promise<void>
  createMailbox: (
    token: string,
    data: {
      email: string
      password: string
      display_name?: string
      quota_bytes?: number
    }
  ) => Promise<void>
  updateMailbox: (
    token: string,
    id: number,
    data: {
      display_name?: string
      quota_bytes?: number
      enabled?: boolean
      password?: string
    }
  ) => Promise<void>
  deleteMailbox: (token: string, id: number) => Promise<void>
  setSelectedDomain: (domain: string | null) => void
  clearError: () => void
}

export const useMailboxStore = create<MailboxState>((set, get) => ({
  mailboxes: [],
  isLoading: false,
  error: null,
  selectedDomain: null,

  fetchMailboxes: async (token: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request('/admin/mailboxes', { method: 'GET' }, token)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch mailboxes')
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({
        mailboxes: Array.isArray(data) ? data : (data.mailboxes || []),
        isLoading: false
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch mailboxes',
        isLoading: false,
      })
      throw error
    }
  },

  createMailbox: async (token: string, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/mailboxes',
        {
          method: 'POST',
          body: JSON.stringify(data),
        },
        token
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create mailbox')
      }

      // Refresh the list
      await get().fetchMailboxes(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create mailbox',
        isLoading: false,
      })
      throw error
    }
  },

  updateMailbox: async (token: string, id: string, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/mailboxes/${id}`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
        },
        token
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to update mailbox')
      }

      // Refresh the list
      await get().fetchMailboxes(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update mailbox',
        isLoading: false,
      })
      throw error
    }
  },

  deleteMailbox: async (token: string, id: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/mailboxes/${id}`, { method: 'DELETE' }, token)

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete mailbox')
      }

      // Refresh the list
      await get().fetchMailboxes(token)
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete mailbox',
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
