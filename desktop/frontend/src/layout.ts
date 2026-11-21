import { useSyncExternalStore } from 'react';
import { useMailStore } from '@webmail/stores/mailStore';

export type MailLayout = 'columns' | 'stacked';
export interface LayoutPreferences {
  layout: MailLayout;
  folders: boolean;
  reader: boolean;
  columnsWidth: number;
  stackedHeight: number;
}

const defaults: LayoutPreferences = { layout: 'columns', folders: true, reader: true, columnsWidth: 340, stackedHeight: 230 };
let state = { ...defaults };
let storageKey = '';
const listeners = new Set<() => void>();

export function normalizeLayout(value: unknown): LayoutPreferences {
  const saved = value && typeof value === 'object' ? value as Partial<LayoutPreferences> : {};
  return {
    layout: saved.layout === 'stacked' ? 'stacked' : 'columns',
    folders: typeof saved.folders === 'boolean' ? saved.folders : true,
    reader: typeof saved.reader === 'boolean' ? saved.reader : true,
    columnsWidth: typeof saved.columnsWidth === 'number' && Number.isFinite(saved.columnsWidth) ? Math.max(260, Math.min(600, saved.columnsWidth)) : defaults.columnsWidth,
    stackedHeight: typeof saved.stackedHeight === 'number' && Number.isFinite(saved.stackedHeight) ? Math.max(150, Math.min(900, saved.stackedHeight)) : defaults.stackedHeight,
  };
}

function publish() { listeners.forEach(listener => listener()); }
function update(patch: Partial<LayoutPreferences>) {
  const next = normalizeLayout({ ...state, ...patch });
  if (JSON.stringify(next) === JSON.stringify(state)) return;
  state = next;
  try { if (storageKey) localStorage.setItem(storageKey, JSON.stringify(state)); } catch {}
  publish();
}

export const layoutPreferences = {
  get: () => state,
  subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
  restore: (server: string, email: string) => {
    storageKey = email ? `restmail:desktop-layout:${encodeURIComponent(server)}:${encodeURIComponent(email)}` : '';
    try { state = normalizeLayout(storageKey ? JSON.parse(localStorage.getItem(storageKey) || 'null') : null); }
    catch { state = { ...defaults }; }
    publish();
  },
  select: (layout: MailLayout) => update({ layout }),
  toggleFolders: () => update({ folders: !state.folders }),
  toggleReader: () => {
    if (state.reader) useMailStore.setState({ selectedMessageId: null, selectedMessage: null });
    update({ reader: !state.reader });
  },
  resize: (size: number) => update(state.layout === 'columns' ? { columnsWidth: Math.round(size) } : { stackedHeight: Math.round(size) }),
};

export function useLayoutPreferences() { return useSyncExternalStore(layoutPreferences.subscribe, layoutPreferences.get); }
