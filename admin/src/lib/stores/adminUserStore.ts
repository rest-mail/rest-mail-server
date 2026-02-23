import { create } from 'zustand'
import { apiV1 } from '../api'

interface Role {
  id: number
  name: string
  description: string
  system_role: boolean
  created_at: string
  updated_at: string
}

interface Capability {
  id: number
  name: string
  description: string
  resource: string
  action: string
  created_at: string
}

interface AdminUser {
  id: number
  username: string
  email?: string
  password_change_required: boolean
  last_password_change?: string
  active: boolean
  created_at: string
  updated_at: string
  roles?: Role[]
}

interface AdminUserState {
  adminUsers: AdminUser[]
  roles: Role[]
  capabilities: Capability[]
  isLoading: boolean
  error: string | null

  // Actions
  fetchAdminUsers: (accessToken: string) => Promise<void>
  fetchRoles: (accessToken: string) => Promise<void>
  fetchCapabilities: (accessToken: string) => Promise<void>
  createAdminUser: (accessToken: string, data: { username: string; email?: string; password: string; role_ids?: number[] }) => Promise<AdminUser>
  updateAdminUser: (accessToken: string, id: number, data: { email?: string; password?: string; active?: boolean; password_change_required?: boolean; role_ids?: number[] }) => Promise<void>
  deleteAdminUser: (accessToken: string, id: number) => Promise<void>
  setError: (error: string | null) => void
  clearError: () => void
}

export const useAdminUserStore = create<AdminUserState>((set, get) => ({
  adminUsers: [],
  roles: [],
  capabilities: [],
  isLoading: false,
  error: null,

  fetchAdminUsers: async (accessToken: string) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request('/admin/admin-users', { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to fetch admin users'
        throw new Error(errorMsg)
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ adminUsers: data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch admin users',
        isLoading: false,
      })
      throw error
    }
  },

  fetchRoles: async (accessToken: string) => {
    try {
      const response = await apiV1.request('/admin/roles', { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to fetch roles'
        throw new Error(errorMsg)
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ roles: data })
    } catch (error) {
      console.error('Failed to fetch roles:', error)
      throw error
    }
  },

  fetchCapabilities: async (accessToken: string) => {
    try {
      const response = await apiV1.request('/admin/capabilities', { method: 'GET' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to fetch capabilities'
        throw new Error(errorMsg)
      }

      const response_data = await response.json()
      const data = response_data.data || response_data
      set({ capabilities: data })
    } catch (error) {
      console.error('Failed to fetch capabilities:', error)
      throw error
    }
  },

  createAdminUser: async (accessToken: string, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        '/admin/admin-users',
        {
          method: 'POST',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to create admin user'
        throw new Error(errorMsg)
      }

      const newUser = await response.json()

      // Refresh the list
      await get().fetchAdminUsers(accessToken)

      set({ isLoading: false })
      return newUser
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create admin user',
        isLoading: false,
      })
      throw error
    }
  },

  updateAdminUser: async (accessToken: string, id: number, data) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(
        `/admin/admin-users/${id}`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
        },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to update admin user'
        throw new Error(errorMsg)
      }

      // Refresh the list
      await get().fetchAdminUsers(accessToken)

      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to update admin user',
        isLoading: false,
      })
      throw error
    }
  },

  deleteAdminUser: async (accessToken: string, id: number) => {
    set({ isLoading: true, error: null })

    try {
      const response = await apiV1.request(`/admin/admin-users/${id}`, { method: 'DELETE' }, accessToken)

      if (!response.ok) {
        const error = await response.json()
        const errorMsg = typeof error.error === 'string'
          ? error.error
          : error.message || JSON.stringify(error) || 'Failed to delete admin user'
        throw new Error(errorMsg)
      }

      // Remove from local state
      set(state => ({
        adminUsers: state.adminUsers.filter(user => user.id !== id),
        isLoading: false,
      }))
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete admin user',
        isLoading: false,
      })
      throw error
    }
  },

  setError: (error) => set({ error }),
  clearError: () => set({ error: null }),
}))
