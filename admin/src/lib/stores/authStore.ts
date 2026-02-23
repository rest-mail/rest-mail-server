import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { apiV1 } from '../api'

interface User {
  username: string
  capabilities: string[]
}

interface AuthState {
  user: User | null
  accessToken: string | null
  isAuthenticated: boolean
  isLoading: boolean
  error: string | null

  // Actions
  login: (username: string, password: string) => Promise<void>
  logout: () => void
  setUser: (user: User | null) => void
  setAccessToken: (token: string | null) => void
  clearError: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      user: null,
      accessToken: null,
      isAuthenticated: false,
      isLoading: false,
      error: null,

      login: async (username: string, password: string) => {
        set({ isLoading: true, error: null })

        try {
          const response = await fetch(apiV1.url('/auth/login'), {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              username,
              password,
            }),
          })

          if (!response.ok) {
            const error = await response.json()
            throw new Error(error.error || 'Login failed')
          }

          const response_data = await response.json()
          const data = response_data.data || response_data

          // Extract capabilities from the JWT token
          // The backend returns capabilities in the token
          const user: User = {
            username,
            capabilities: data.capabilities || [],
          }

          set({
            user,
            accessToken: data.access_token,
            isAuthenticated: true,
            isLoading: false,
            error: null,
          })
        } catch (error) {
          set({
            error: error instanceof Error ? error.message : 'Login failed',
            isLoading: false,
            isAuthenticated: false,
          })
          throw error
        }
      },

      logout: () => {
        set({
          user: null,
          accessToken: null,
          isAuthenticated: false,
          error: null,
        })
      },

      setUser: (user) => {
        set({ user, isAuthenticated: !!user })
      },

      setAccessToken: (token) => {
        set({ accessToken: token })
      },

      clearError: () => {
        set({ error: null })
      },
    }),
    {
      name: 'rest-mail-admin-auth',
      partialize: (state) => ({
        user: state.user,
        accessToken: state.accessToken,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
)
