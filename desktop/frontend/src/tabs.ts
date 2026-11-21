import { useSyncExternalStore } from 'react';
import type { ComposeState } from '@webmail/stores/uiStore';

export interface ComposeTab {
  id: string;
  initialState: ComposeState;
  from: string;
  title: string;
  dirty: boolean;
  attachments: HeldAttachment[];
}
export interface HeldAttachment { id: string; filename: string; size: number }
let sequence = 0;
let snapshot: { active: string; tabs: ComposeTab[] } = { active: 'mail', tabs: [] };
const listeners = new Set<() => void>();
const publish = (next: typeof snapshot) => { snapshot = next; listeners.forEach(listener => listener()); };
export const tabs = {
  get: () => snapshot,
  subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
  open: (initialState: ComposeState, from = '') => {
    const id = `compose-${++sequence}`;
    publish({ active: id, tabs: [...snapshot.tabs, { id, initialState, from, title: initialState.subject || 'New Message', dirty: false, attachments: [] }] });
    return id;
  },
  select: (id: string) => { if (id === 'mail' || snapshot.tabs.some(tab => tab.id === id)) publish({ ...snapshot, active: id }); },
  update: (id: string, status: { subject: string; dirty: boolean }) => {
    const title = status.subject || 'New Message';
    if (snapshot.tabs.some(tab => tab.id === id && (tab.title !== title || tab.dirty !== status.dirty))) publish({ ...snapshot, tabs: snapshot.tabs.map(tab => tab.id === id ? { ...tab, title, dirty: status.dirty } : tab) });
  },
  close: (id: string) => {
    const index = snapshot.tabs.findIndex(tab => tab.id === id);
    if (index < 0) return;
    const remaining = snapshot.tabs.filter(tab => tab.id !== id);
    publish({ active: snapshot.active === id ? remaining[Math.max(0, index - 1)]?.id ?? 'mail' : snapshot.active, tabs: remaining });
  },
  cycle: (backwards: boolean) => {
    const ids = ['mail', ...snapshot.tabs.map(tab => tab.id)];
    tabs.select(ids[(ids.indexOf(snapshot.active) + (backwards ? ids.length - 1 : 1)) % ids.length]);
  },
  clear: () => publish({ active: 'mail', tabs: [] }),
  attach: (id: string, attachments: HeldAttachment[], from: string) => {
    const existing = snapshot.tabs.find(tab => tab.id === id);
    const next = existing ? snapshot.tabs.map(tab => tab.id === id ? { ...tab, attachments } : tab) : [...snapshot.tabs, { id, attachments, from, title: 'New Message', dirty: true, initialState: { to: '', cc: '', bcc: '', subject: '' } }];
    publish({ active: id, tabs: next });
  },
  list: () => [{ id: 'mail', title: 'Mail', active: snapshot.active === 'mail', dirty: false }, ...snapshot.tabs.map(({ id, title, dirty }) => ({ id, title, dirty, active: snapshot.active === id }))],
};
export const useDesktopTabs = () => useSyncExternalStore(tabs.subscribe, tabs.get);
