import { create } from 'zustand';

export type Theme = 'dawn' | 'linen' | 'slate' | 'dusk' | 'midnight' | 'forest';
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

const DARK_THEMES: Theme[] = ['midnight', 'forest', 'slate', 'dusk'];

function getInitialTheme(): Theme {
  const stored = localStorage.getItem('restmail-theme');
  const valid: Theme[] = ['dawn', 'linen', 'slate', 'dusk', 'midnight', 'forest'];
  if (stored && valid.includes(stored as Theme)) return stored as Theme;
  // Legacy mapping
  if (stored === 'dark') return 'midnight';
  if (stored === 'light') return 'dawn';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'midnight' : 'dawn';
}

function applyTheme(theme: Theme) {
  const all: Theme[] = ['dawn', 'linen', 'slate', 'dusk', 'midnight', 'forest'];
  // Remove all palette classes, add the selected one
  document.documentElement.classList.remove(...all, 'dark');
  document.documentElement.classList.add(theme);
  // Keep .dark for any shadcn components that use it directly
  if (DARK_THEMES.includes(theme)) {
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
