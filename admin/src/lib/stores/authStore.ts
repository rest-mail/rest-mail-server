import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { apiV1 } from '../api'
import type { SecondFactor } from '../twoFactor'

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
  // totpRequired flips true when the API answered a login with a 2FA challenge
  // (error code "totp_required"); the login form then collects the second factor
  // and retries. It stays false for accounts without 2FA.
  totpRequired: boolean
  error: string | null

  // Actions
  login: (username: string, password: string, second?: SecondFactor) => Promise<void>
  logout: () => void
  checkSession: () => Promise<void>
  setUser: (user: User | null) => void
  setAccessToken: (token: string | null) => void
  clearError: () => void
  // resetTotpChallenge returns the login form to the credentials step.
  resetTotpChallenge: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      user: null,
      accessToken: null,
      isAuthenticated: false,
      isLoading: false,
      totpRequired: false,
      error: null,

      login: async (username: string, password: string, second?: SecondFactor) => {
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
              ...second,
            }),
          })

          if (!response.ok) {
            const body = await response.json().catch(() => ({}))
            // A 2FA-active account answers the first (single-factor) attempt with
            // a totp_required challenge — a prompt for the second factor, not an
            // error to display.
            if (body?.error?.code === 'totp_required') {
              set({ totpRequired: true, isLoading: false, error: null })
              return
            }
            throw new Error(body?.error?.message || 'Login failed')
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
            totpRequired: false,
          })
        } catch (error) {
          // Leave totpRequired untouched: if the user is on the 2FA step and
          // submits a wrong code (a generic 401), they must stay on that step
          // with the error shown, not get bounced back to the password form.
          set({
            error: error instanceof Error ? error.message : 'Login failed',
            isLoading: false,
            isAuthenticated: false,
          })
          throw error
        }
      },

      resetTotpChallenge: () => set({ totpRequired: false, error: null }),

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
