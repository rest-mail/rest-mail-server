import { create } from 'zustand'
import { apiV1 } from '../api'

// A grant gives an account a role at a scope: "*", "domain:<name>" or
// "mailbox:<address>". What makes an account an admin.
export interface Grant {
  id: number
  holder: { kind: string; id: number }
  role: string
  role_id: number
  scope: string
  mode: 'active' | 'eligible'
  granted_at: string
  expires_at: string | null
}

export interface Role {
  id: number
  name: string
  description: string
  system_role: boolean
  capabilities: string[]
}

interface GrantState {
  grants: Grant[]
  roles: Role[]
  isLoading: boolean
  error: string | null

  fetchGrants: () => Promise<void>
  fetchRoles: () => Promise<void>
  createGrant: (data: { accountId: number; role: string; scope: string; mode: 'active' | 'eligible' }) => Promise<void>
  deleteGrant: (id: number) => Promise<void>
  clearError: () => void
}

async function errorMessage(response: Response, fallback: string): Promise<string> {
  const body = await response.json().catch(() => ({}))
  return body?.error?.message || fallback
}

export const useGrantStore = create<GrantState>((set, get) => ({
  grants: [],
  roles: [],
  isLoading: false,
  error: null,

  fetchGrants: async () => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request('/admin/grants?limit=200', { method: 'GET' })
      if (!response.ok) {
        throw new Error(await errorMessage(response, 'Failed to fetch grants'))
      }
      const body = await response.json()
      set({ grants: body.data || [], isLoading: false })
    } catch (error) {
      set({ error: error instanceof Error ? error.message : 'Failed to fetch grants', isLoading: false })
    }
  },

  fetchRoles: async () => {
    try {
      const response = await apiV1.request('/admin/roles', { method: 'GET' })
      if (!response.ok) {
        throw new Error(await errorMessage(response, 'Failed to fetch roles'))
      }
      const body = await response.json()
      set({ roles: body.data || [] })
    } catch (error) {
      set({ error: error instanceof Error ? error.message : 'Failed to fetch roles' })
    }
  },

  // Granting needs users:write, a session signed in with 2FA, and holding
  // everything the role gives at a scope covering the grant's.
  createGrant: async (data) => {
    set({ error: null })
    const response = await apiV1.request('/admin/grants', {
      method: 'POST',
      body: JSON.stringify({
        holder: { kind: 'account', id: data.accountId },
        role: data.role,
        scope: data.scope,
        mode: data.mode,
      }),
    })
    if (!response.ok) {
      const message = await errorMessage(response, 'Failed to grant')
      set({ error: message })
      throw new Error(message)
    }
    await get().fetchGrants()
  },

  deleteGrant: async (id) => {
    set({ error: null })
    const response = await apiV1.request(`/admin/grants/${id}`, { method: 'DELETE' })
    if (!response.ok) {
      const message = await errorMessage(response, 'Failed to remove the grant')
      set({ error: message })
      throw new Error(message)
    }
    await get().fetchGrants()
  },

  clearError: () => set({ error: null }),
}))
