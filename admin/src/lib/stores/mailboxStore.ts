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
  fetchMailboxes: () => Promise<void>
  createMailbox: (
    data: {
      email: string
      // Optional: an account with no password can't be signed in to, and is
      // reached only through its owners (the account creating it).
      password?: string
      display_name?: string
      quota_bytes?: number
    }
  ) => Promise<void>
  deleteMailbox: (id: number) => Promise<void>
  setSelectedDomain: (domain: string | null) => void
  clearError: () => void
}

export const useMailboxStore = create<MailboxState>((set, get) => ({
  mailboxes: [],
  isLoading: false,
  error: null,
  selectedDomain: null,

  fetchMailboxes: async () => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request('/admin/mailboxes', { method: 'GET' })

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

  createMailbox: async (data) => {
    set({ isLoading: true, error: null })

    try {
      // An account is created the same way for everyone: POST /accounts, which
      // needs mailboxes:write on its domain and makes the creator its owner.
      const response = await apiV1.request('/accounts', {
        method: 'POST',
        body: JSON.stringify({
          address: data.email,
          display_name: data.display_name || undefined,
          password: data.password || undefined,
        }),
      })

      if (!response.ok) {
        const error = await response.json().catch(() => ({}))
        throw new Error(error?.error?.message || 'Failed to create the account')
      }
      const created = await response.json()
      const id = created?.data?.id
      if (id && data.quota_bytes) {
        await apiV1.request(`/admin/mailboxes/${id}`, {
          method: 'PATCH',
          body: JSON.stringify({ quota_bytes: data.quota_bytes }),
        })
      }

      // Refresh the list
      await get().fetchMailboxes()
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create the account',
        isLoading: false,
      })
      throw error
    }
  },

  deleteMailbox: async (id: number) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/mailboxes/${id}`, { method: 'DELETE' })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete mailbox')
      }

      // Refresh the list
      await get().fetchMailboxes()
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
