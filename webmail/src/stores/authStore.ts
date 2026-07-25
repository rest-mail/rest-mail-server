import { create } from 'zustand';
import type { User } from '../types';
import * as api from '../api/client';

interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  // sessionChecked flips true once the boot-time refresh has resolved, so the UI
  // can avoid flashing the login page before we know whether a session exists.
  sessionChecked: boolean;
  error: string | null;
  login: (email: string, password: string) => Promise<void>;
  logout: () => void;
  checkSession: () => Promise<void>;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isAuthenticated: false,
  isLoading: false,
  sessionChecked: false,
  error: null,

  login: async (email, password) => {
    set({ isLoading: true, error: null });
    try {
      // The token is set by the API as an httpOnly cookie; JS only receives the
      // user identity. Nothing token-shaped is stored client-side.
      const resp = await api.login(email, password);
      set({
        user: resp.data.user,
        isAuthenticated: true,
        isLoading: false,
        sessionChecked: true,
      });
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : 'Login failed',
        isLoading: false,
      });
    }
  },

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
