# Webmail Themes, Settings & Account Settings Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add 6 named colour palettes to the webmail, a discoverable user-menu trigger (avatar + chevron), a full Settings page (General / Accounts / Notifications tabs), and a per-account gear icon in the sidebar that opens an account settings panel with a Danger Zone.

**Architecture:** All state lives in Zustand stores (uiStore for theme/view/selectedAccountId, new settingsStore for preferences). Themes are CSS custom-property classes applied to `<html>`. All new views integrate into the existing `view` router in `App.tsx`. No new routing library needed.

**Tech Stack:** React 18, TypeScript, Zustand, Tailwind CSS v4, shadcn/ui primitives (DropdownMenu, Card, Button, Input, Label, Badge, Separator), lucide-react icons.

---

## Context for each task

- Run `cd /workspaces/rest-mail/webmail && npm run dev` to see changes live.
- There are no automated tests for the React frontend — manual browser verification is the test.
- Imports use the `@/` alias which maps to `src/`.
- `uiStore.ts` drives what panel is visible in `App.tsx` via the `view` field.
- `mailStore.accounts` is `Account[]`; each has `id: number` and `address: string`.
- `authStore.user.email` identifies the primary (logged-in) account.
- The existing `VacationView` at `src/components/settings/VacationView.tsx` takes `activeAccountId` from `mailStore` — it can be reused inside the new panels by temporarily setting `activeAccountId` in `mailStore` before rendering it.

---

## Task 1: Add 6 colour palettes to index.css

**Files:**
- Modify: `src/index.css`

The file currently has `:root` (light/Dawn) and `.dark` (Midnight). Add four new palette classes and rename the existing ones with comments. The theme is applied by adding a class to `<html>`.

**Step 1: Replace the `:root` and `.dark` blocks and add four new palette classes**

Replace the entire CSS variable section (lines 7–102) with:

```css
/* ── Dawn (light, blue accent) ──────────────────────────────────── */
:root,
.dawn {
  --color-bg: #ffffff;
  --color-bg-secondary: #f3f4f6;
  --color-bg-tertiary: #e5e7eb;
  --color-text: #111827;
  --color-text-secondary: #6b7280;
  --color-border: #d1d5db;
  --color-primary: #2563eb;
  --color-primary-hover: #1d4ed8;
  --color-danger: #dc2626;
  --color-success: #16a34a;
  --color-sidebar: #f9fafb;
  --color-selected: #eff6ff;
  --color-hover: #f3f4f6;
  --color-unread: #111827;
  --radius: 0.625rem;
  --background: oklch(1 0 0);
  --foreground: oklch(0.145 0 0);
  --card: oklch(1 0 0);
  --card-foreground: oklch(0.145 0 0);
  --popover: oklch(1 0 0);
  --popover-foreground: oklch(0.145 0 0);
  --primary: oklch(0.205 0 0);
  --primary-foreground: oklch(0.985 0 0);
  --secondary: oklch(0.97 0 0);
  --secondary-foreground: oklch(0.205 0 0);
  --muted: oklch(0.97 0 0);
  --muted-foreground: oklch(0.556 0 0);
  --accent: oklch(0.97 0 0);
  --accent-foreground: oklch(0.205 0 0);
  --destructive: oklch(0.577 0.245 27.325);
  --border: oklch(0.922 0 0);
  --input: oklch(0.922 0 0);
  --ring: oklch(0.708 0 0);
  --chart-1: oklch(0.646 0.222 41.116);
  --chart-2: oklch(0.6 0.118 184.704);
  --chart-3: oklch(0.398 0.07 227.392);
  --chart-4: oklch(0.828 0.189 84.429);
  --chart-5: oklch(0.769 0.188 70.08);
  --sidebar: oklch(0.985 0 0);
  --sidebar-foreground: oklch(0.145 0 0);
  --sidebar-primary: oklch(0.205 0 0);
  --sidebar-primary-foreground: oklch(0.985 0 0);
  --sidebar-accent: oklch(0.97 0 0);
  --sidebar-accent-foreground: oklch(0.205 0 0);
  --sidebar-border: oklch(0.922 0 0);
  --sidebar-ring: oklch(0.708 0 0);
}

/* ── Linen (light-warm, amber accent) ───────────────────────────── */
.linen {
  --color-bg: #fdf6e3;
  --color-bg-secondary: #f5ead0;
  --color-bg-tertiary: #e8ddd0;
  --color-text: #3b2a1a;
  --color-text-secondary: #7a6550;
  --color-border: #d4c4a8;
  --color-primary: #c27835;
  --color-primary-hover: #a86428;
  --color-danger: #dc2626;
  --color-success: #16a34a;
  --color-sidebar: #f5ead0;
  --color-selected: #fef3c7;
  --color-hover: #f0e4cc;
  --color-unread: #3b2a1a;
  --radius: 0.625rem;
  --background: oklch(0.975 0.018 78);
  --foreground: oklch(0.28 0.04 65);
  --card: oklch(0.965 0.018 78);
  --card-foreground: oklch(0.28 0.04 65);
  --popover: oklch(0.975 0.018 78);
  --popover-foreground: oklch(0.28 0.04 65);
  --primary: oklch(0.62 0.13 68);
  --primary-foreground: oklch(0.99 0 0);
  --secondary: oklch(0.91 0.025 78);
  --secondary-foreground: oklch(0.28 0.04 65);
  --muted: oklch(0.91 0.025 78);
  --muted-foreground: oklch(0.52 0.04 65);
  --accent: oklch(0.91 0.025 78);
  --accent-foreground: oklch(0.28 0.04 65);
  --destructive: oklch(0.577 0.245 27.325);
  --border: oklch(0.87 0.025 78);
  --input: oklch(0.87 0.025 78);
  --ring: oklch(0.62 0.13 68);
  --sidebar: oklch(0.96 0.018 78);
  --sidebar-foreground: oklch(0.28 0.04 65);
  --sidebar-primary: oklch(0.62 0.13 68);
  --sidebar-primary-foreground: oklch(0.99 0 0);
  --sidebar-accent: oklch(0.88 0.022 78);
  --sidebar-accent-foreground: oklch(0.28 0.04 65);
  --sidebar-border: oklch(0.87 0.025 78);
  --sidebar-ring: oklch(0.62 0.13 68);
}

/* ── Slate (mid, indigo accent) ─────────────────────────────────── */
.slate {
  --color-bg: #1e293b;
  --color-bg-secondary: #273548;
  --color-bg-tertiary: #334155;
  --color-text: #e2e8f0;
  --color-text-secondary: #94a3b8;
  --color-border: #334155;
  --color-primary: #818cf8;
  --color-primary-hover: #6366f1;
  --color-danger: #f87171;
  --color-success: #4ade80;
  --color-sidebar: #0f172a;
  --color-selected: #1e3a5f;
  --color-hover: #273548;
  --color-unread: #f1f5f9;
  --radius: 0.625rem;
  --background: oklch(0.22 0.022 258);
  --foreground: oklch(0.88 0.012 240);
  --card: oklch(0.27 0.024 258);
  --card-foreground: oklch(0.88 0.012 240);
  --popover: oklch(0.27 0.024 258);
  --popover-foreground: oklch(0.88 0.012 240);
  --primary: oklch(0.69 0.18 272);
  --primary-foreground: oklch(0.15 0.02 258);
  --secondary: oklch(0.3 0.025 258);
  --secondary-foreground: oklch(0.88 0.012 240);
  --muted: oklch(0.3 0.025 258);
  --muted-foreground: oklch(0.64 0.012 240);
  --accent: oklch(0.3 0.025 258);
  --accent-foreground: oklch(0.88 0.012 240);
  --destructive: oklch(0.67 0.2 27);
  --border: oklch(1 0 0 / 10%);
  --input: oklch(1 0 0 / 15%);
  --ring: oklch(0.69 0.18 272);
  --sidebar: oklch(0.16 0.02 258);
  --sidebar-foreground: oklch(0.88 0.012 240);
  --sidebar-primary: oklch(0.69 0.18 272);
  --sidebar-primary-foreground: oklch(0.15 0.02 258);
  --sidebar-accent: oklch(0.3 0.025 258);
  --sidebar-accent-foreground: oklch(0.88 0.012 240);
  --sidebar-border: oklch(1 0 0 / 10%);
  --sidebar-ring: oklch(0.69 0.18 272);
}

/* ── Dusk (mid-warm, amber-rose accent) ─────────────────────────── */
.dusk {
  --color-bg: #2a1f1a;
  --color-bg-secondary: #342820;
  --color-bg-tertiary: #4a3728;
  --color-text: #e8d5c4;
  --color-text-secondary: #b09070;
  --color-border: #4a3728;
  --color-primary: #e07b4f;
  --color-primary-hover: #c96638;
  --color-danger: #f87171;
  --color-success: #4ade80;
  --color-sidebar: #1e1510;
  --color-selected: #4a2e1a;
  --color-hover: #342820;
  --color-unread: #f0e0d0;
  --radius: 0.625rem;
  --background: oklch(0.19 0.03 50);
  --foreground: oklch(0.87 0.022 55);
  --card: oklch(0.23 0.03 50);
  --card-foreground: oklch(0.87 0.022 55);
  --popover: oklch(0.23 0.03 50);
  --popover-foreground: oklch(0.87 0.022 55);
  --primary: oklch(0.68 0.18 46);
  --primary-foreground: oklch(0.98 0 0);
  --secondary: oklch(0.27 0.03 50);
  --secondary-foreground: oklch(0.87 0.022 55);
  --muted: oklch(0.27 0.03 50);
  --muted-foreground: oklch(0.63 0.025 52);
  --accent: oklch(0.27 0.03 50);
  --accent-foreground: oklch(0.87 0.022 55);
  --destructive: oklch(0.67 0.2 27);
  --border: oklch(1 0 0 / 10%);
  --input: oklch(1 0 0 / 15%);
  --ring: oklch(0.68 0.18 46);
  --sidebar: oklch(0.14 0.03 50);
  --sidebar-foreground: oklch(0.87 0.022 55);
  --sidebar-primary: oklch(0.68 0.18 46);
  --sidebar-primary-foreground: oklch(0.98 0 0);
  --sidebar-accent: oklch(0.27 0.03 50);
  --sidebar-accent-foreground: oklch(0.87 0.022 55);
  --sidebar-border: oklch(1 0 0 / 10%);
  --sidebar-ring: oklch(0.68 0.18 46);
}

/* ── Midnight (dark, blue accent) ───────────────────────────────── */
.midnight {
  --color-bg: #111827;
  --color-bg-secondary: #1f2937;
  --color-bg-tertiary: #374151;
  --color-text: #f9fafb;
  --color-text-secondary: #9ca3af;
  --color-border: #374151;
  --color-primary: #3b82f6;
  --color-primary-hover: #2563eb;
  --color-danger: #ef4444;
  --color-success: #22c55e;
  --color-sidebar: #0f172a;
  --color-selected: #1e3a5f;
  --color-hover: #1f2937;
  --color-unread: #f9fafb;
  --radius: 0.625rem;
  --background: oklch(0.145 0 0);
  --foreground: oklch(0.985 0 0);
  --card: oklch(0.205 0 0);
  --card-foreground: oklch(0.985 0 0);
  --popover: oklch(0.205 0 0);
  --popover-foreground: oklch(0.985 0 0);
  --primary: oklch(0.922 0 0);
  --primary-foreground: oklch(0.205 0 0);
  --secondary: oklch(0.269 0 0);
  --secondary-foreground: oklch(0.985 0 0);
  --muted: oklch(0.269 0 0);
  --muted-foreground: oklch(0.708 0 0);
  --accent: oklch(0.269 0 0);
  --accent-foreground: oklch(0.985 0 0);
  --destructive: oklch(0.704 0.191 22.216);
  --border: oklch(1 0 0 / 10%);
  --input: oklch(1 0 0 / 15%);
  --ring: oklch(0.556 0 0);
  --chart-1: oklch(0.488 0.243 264.376);
  --chart-2: oklch(0.696 0.17 162.48);
  --chart-3: oklch(0.769 0.188 70.08);
  --chart-4: oklch(0.627 0.265 303.9);
  --chart-5: oklch(0.645 0.246 16.439);
  --sidebar: oklch(0.205 0 0);
  --sidebar-foreground: oklch(0.985 0 0);
  --sidebar-primary: oklch(0.488 0.243 264.376);
  --sidebar-primary-foreground: oklch(0.985 0 0);
  --sidebar-accent: oklch(0.269 0 0);
  --sidebar-accent-foreground: oklch(0.985 0 0);
  --sidebar-border: oklch(1 0 0 / 10%);
  --sidebar-ring: oklch(0.556 0 0);
}

/* ── Forest (dark-green, sage accent) ───────────────────────────── */
.forest {
  --color-bg: #0d1f0d;
  --color-bg-secondary: #142814;
  --color-bg-tertiary: #1a3a1a;
  --color-text: #d4e8d4;
  --color-text-secondary: #7aaa7a;
  --color-border: #1a3a1a;
  --color-primary: #4ade80;
  --color-primary-hover: #22c55e;
  --color-danger: #f87171;
  --color-success: #4ade80;
  --color-sidebar: #071407;
  --color-selected: #0a3a0a;
  --color-hover: #142814;
  --color-unread: #e8f5e8;
  --radius: 0.625rem;
  --background: oklch(0.12 0.03 148);
  --foreground: oklch(0.86 0.025 145);
  --card: oklch(0.17 0.032 148);
  --card-foreground: oklch(0.86 0.025 145);
  --popover: oklch(0.17 0.032 148);
  --popover-foreground: oklch(0.86 0.025 145);
  --primary: oklch(0.76 0.19 150);
  --primary-foreground: oklch(0.1 0.03 148);
  --secondary: oklch(0.22 0.032 148);
  --secondary-foreground: oklch(0.86 0.025 145);
  --muted: oklch(0.22 0.032 148);
  --muted-foreground: oklch(0.62 0.022 145);
  --accent: oklch(0.22 0.032 148);
  --accent-foreground: oklch(0.86 0.025 145);
  --destructive: oklch(0.67 0.2 27);
  --border: oklch(1 0 0 / 10%);
  --input: oklch(1 0 0 / 15%);
  --ring: oklch(0.56 0 0);
  --sidebar: oklch(0.08 0.025 148);
  --sidebar-foreground: oklch(0.86 0.025 145);
  --sidebar-primary: oklch(0.76 0.19 150);
  --sidebar-primary-foreground: oklch(0.1 0.03 148);
  --sidebar-accent: oklch(0.22 0.032 148);
  --sidebar-accent-foreground: oklch(0.86 0.025 145);
  --sidebar-border: oklch(1 0 0 / 10%);
  --sidebar-ring: oklch(0.56 0 0);
}
```

Also update the `@custom-variant dark` line — the old `.dark` class no longer exists; the dark palettes are `.midnight` and `.forest`. The shadcn dark variant needs to match both:

```css
@custom-variant dark (&:where(.midnight, .midnight *, .forest, .forest *));
```

**Step 2: Verify visually**

Open the webmail in a browser. The page should still render correctly with the Dawn theme (no visible change yet — that comes in Task 2).

**Step 3: Commit**

```bash
git add webmail/src/index.css
git commit -m "feat(webmail): add 6 named colour palettes to index.css"
```

---

## Task 2: Expand uiStore — Theme type, new views, selectedAccountId

**Files:**
- Modify: `src/stores/uiStore.ts`

**Step 1: Replace the file contents**

```typescript
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

export const useUIStore = create<UIState>((set, get) => ({
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
```

**Step 2: Verify**

Run `npm run build` in `webmail/` to confirm TypeScript compiles without errors.

```bash
cd /workspaces/rest-mail/webmail && npm run build 2>&1 | tail -20
```

Expected: build succeeds (may have warnings about unused `toggleTheme` references — those will be cleaned up in the TopBar task).

**Step 3: Commit**

```bash
git add webmail/src/stores/uiStore.ts
git commit -m "feat(webmail): expand uiStore — 6 themes, settings/accountSettings views"
```

---

## Task 3: Create settingsStore

**Files:**
- Create: `src/stores/settingsStore.ts`

**Step 1: Create the file**

```typescript
import { create } from 'zustand';

export type ReadingPane = 'bottom' | 'right' | 'off';
export type Density = 'comfortable' | 'compact';

interface Settings {
  readingPane: ReadingPane;
  density: Density;
  autoSaveDrafts: boolean;
  desktopNotifications: boolean;
  newMailSound: boolean;
}

interface SettingsState extends Settings {
  update: (patch: Partial<Settings>) => void;
}

function load(): Settings {
  try {
    const raw = localStorage.getItem('restmail-settings');
    if (raw) return { ...defaults(), ...JSON.parse(raw) };
  } catch { /* ignore */ }
  return defaults();
}

function defaults(): Settings {
  return {
    readingPane: 'bottom',
    density: 'comfortable',
    autoSaveDrafts: true,
    desktopNotifications: false,
    newMailSound: false,
  };
}

export const useSettingsStore = create<SettingsState>((set) => ({
  ...load(),

  update: (patch) => set(state => {
    const next = { ...state, ...patch };
    localStorage.setItem('restmail-settings', JSON.stringify({
      readingPane: next.readingPane,
      density: next.density,
      autoSaveDrafts: next.autoSaveDrafts,
      desktopNotifications: next.desktopNotifications,
      newMailSound: next.newMailSound,
    }));
    return next;
  }),
}));
```

**Step 2: Verify**

```bash
cd /workspaces/rest-mail/webmail && npm run build 2>&1 | tail -10
```

Expected: no new errors.

**Step 3: Commit**

```bash
git add webmail/src/stores/settingsStore.ts
git commit -m "feat(webmail): add settingsStore (reading pane, density, notifications)"
```

---

## Task 4: Update TopBar — avatar trigger, expanded theme picker, Settings item

**Files:**
- Modify: `src/components/layout/TopBar.tsx`

This is the largest single-file change. Completely replace the file.

**Step 1: Replace TopBar.tsx**

```typescript
import { useState, useCallback, useRef, useEffect } from 'react';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore, type Theme } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Separator } from '@/components/ui/separator';
import { Search, X, Loader2, Check, PenSquare, RefreshCw, ChevronDown, Palette, Settings } from 'lucide-react';
import { cn } from '@/lib/utils';

// Returns 1-2 uppercase initials from an email address.
function initials(email: string | undefined): string {
  if (!email) return '?';
  const local = email.split('@')[0];
  const parts = local.split(/[._-]/);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return local.slice(0, 2).toUpperCase();
}

type PaletteGroup = { label: string; themes: { id: Theme; name: string; bg: string; accent: string }[] };

const PALETTE_GROUPS: PaletteGroup[] = [
  {
    label: 'Light',
    themes: [
      { id: 'dawn',   name: 'Dawn',   bg: '#ffffff', accent: '#2563eb' },
      { id: 'linen',  name: 'Linen',  bg: '#fdf6e3', accent: '#c27835' },
    ],
  },
  {
    label: 'Mid',
    themes: [
      { id: 'slate',  name: 'Slate',  bg: '#1e293b', accent: '#818cf8' },
      { id: 'dusk',   name: 'Dusk',   bg: '#2a1f1a', accent: '#e07b4f' },
    ],
  },
  {
    label: 'Dark',
    themes: [
      { id: 'midnight', name: 'Midnight', bg: '#111827', accent: '#3b82f6' },
      { id: 'forest',   name: 'Forest',   bg: '#0d1f0d', accent: '#4ade80' },
    ],
  },
];

export function TopBar() {
  const { user, logout } = useAuthStore();
  const { startCompose, theme, setTheme, setView } = useUIStore();
  const { refresh, searchMessages, clearSearch, isSearching, searchQuery } = useMailStore();

  const [inputValue, setInputValue] = useState('');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!searchQuery && inputValue) setInputValue('');
  }, [searchQuery]); // eslint-disable-line react-hooks/exhaustive-deps

  const debouncedSearch = useCallback((value: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      value.trim() ? searchMessages(value) : clearSearch();
    }, 300);
  }, [searchMessages, clearSearch]);

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setInputValue(e.target.value);
    debouncedSearch(e.target.value);
  };

  const handleClearSearch = () => {
    setInputValue('');
    clearSearch();
    inputRef.current?.focus();
  };

  return (
    <>
      <div className="flex items-center justify-between gap-2 px-4 py-2 bg-background">
        {/* Left: actions */}
        <div className="flex items-center gap-2 shrink-0">
          <Button size="sm" onClick={() => startCompose()}>
            <PenSquare className="w-4 h-4 mr-1" />
            Compose
          </Button>
          <Button variant="outline" size="sm" className="hidden sm:inline-flex" onClick={() => refresh()}>
            <RefreshCw className="w-4 h-4 mr-1" />
            Get Mail
          </Button>
        </div>

        {/* Center: search */}
        <div className="flex-1 max-w-md min-w-0 hidden sm:block">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              ref={inputRef}
              type="text"
              placeholder="Search messages..."
              value={inputValue}
              onChange={handleInputChange}
              onKeyDown={e => e.key === 'Escape' && handleClearSearch()}
              className="pl-9 pr-9 h-8"
            />
            {isSearching
              ? <Loader2 className="absolute right-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground animate-spin" />
              : inputValue
                ? <button onClick={handleClearSearch} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground">
                    <X className="h-4 w-4" />
                  </button>
                : null}
          </div>
        </div>

        {/* Right: user menu — avatar + chevron trigger */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="shrink-0 flex items-center gap-1.5 px-2">
              <span className="w-7 h-7 rounded-full bg-primary text-primary-foreground flex items-center justify-center text-xs font-semibold select-none">
                {initials(user?.email)}
              </span>
              <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>

          <DropdownMenuContent align="end" className="w-60">
            <DropdownMenuLabel className="font-normal">
              <p className="text-sm font-medium truncate">{user?.email}</p>
              <p className="text-xs text-muted-foreground">Signed in</p>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />

            {/* Settings */}
            <DropdownMenuItem onClick={() => setView('settings')}>
              <Settings className="w-4 h-4 mr-2" />
              Settings
            </DropdownMenuItem>

            {/* Theme picker */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <Palette className="w-4 h-4 mr-2" />
                Theme
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-52 p-2">
                {PALETTE_GROUPS.map((group, gi) => (
                  <div key={group.label}>
                    {gi > 0 && <div className="my-1.5 border-t border-border" />}
                    <p className="px-1 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                      {group.label}
                    </p>
                    {group.themes.map(p => (
                      <button
                        key={p.id}
                        onClick={() => setTheme(p.id)}
                        className={cn(
                          "w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md text-sm transition-colors",
                          "hover:bg-accent hover:text-accent-foreground",
                          theme === p.id && "bg-accent text-accent-foreground font-medium"
                        )}
                      >
                        {/* Swatch: split circle showing bg + accent */}
                        <span className="relative w-5 h-5 rounded-full border border-border overflow-hidden shrink-0">
                          <span className="absolute inset-0 left-0 w-1/2" style={{ background: p.bg }} />
                          <span className="absolute inset-0 left-1/2" style={{ background: p.accent }} />
                        </span>
                        {p.name}
                        {theme === p.id && <Check className="w-3.5 h-3.5 ml-auto" />}
                      </button>
                    ))}
                  </div>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>

            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setView('accountDetails')}>
              Account Details
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={logout} className="text-destructive focus:text-destructive">
              Logout
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <Separator />
    </>
  );
}
```

**Step 2: Verify in browser**

- Click the avatar+chevron — dropdown opens
- Theme sub-menu shows 3 groups with swatch chips
- Clicking a theme changes the colour palette immediately
- Settings item appears above Theme

**Step 3: Commit**

```bash
git add webmail/src/components/layout/TopBar.tsx
git commit -m "feat(webmail): avatar trigger, 6-palette theme picker, Settings item in TopBar"
```

---

## Task 5: Add gear icon to Sidebar account rows

**Files:**
- Modify: `src/components/layout/Sidebar.tsx`

**Step 1: Import Settings from lucide and setSelectedAccountId from uiStore**

At the top of Sidebar.tsx, the imports already include `Settings2`. Add `Settings` as well (or just use `Settings2` as the gear icon — it has a rounder look). Also import `setSelectedAccountId` from uiStore.

Change the uiStore destructure line from:
```typescript
const { sidebarCollapsed, toggleAccountCollapsed, setView } = useUIStore();
```
to:
```typescript
const { sidebarCollapsed, toggleAccountCollapsed, setView, setSelectedAccountId } = useUIStore();
```

**Step 2: Wrap each account button in a hover group and add the gear icon**

Find this block (around line 154–166):

```typescript
{accounts.map(account => (
  <div key={account.id}>
    <button
      onClick={() => handleAccountClick(account.id)}
      className={cn(
        "w-full text-left px-3 py-1.5 text-sm flex items-center gap-1.5",
        "hover:bg-sidebar-accent transition-colors text-sidebar-foreground"
      )}
    >
      {isExpanded(account.id) ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
      <span className="truncate flex-1 font-medium">{account.address}</span>
      {!isExpanded(account.id) && <InboxBadge />}
    </button>
```

Replace with:

```typescript
{accounts.map(account => (
  <div key={account.id}>
    <div className="group flex items-center hover:bg-sidebar-accent transition-colors">
      <button
        onClick={() => handleAccountClick(account.id)}
        className="flex-1 text-left px-3 py-1.5 text-sm flex items-center gap-1.5 text-sidebar-foreground min-w-0"
      >
        {isExpanded(account.id) ? <ChevronDown className="w-3 h-3 shrink-0" /> : <ChevronRight className="w-3 h-3 shrink-0" />}
        <span className="truncate flex-1 font-medium">{account.address}</span>
        {!isExpanded(account.id) && <InboxBadge />}
      </button>
      <button
        title="Account settings"
        onClick={() => {
          setSelectedAccountId(account.id);
          setView('accountSettings');
          setMobileOpen(false);
        }}
        className="opacity-0 group-hover:opacity-100 transition-opacity p-1.5 mr-1 rounded text-muted-foreground hover:text-foreground"
      >
        <Settings2 className="w-3.5 h-3.5" />
      </button>
    </div>
```

**Step 3: Verify in browser**

Hover over an account row — a subtle gear icon appears on the right. Clicking it should currently show nothing (the view 'accountSettings' is not wired yet — that comes in Task 7).

**Step 4: Commit**

```bash
git add webmail/src/components/layout/Sidebar.tsx
git commit -m "feat(webmail): add per-account gear icon to sidebar"
```

---

## Task 6: Create SettingsView (General / Accounts / Notifications tabs)

**Files:**
- Create: `src/components/settings/SettingsView.tsx`

This is a full-page panel with three tabs. Tabs are implemented with local state (no external tab library needed).

**Step 1: Create the file**

```typescript
import { useState } from 'react';
import { useSettingsStore, type ReadingPane, type Density } from '@/stores/settingsStore';
import { useMailStore } from '@/stores/mailStore';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { VacationView } from '@/components/settings/VacationView';
import { Settings, Bell, Users, LayoutPanelTop, MonitorCheck, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

type Tab = 'general' | 'accounts' | 'notifications';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'general',       label: 'General',       icon: <Settings className="w-4 h-4" /> },
  { id: 'accounts',      label: 'Accounts',      icon: <Users className="w-4 h-4" /> },
  { id: 'notifications', label: 'Notifications', icon: <Bell className="w-4 h-4" /> },
];

export function SettingsView() {
  const [tab, setTab] = useState<Tab>('general');

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-5 pb-0 border-b border-border">
        <h2 className="text-lg font-semibold mb-4">Settings</h2>
        <div className="flex gap-0">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={cn(
                "flex items-center gap-1.5 px-4 py-2 text-sm border-b-2 transition-colors -mb-px",
                tab === t.id
                  ? "border-primary text-foreground font-medium"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              )}
            >
              {t.icon}
              {t.label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-auto p-6">
        {tab === 'general'       && <GeneralTab />}
        {tab === 'accounts'      && <AccountsTab />}
        {tab === 'notifications' && <NotificationsTab />}
      </div>
    </div>
  );
}

// ── General tab ───────────────────────────────────────────────────

function GeneralTab() {
  const { readingPane, density, autoSaveDrafts, update } = useSettingsStore();

  return (
    <div className="max-w-2xl space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <LayoutPanelTop className="w-4 h-4" /> Reading Pane
          </CardTitle>
          <CardDescription>Where the message viewer appears.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-3">
          {(['bottom', 'right', 'off'] as ReadingPane[]).map(v => (
            <button
              key={v}
              onClick={() => update({ readingPane: v })}
              className={cn(
                "px-4 py-2 rounded-md border text-sm capitalize transition-colors",
                readingPane === v
                  ? "border-primary bg-primary/10 text-primary font-medium"
                  : "border-border hover:bg-accent"
              )}
            >
              {v}
            </button>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <MonitorCheck className="w-4 h-4" /> Display Density
          </CardTitle>
          <CardDescription>Controls message list row spacing.</CardDescription>
        </CardHeader>
        <CardContent className="flex gap-3">
          {(['comfortable', 'compact'] as Density[]).map(v => (
            <button
              key={v}
              onClick={() => update({ density: v })}
              className={cn(
                "px-4 py-2 rounded-md border text-sm capitalize transition-colors",
                density === v
                  ? "border-primary bg-primary/10 text-primary font-medium"
                  : "border-border hover:bg-accent"
              )}
            >
              {v}
            </button>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Composer</CardTitle>
        </CardHeader>
        <CardContent>
          <Toggle
            label="Auto-save drafts"
            description="Automatically save drafts while composing."
            checked={autoSaveDrafts}
            onChange={v => update({ autoSaveDrafts: v })}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Keyboard Shortcuts</CardTitle>
          <CardDescription>Available in the message list.</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="text-sm space-y-1.5">
            {[
              ['c', 'Compose new message'],
              ['r', 'Reply to selected message'],
              ['f', 'Forward selected message'],
              ['Backspace / Delete', 'Move to trash'],
              ['Esc', 'Clear search / close compose'],
            ].map(([key, desc]) => (
              <div key={key} className="flex items-center gap-3">
                <kbd className="px-2 py-0.5 bg-muted text-muted-foreground rounded text-xs font-mono whitespace-nowrap">{key}</kbd>
                <span className="text-muted-foreground">{desc}</span>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

// ── Accounts tab ──────────────────────────────────────────────────

function AccountsTab() {
  const { accounts, activeAccountId, setActiveAccount } = useMailStore();
  const { user } = useAuthStore();
  const { setView, setSelectedAccountId } = useUIStore();
  const [accountTab, setAccountTab] = useState<'vacation' | 'danger'>('vacation');

  const selected = accounts.find(a => a.id === activeAccountId) ?? accounts[0];

  function openFullSettings(id: number) {
    setSelectedAccountId(id);
    setView('accountSettings');
  }

  if (!selected) return <p className="text-muted-foreground text-sm">No accounts found.</p>;

  const isPrimary = selected.address === user?.email;

  return (
    <div className="max-w-2xl space-y-4">
      {/* Account selector */}
      <div className="flex flex-wrap gap-2">
        {accounts.map(a => (
          <button
            key={a.id}
            onClick={() => setActiveAccount(a.id)}
            className={cn(
              "px-3 py-1.5 rounded-full border text-sm transition-colors",
              a.id === selected.id
                ? "border-primary bg-primary/10 text-primary font-medium"
                : "border-border hover:bg-accent text-muted-foreground"
            )}
          >
            {a.address}
            {a.address === user?.email && <span className="ml-1.5 text-xs opacity-60">(primary)</span>}
          </button>
        ))}
      </div>

      {/* Sub-tabs */}
      <div className="flex gap-0 border-b border-border">
        {(['vacation', 'danger'] as const).map(t => (
          <button
            key={t}
            onClick={() => setAccountTab(t)}
            className={cn(
              "px-4 py-2 text-sm border-b-2 -mb-px transition-colors capitalize",
              accountTab === t
                ? "border-primary text-foreground font-medium"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {t === 'danger' ? 'Danger Zone' : 'Vacation'}
          </button>
        ))}
      </div>

      {accountTab === 'vacation' && <VacationView />}

      {accountTab === 'danger' && (
        <DangerZone isPrimary={isPrimary} accountId={selected.id} onDeleted={() => openFullSettings(selected.id)} />
      )}

      <p className="text-xs text-muted-foreground pt-2">
        Full account settings:{' '}
        <button className="underline hover:no-underline" onClick={() => openFullSettings(selected.id)}>
          open account settings panel →
        </button>
      </p>
    </div>
  );
}

// ── Notifications tab ─────────────────────────────────────────────

function NotificationsTab() {
  const { desktopNotifications, newMailSound, update } = useSettingsStore();

  return (
    <div className="max-w-2xl">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Notifications</CardTitle>
          <CardDescription>Control how you are alerted to new mail.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Toggle
            label="Desktop notifications"
            description="Show a system notification when new mail arrives."
            checked={desktopNotifications}
            onChange={async (v) => {
              if (v && Notification.permission !== 'granted') {
                const perm = await Notification.requestPermission();
                if (perm !== 'granted') return;
              }
              update({ desktopNotifications: v });
            }}
          />
          <Separator />
          <Toggle
            label="New mail sound"
            description="Play a sound when new mail arrives."
            checked={newMailSound}
            onChange={v => update({ newMailSound: v })}
          />
        </CardContent>
      </Card>
    </div>
  );
}

// ── Danger Zone card (reused in AccountSettings too) ──────────────

export function DangerZone({ isPrimary, accountId, onDeleted }: {
  isPrimary: boolean;
  accountId: number;
  onDeleted?: () => void;
}) {
  const { removeAccount } = useMailStore();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  async function handleRemove() {
    setBusy(true);
    try {
      await removeAccount(accountId);
      onDeleted?.();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <Card className="border-destructive/50">
      <CardHeader>
        <CardTitle className="text-base text-destructive flex items-center gap-2">
          <Trash2 className="w-4 h-4" /> Danger Zone
        </CardTitle>
        <CardDescription>Removing an account only removes it from this webmail session. It does not delete the mailbox.</CardDescription>
      </CardHeader>
      <CardContent>
        {isPrimary ? (
          <p className="text-sm text-muted-foreground">
            You cannot remove your primary account. Log out instead.
          </p>
        ) : confirming ? (
          <div className="flex items-center gap-3">
            <p className="text-sm text-muted-foreground flex-1">Are you sure? This cannot be undone.</p>
            <Button variant="destructive" size="sm" disabled={busy} onClick={handleRemove}>
              {busy ? 'Removing…' : 'Yes, remove'}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>Cancel</Button>
          </div>
        ) : (
          <Button variant="outline" size="sm" className="border-destructive/50 text-destructive hover:bg-destructive hover:text-destructive-foreground" onClick={() => setConfirming(true)}>
            Remove this account from webmail
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

// ── Toggle helper ─────────────────────────────────────────────────

function Toggle({ label, description, checked, onChange }: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div>
        <Label className="font-medium">{label}</Label>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors mt-0.5",
          checked ? "bg-primary" : "bg-muted"
        )}
      >
        <span className={cn(
          "inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform",
          checked ? "translate-x-6" : "translate-x-1"
        )} />
      </button>
    </div>
  );
}
```

**Step 2: Verify build**

```bash
cd /workspaces/rest-mail/webmail && npm run build 2>&1 | tail -15
```

**Step 3: Commit**

```bash
git add webmail/src/components/settings/SettingsView.tsx
git commit -m "feat(webmail): add SettingsView (General/Accounts/Notifications tabs)"
```

---

## Task 7: Create AccountSettingsView (per-account panel from sidebar gear)

**Files:**
- Create: `src/components/account/AccountSettingsView.tsx`

**Step 1: Create the file**

```typescript
import { useState } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { VacationView } from '@/components/settings/VacationView';
import { DangerZone } from '@/components/settings/SettingsView';
import { User, Palmtree, HardDrive, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

type Tab = 'details' | 'vacation' | 'danger';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'details',  label: 'Details',  icon: <User className="w-4 h-4" /> },
  { id: 'vacation', label: 'Vacation', icon: <Palmtree className="w-4 h-4" /> },
  { id: 'danger',   label: 'Danger Zone', icon: <Trash2 className="w-4 h-4" /> },
];

export function AccountSettingsView() {
  const { selectedAccountId } = useUIStore();
  const { accounts, setActiveAccount } = useMailStore();
  const { setView } = useUIStore();
  const { user } = useAuthStore();
  const [tab, setTab] = useState<Tab>('details');

  // Ensure the account we're editing is the active one in mailStore
  // so VacationView reads the right activeAccountId.
  const account = accounts.find(a => a.id === selectedAccountId);

  if (!account) {
    return (
      <div className="p-6 text-muted-foreground text-sm">
        Account not found.{' '}
        <button className="underline" onClick={() => setView('mail')}>Go back</button>
      </div>
    );
  }

  const isPrimary = account.address === user?.email;

  // Activate this account in mailStore so VacationView works
  function handleTabClick(t: Tab) {
    setTab(t);
    if (account) setActiveAccount(account.id);
  }

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-5 pb-0 border-b border-border">
        <p className="text-xs text-muted-foreground mb-0.5">Account settings</p>
        <h2 className="text-lg font-semibold mb-4 truncate">{account.address}</h2>
        <div className="flex gap-0">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => handleTabClick(t.id)}
              className={cn(
                "flex items-center gap-1.5 px-4 py-2 text-sm border-b-2 transition-colors -mb-px",
                tab === t.id
                  ? "border-primary text-foreground font-medium"
                  : "border-transparent text-muted-foreground hover:text-foreground",
                t.id === 'danger' && tab !== 'danger' && "hover:text-destructive"
              )}
            >
              {t.icon}
              {t.label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-auto p-6">
        {tab === 'details'  && <DetailsTab account={account} isPrimary={isPrimary} />}
        {tab === 'vacation' && <VacationView />}
        {tab === 'danger'   && (
          <div className="max-w-2xl">
            <DangerZone
              isPrimary={isPrimary}
              accountId={account.id}
              onDeleted={() => setView('mail')}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function DetailsTab({ account, isPrimary }: { account: { id: number; address: string }; isPrimary: boolean }) {
  return (
    <div className="max-w-2xl space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <User className="w-4 h-4" /> Account Details
          </CardTitle>
          {isPrimary && (
            <CardDescription>This is your primary account.</CardDescription>
          )}
        </CardHeader>
        <CardContent className="space-y-3">
          <div>
            <p className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Email address</p>
            <p className="text-sm font-mono">{account.address}</p>
          </div>
          <div>
            <p className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Account ID</p>
            <p className="text-sm font-mono text-muted-foreground">#{account.id}</p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <HardDrive className="w-4 h-4" /> Storage Quota
          </CardTitle>
          <CardDescription>Quota information is available via the API.</CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Check your quota via <span className="font-mono text-xs bg-muted px-1 py-0.5 rounded">GET /api/v1/mailboxes/:id</span>
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
```

**Step 2: Verify build**

```bash
cd /workspaces/rest-mail/webmail && npm run build 2>&1 | tail -15
```

**Step 3: Commit**

```bash
git add webmail/src/components/account/AccountSettingsView.tsx
git commit -m "feat(webmail): add AccountSettingsView with Details/Vacation/DangerZone tabs"
```

---

## Task 8: Wire new views in App.tsx + apply reading-pane and density

**Files:**
- Modify: `src/App.tsx`

**Step 1: Add the new imports and update App.tsx**

Add after existing imports:
```typescript
import { SettingsView } from '@/components/settings/SettingsView';
import { AccountSettingsView } from '@/components/account/AccountSettingsView';
import { useSettingsStore } from '@/stores/settingsStore';
```

In the `App` function body, add:
```typescript
const { readingPane, density } = useSettingsStore();
```

In the content area, add two new view routes after the existing ones:
```typescript
{view === 'settings'        && <div className="h-full animate-fade-in"><SettingsView /></div>}
{view === 'accountSettings' && <div className="h-full animate-fade-in"><AccountSettingsView /></div>}
```

Update `MailView` to accept and use `readingPane` and `density` props. Replace the `MailView` call and function:

In the JSX where MailView is called:
```typescript
{view === 'mail' && <div className="h-full animate-fade-in"><MailView readingPane={readingPane} density={density} /></div>}
```

Replace the `MailView` function definition at the bottom:
```typescript
import type { ReadingPane, Density } from '@/stores/settingsStore';

function MailView({ readingPane, density }: { readingPane: ReadingPane; density: Density }) {
  // density is passed via context — MessageList reads from settingsStore directly.
  // readingPane controls layout here.
  if (readingPane === 'right') {
    return (
      <div className="h-full flex flex-row">
        <div className="w-2/5 min-w-0 border-r border-border overflow-hidden">
          <MessageList />
        </div>
        <div className="flex-1 min-w-0 overflow-hidden">
          <MessageViewer />
        </div>
      </div>
    );
  }

  if (readingPane === 'off') {
    return (
      <div className="h-full flex flex-col">
        <MessageList />
      </div>
    );
  }

  // Default: bottom
  return (
    <div className="h-full flex flex-col">
      <div className="flex-1 min-h-0 border-b border-border">
        <MessageList />
      </div>
      <Separator />
      <div className="flex-1 min-h-0">
        <MessageViewer />
      </div>
    </div>
  );
}
```

**Note:** `density` is stored in settingsStore and read directly by MessageList (no prop drilling needed — it already has access to all stores). If you want to wire density into the message row height, open `src/components/mail/MessageList.tsx` and import `useSettingsStore`, then use `density === 'compact' ? 'py-1' : 'py-2'` on the row class.

**Step 2: Verify everything in browser**

- User dropdown → Settings → Settings page opens with tabs
- Sidebar gear icon on account → Account settings panel opens
- Reading pane setting changes the mail view layout
- Themes all apply correctly
- Danger zone shows disabled state for primary account, confirm dialog for others

**Step 3: Final build check**

```bash
cd /workspaces/rest-mail/webmail && npm run build 2>&1 | tail -20
```

Expected: clean build, no TypeScript errors.

**Step 4: Commit**

```bash
git add webmail/src/App.tsx
git commit -m "feat(webmail): wire settings/accountSettings views, reading pane layout in App.tsx"
```

---

## Task 9 (optional): Apply density to MessageList rows

**Files:**
- Modify: `src/components/mail/MessageList.tsx`

If the density setting should visually change the message list, open MessageList.tsx and find the row element. Import `useSettingsStore` and change the row padding class to be conditional:

```typescript
import { useSettingsStore } from '@/stores/settingsStore';
// ...
const { density } = useSettingsStore();
// ...
// On the row button/div:
className={cn("...", density === 'compact' ? 'py-1' : 'py-2.5', ...)}
```

**Commit:**

```bash
git add webmail/src/components/mail/MessageList.tsx
git commit -m "feat(webmail): apply density setting to message list row spacing"
```

---

## Final verification checklist

- [ ] All 6 themes apply and persist across page reload
- [ ] Avatar+chevron trigger is visible and opens the dropdown
- [ ] Theme sub-menu shows 3 groups (Light/Mid/Dark) with swatch chips
- [ ] Settings menu item navigates to the settings page
- [ ] Settings page has General / Accounts / Notifications tabs
- [ ] Reading pane toggle changes the mail view layout (bottom/right/off)
- [ ] Gear icon appears on hover of each account in the sidebar
- [ ] Clicking gear icon opens AccountSettingsView for that account
- [ ] Vacation tab in account settings works (loads/saves)
- [ ] Danger zone: primary account button is disabled with explanation
- [ ] Danger zone: secondary account shows confirm dialog, then removes account
- [ ] `npm run build` passes with no TypeScript errors
