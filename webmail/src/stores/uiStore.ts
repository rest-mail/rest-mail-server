import { create } from 'zustand';

type Theme = 'light' | 'dark';
type View = 'mail' | 'compose' | 'addAccount' | 'accountDetails';

interface ComposeState {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  inReplyTo?: string;
  quoteHtml?: string;
}

interface UIState {
  theme: Theme;
  view: View;
  composeState: ComposeState | null;
  sidebarCollapsed: Record<number, boolean>;

  setTheme: (theme: Theme) => void;
  toggleTheme: () => void;
  setView: (view: View) => void;
  startCompose: (state?: ComposeState) => void;
  closeCompose: () => void;
  toggleAccountCollapsed: (accountId: number) => void;
}

function getInitialTheme(): Theme {
  const stored = localStorage.getItem('restmail-theme');
  if (stored === 'dark' || stored === 'light') return stored;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle('dark', theme === 'dark');
  localStorage.setItem('restmail-theme', theme);
}

const initialTheme = getInitialTheme();
applyTheme(initialTheme);

export const useUIStore = create<UIState>((set, get) => ({
  theme: initialTheme,
  view: 'mail',
  composeState: null,
  sidebarCollapsed: {},

  setTheme: (theme) => {
    applyTheme(theme);
    set({ theme });
  },

  toggleTheme: () => {
    const next = get().theme === 'light' ? 'dark' : 'light';
    applyTheme(next);
    set({ theme: next });
  },

  setView: (view) => set({ view }),

  startCompose: (state) => set({
    view: 'compose',
    composeState: state || { to: '', cc: '', bcc: '', subject: '' },
  }),

  closeCompose: () => set({ view: 'mail', composeState: null }),

  toggleAccountCollapsed: (accountId) => set(state => ({
    sidebarCollapsed: {
      ...state.sidebarCollapsed,
      [accountId]: !state.sidebarCollapsed[accountId],
    },
  })),
}));
