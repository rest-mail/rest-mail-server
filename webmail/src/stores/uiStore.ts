import { create } from 'zustand';
import { THEME_IDS, DARK_THEME_IDS, type ThemeId } from '@/theme/packs';

export type Theme = ThemeId;
type View =
  | 'mail' | 'compose' | 'addAccount' | 'accountDetails'
  | 'vacation' | 'quarantine' | 'tlsReports'
  | 'pipelineTester' | 'pipelineConfig'
  | 'settings' | 'accountSettings';

interface ComposeState {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  inReplyTo?: string;
  quoteHtml?: string;
  draftId?: number;
  bodyHtml?: string;
  bodyText?: string;
}

interface UIState {
  theme: Theme;
  view: View;
  composeState: ComposeState | null;
  sidebarCollapsed: Record<number, boolean>;
  selectedAccountId: number | null;  // for accountSettings view

  setTheme: (theme: Theme) => void;
  setView: (view: View) => void;
  setSelectedAccountId: (id: number | null) => void;
  startCompose: (state?: ComposeState) => void;
  closeCompose: () => void;
  toggleAccountCollapsed: (accountId: number) => void;
}

function getInitialTheme(): Theme {
  const stored = localStorage.getItem('restmail-theme');
  if (stored && (THEME_IDS as string[]).includes(stored)) return stored as Theme;
  return window.matchMedia('(prefers-color-scheme: dark').matches ? 'industrial' : 'dawn';
}

function applyTheme(theme: Theme) {
  // Remove all palette classes, add the selected one
  document.documentElement.classList.remove(...THEME_IDS, 'dark');
  document.documentElement.classList.add(theme);
  // Keep .dark for any shadcn components that use it directly
  if (DARK_THEME_IDS.includes(theme)) {
    document.documentElement.classList.add('dark');
  }
  localStorage.setItem('restmail-theme', theme);
}

const initialTheme = getInitialTheme();
applyTheme(initialTheme);

export const useUIStore = create<UIState>((set) => ({
  theme: initialTheme,
  view: 'mail',
  composeState: null,
  sidebarCollapsed: {},
  selectedAccountId: null,

  setTheme: (theme) => {
    applyTheme(theme);
    set({ theme });
  },

  setView: (view) => set({ view }),

  setSelectedAccountId: (id) => set({ selectedAccountId: id }),

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
