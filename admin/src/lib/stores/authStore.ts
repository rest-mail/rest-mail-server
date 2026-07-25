import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { apiV1 } from '../api'

interface User {
  username: string
  capabilities: string[]
}

// COOKIE_SESSION is a non-secret sentinel stored in `accessToken` purely as a
// "there is a live session" flag for the many components that gate rendering and
// fetching on `accessToken` being truthy. The real access token is NEVER held in
// JavaScript — it lives only in the httpOnly restmail_access cookie the API sets
// and the browser attaches automatically. This value is deliberately not a
// usable credential and is never persisted.
const COOKIE_SESSION = 'cookie-session'

interface AuthState {
  user: User | null
  accessToken: string | null
  isAuthenticated: boolean
  isLoading: boolean
  error: string | null

  // Actions
  login: (username: string, password: string) => Promise<void>
  logout: () => void
  checkSession: () => Promise<void>
  setUser: (user: User | null) => void
  setAccessToken: (token: string | null) => void
  clearError: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
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
            credentials: 'include',
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

          // The access token was set as an httpOnly cookie by the API; the body
          // carries only identity + capabilities, never the token.
          const user: User = {
            username,
            capabilities: data.capabilities || [],
          }

          set({
            user,
            accessToken: COOKIE_SESSION,
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

      // checkSession restores a session on boot (and after the persisted flag
      // says we were logged in) by exchanging the httpOnly refresh cookie for a
      // fresh access cookie. No token is ever exposed to JS; on failure the
      // session is cleared so route guards send the user to /login.
      checkSession: async () => {
        try {
          const response = await apiV1.request('/auth/refresh', { method: 'POST' })
          if (response.ok) {
            const body = await response.json()
            const data = body.data || body
            set({
              user: {
                username: data.user?.email || data.user?.display_name || '',
                capabilities: data.capabilities || [],
              },
              accessToken: COOKIE_SESSION,
              isAuthenticated: true,
              error: null,
            })
            return
          }
        } catch {
          // fall through to unauthenticated
        }
        set({ user: null, accessToken: null, isAuthenticated: false })
      },

      logout: () => {
        // Revoke the session server-side (clears the refresh + access + CSRF
        // cookies and the refresh-token ledger row); best-effort.
        void apiV1.request('/auth/logout', { method: 'POST' })
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
      // The access token is NOT persisted (it never lives in JS at all now); only
      // non-secret UI state is kept so the shell can render optimistically while
      // checkSession re-validates the httpOnly cookie on boot.
      partialize: (state) => ({
        user: state.user,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
)
