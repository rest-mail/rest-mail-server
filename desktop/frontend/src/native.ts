import { Call } from '@wailsio/runtime';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore, type ComposeState } from '@webmail/stores/uiStore';
import type { SSEEvent } from '@webmail/lib/mailStream';
import type { RuntimeEvents } from './session';
import { toast } from 'sonner';
import { tabs, type HeldAttachment } from './tabs';
import { layoutPreferences } from './layout';

const invoke = <Result,>(method: string, ...args: unknown[]) => Call.ByName(`main.Native.${method}`, ...args) as Promise<Result>;
const report = (error: unknown) => toast.error(error instanceof Error ? error.message : 'Native action failed');

export const native = {
  openCompose: (state?: ComposeState) => {
    const mail = useMailStore.getState();
    tabs.open(state ?? { to: '', cc: '', bcc: '', subject: '' }, mail.accounts.find(account => account.id === mail.activeAccountId)?.address ?? mail.accounts[0]?.address ?? '');
  },
  syncCompose: (name: string, state: { dirty: boolean; canSave: boolean; busy: boolean; subject: string }) => { void invoke('SyncCompose', name, state).catch(report); },
  requestClose: (name: string) => { void invoke('RequestClose', name).catch(report); },
  cancelClose: (name: string) => { void invoke('CancelClose', name).catch(report); },
  completeCompose: (name: string, sent = false) => { void invoke('CompleteCompose', name, sent).catch(report); },
  pickAttachments: () => { void invoke('PickAttachments').catch(report); },
  removeAttachment: (name: string, id: string) => { void invoke<HeldAttachment[]>('RemoveAttachment', name, id).then(files => tabs.attach(name, files, '')).catch(report); },
};

const messageActions = new Map<string, () => void>();
export function registerMessageAction(name: string, action: () => void) {
  messageActions.set(name, action);
  return () => { if (messageActions.get(name) === action) messageActions.delete(name); };
}

export function isEditing(element: Element | null): boolean {
  return !!element?.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]');
}

export function inboxUnread(folders: { name: string; unread: number }[]): number {
  return folders.filter(folder => folder.name.toUpperCase() === 'INBOX').reduce((total, folder) => total + Math.max(0, folder.unread), 0);
}

export function notificationMetadata(event: SSEEvent) {
  if (event.type !== 'new_message') return null;
  const { message_id, sender, subject, folder } = event.data;
  const id = Number(message_id);
  if (!Number.isSafeInteger(id) || id <= 0 || typeof folder === 'string' && folder.toUpperCase() !== 'INBOX') return null;
  return { id, sender: typeof sender === 'string' ? sender : '', subject: typeof subject === 'string' ? subject : '' };
}

export function handleNativeMailEvent(event: SSEEvent) {
  const metadata = notificationMetadata(event);
  if (metadata) void invoke('NewMail', metadata.id, metadata.sender, metadata.subject).catch(report);
}

export function runNativeCommand(action: string, windowName = 'main') {
  if (!document.hasFocus()) return;
  if (action === 'close-tab') { if (tabs.get().active !== 'mail') native.requestClose(tabs.get().active); return; }
  if (isEditing(document.activeElement)) return;
  const auth = useAuthStore.getState();
  if (!auth.isAuthenticated || auth.setupRequired.length) return;
  const mail = useMailStore.getState();
  if (action === 'new-message') { native.openCompose(); return; }
  if (windowName !== 'main') return;
  if (action === 'view.layout.columns' || action === 'view.layout.stacked') { layoutPreferences.select(action === 'view.layout.columns' ? 'columns' : 'stacked'); return; }
  if (action === 'view.pane.folders') { layoutPreferences.toggleFolders(); return; }
  if (action === 'view.pane.reader') { layoutPreferences.toggleReader(); return; }
  if (action === 'import-thunderbird') { window.dispatchEvent(new Event('desktop:import-thunderbird')); return; }
  if (action === 'settings') { tabs.select('mail'); useUIStore.getState().setView('settings'); return; }
  if (action === 'refresh') { void mail.refresh().catch(report); return; }
  if (tabs.get().active !== 'mail' || useUIStore.getState().view !== 'mail' || !mail.selectedMessage || mail.loadingMessage || mail.selectedMessage.is_draft) return;
  const names: Record<string, string> = { reply: 'Reply', 'reply-all': 'Reply all', forward: 'Forward', archive: 'Archive', delete: 'Delete', 'toggle-flag': mail.selectedMessage.is_flagged ? 'Unflag' : 'Flag' };
  if (action === 'toggle-read') void mail.markRead(mail.selectedMessage.id, !mail.selectedMessage.is_read).then(() => mail.loadFolders()).catch(report);
  else messageActions.get(names[action])?.();
}

export function installNative(events: RuntimeEvents, windowName: string) {
  let permissionRequested = false;
  let lastUnread = -1;
  let lastState = '';
  const sync = () => {
    const auth = useAuthStore.getState();
    const mail = useMailStore.getState();
    const signedIn = auth.isAuthenticated && auth.setupRequired.length === 0;
    const message = mail.selectedMessage;
    const preferences = layoutPreferences.get();
    const state = { signedIn, editing: isEditing(document.activeElement), viewing: tabs.get().active === 'mail' && useUIStore.getState().view === 'mail', composeTab: tabs.get().active !== 'mail', activeTab: tabs.get().active, messageId: mail.loadingMessage ? 0 : message?.id ?? 0, draft: message?.is_draft ?? false, read: message?.is_read ?? false, flagged: message?.is_flagged ?? false, layout: preferences.layout, folders: preferences.folders, reader: preferences.reader };
    const serialized = JSON.stringify(state);
    if (lastState !== serialized) { lastState = serialized; void invoke('SyncState', windowName, state).catch(report); }
    if (windowName !== 'main') return;
    const unread = signedIn ? inboxUnread(mail.folders) : 0;
    if (unread !== lastUnread) { lastUnread = unread; void invoke('SetUnread', unread).catch(report); }
    if (signedIn && !permissionRequested) { permissionRequested = true; void invoke('RequestNotificationPermission').catch(report); }
    if (!signedIn) permissionRequested = false;
  };
  const unsubscribe = [
    useAuthStore.subscribe(sync), useMailStore.subscribe(sync), useUIStore.subscribe(sync), tabs.subscribe(sync), layoutPreferences.subscribe(sync),
    events.on('native:tab-closed', data => tabs.close(String(data))),
    events.on('native:select-tab', data => tabs.select(String(data))),
    events.on('native:drop-error', data => toast.error(String(data))),
    events.on('native:files-dropped', data => {
      const drop = data as { tab: string; attachments: HeldAttachment[] };
      const mail = useMailStore.getState();
      tabs.attach(drop.tab, drop.attachments, mail.accounts.find(account => account.id === mail.activeAccountId)?.address ?? mail.accounts[0]?.address ?? '');
    }),
    events.on('native:command', data => {
      const command = data as { window: string; action: string };
      if (command.window === windowName) runNativeCommand(command.action, windowName);
    }),
    events.on('native:mail-changed', () => { if (windowName === 'main') void useMailStore.getState().refresh().catch(report); }),
    events.on('native:open-message', async data => {
      if (windowName !== 'main' || !useAuthStore.getState().isAuthenticated) return;
      tabs.select('mail');
      useUIStore.getState().setView('mail');
      await useMailStore.getState().selectMessage(Number(data));
    }),
  ];
  const focusChanged = () => { queueMicrotask(sync); };
  const keyDown = (event: KeyboardEvent) => {
    if (!document.hasFocus() || !useAuthStore.getState().isAuthenticated) return;
    if (event.ctrlKey && event.key === 'Tab') { event.preventDefault(); tabs.cycle(event.shiftKey); }
  };
  window.addEventListener('keydown', keyDown, true);
  document.addEventListener('focusin', focusChanged);
  document.addEventListener('focusout', focusChanged);
  window.addEventListener('focus', sync);
  sync();
  return () => {
    unsubscribe.forEach(stop => stop());
    document.removeEventListener('focusin', focusChanged);
    document.removeEventListener('focusout', focusChanged);
    window.removeEventListener('focus', sync);
    window.removeEventListener('keydown', keyDown, true);
  };
}
