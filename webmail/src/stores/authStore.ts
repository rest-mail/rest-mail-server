import { create } from 'zustand';
import type { User } from '../types';
import * as api from '../api/client';
import { ApiError, type SecondFactor } from '../api/client';

interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  // sessionChecked flips true once the boot-time refresh has resolved, so the UI
  // can avoid flashing the login page before we know whether a session exists.
  sessionChecked: boolean;
  // totpRequired flips true when the API answered a login with a 2FA challenge
  // (error code "totp_required"); the login form then collects the second factor
  // and retries. It stays false for accounts without 2FA.
  totpRequired: boolean;
  error: string | null;
  login: (email: string, password: string, second?: SecondFactor) => Promise<void>;
  logout: () => void;
  checkSession: () => Promise<void>;
  // resetTotpChallenge returns the login form to the credentials step.
  resetTotpChallenge: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isAuthenticated: false,
  isLoading: false,
  sessionChecked: false,
  totpRequired: false,
  error: null,

  login: async (email, password, second) => {
    set({ isLoading: true, error: null });
    try {
      // The token is set by the API as an httpOnly cookie; JS only receives the
      // user identity. Nothing token-shaped is stored client-side.
      const resp = await api.login(email, password, second);
      set({
        user: resp.data.user,
        isAuthenticated: true,
        isLoading: false,
        sessionChecked: true,
        totpRequired: false,
      });
    } catch (err) {
      // A 2FA-active account answers the first (single-factor) attempt with a
      // totp_required challenge — not an error to display, but a prompt for the
      // second factor.
      if (err instanceof ApiError && err.code === 'totp_required') {
        set({ totpRequired: true, isLoading: false, error: null });
        return;
      }
      set({
        error: err instanceof Error ? err.message : 'Login failed',
        isLoading: false,
      });
    }
  },

  resetTotpChallenge: () => set({ totpRequired: false, error: null }),

  // checkSession restores an existing session on page load by exchanging the
  // httpOnly refresh cookie for a fresh access cookie. No token is ever exposed
  // to JS; we only learn the user identity to render the UI.
  checkSession: async () => {
    try {
      const data = await api.refreshSession();
      if (data?.user) {
        set({ user: data.user, isAuthenticated: true, sessionChecked: true });
        return;
      }
    } catch {
      // fall through to unauthenticated
    }
    set({ isAuthenticated: false, sessionChecked: true });
  },

  logout: () => {
    api.logout();
    set({ user: null, isAuthenticated: false });
  },
}));
