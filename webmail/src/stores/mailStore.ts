import { create } from 'zustand';
import { toast } from 'sonner';
import type { Folder, MessageSummary, MessageDetail, Account } from '../types';
import * as api from '../api/client';
import { threadDueChange } from '../lib/threadDue';
import { threadSnoozeChange } from '../lib/threadSnooze';
import { noWaitingView, waitingPage } from '../lib/threadAwaiting';

interface MailState {
  // Accounts
  accounts: Account[];
  activeAccountId: number | null;

  // Folders
  folders: Folder[];
  activeFolder: string;

  // Messages
  messages: MessageSummary[];
  selectedMessageId: number | null;
  selectedMessage: MessageDetail | null;
  hasMore: boolean;
  cursor: string | null;

  // Search
  searchQuery: string;
  searchResults: MessageSummary[] | null;
  isSearching: boolean;

  // Threads waiting on a reply, across folders; null when a folder is showing
  waitingMessages: MessageSummary[] | null;
  waitingHasMore: boolean;
  waitingCursor: string | null;

  // Loading states
  loadingFolders: boolean;
  loadingMessages: boolean;
  loadingMessage: boolean;

  // Actions
  setActiveAccount: (accountId: number) => void;
  loadAccounts: () => Promise<void>;
  loadFolders: () => Promise<void>;
  selectFolder: (folder: string) => Promise<void>;
  loadMessages: () => Promise<void>;
  loadMoreMessages: () => Promise<void>;
  selectMessage: (msgId: number) => Promise<void>;
  markRead: (msgId: number, read: boolean) => Promise<void>;
  markFlagged: (msgId: number, flagged: boolean) => Promise<void>;
  deleteMsg: (msgId: number) => Promise<void>;
  setThreadDue: (mailboxId: number, threadId: string, due: string | null) => Promise<void>;
  snoozeThread: (mailboxId: number, threadId: string, until: string | null) => Promise<void>;
  applyThreadSnooze: (mailboxId: number, threadId: string, until: string | null) => void;
  removeAccount: (accountId: number) => Promise<void>;
  refresh: () => Promise<void>;
  searchMessages: (query: string) => Promise<void>;
  clearSearch: () => void;
  showWaiting: () => Promise<void>;
  loadMoreWaiting: () => Promise<void>;
  clearWaiting: () => void;
}

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : 'An unexpected error occurred';
}

export const useMailStore = create<MailState>((set, get) => ({
  accounts: [],
  activeAccountId: null,
  folders: [],
  activeFolder: 'INBOX',
  messages: [],
  selectedMessageId: null,
  selectedMessage: null,
  hasMore: false,
  cursor: null,
  searchQuery: '',
  searchResults: null,
  isSearching: false,
  ...noWaitingView,
  loadingFolders: false,
  loadingMessages: false,
  loadingMessage: false,

  setActiveAccount: (accountId) => {
    set({ activeAccountId: accountId, activeFolder: 'INBOX', messages: [], selectedMessageId: null, selectedMessage: null, ...noWaitingView });
  },

  loadAccounts: async () => {
    try {
      const resp = await api.listAccounts();
      set({ accounts: resp.data });
      if (resp.data.length > 0 && !get().activeAccountId) {
        const primary = resp.data.find(a => a.is_primary) || resp.data[0];
        set({ activeAccountId: primary.id });
      }
    } catch (err) {
      toast.error(`Failed to load accounts: ${errMsg(err)}`);
    }
  },

  loadFolders: async () => {
    const { activeAccountId } = get();
    if (!activeAccountId) return;
    set({ loadingFolders: true });
    try {
      const resp = await api.listFolders(activeAccountId);
      set({ folders: resp.data, loadingFolders: false });
    } catch (err) {
      set({ loadingFolders: false });
      toast.error(`Failed to load folders: ${errMsg(err)}`);
    }
  },

  selectFolder: async (folder) => {
    set({ activeFolder: folder, messages: [], cursor: null, selectedMessageId: null, selectedMessage: null, ...noWaitingView });
    await get().loadMessages();
  },

  loadMessages: async () => {
    const { activeAccountId, activeFolder } = get();
    if (!activeAccountId) return;
    set({ loadingMessages: true });
    try {
      const resp = await api.listMessages(activeAccountId, activeFolder);
      set({
        messages: resp.data,
        hasMore: resp.pagination?.has_more || false,
        cursor: resp.pagination?.cursor || null,
        loadingMessages: false,
      });
    } catch (err) {
      set({ loadingMessages: false });
      toast.error(`Failed to load messages: ${errMsg(err)}`);
    }
  },

  loadMoreMessages: async () => {
    const { activeAccountId, activeFolder, cursor, messages } = get();
    if (!activeAccountId || !cursor) return;
    try {
      const resp = await api.listMessages(activeAccountId, activeFolder, cursor);
      set({
        messages: [...messages, ...resp.data],
        hasMore: resp.pagination?.has_more || false,
        cursor: resp.pagination?.cursor || null,
      });
    } catch (err) {
      toast.error(`Failed to load more messages: ${errMsg(err)}`);
    }
  },

  selectMessage: async (msgId) => {
    set({ selectedMessageId: msgId, loadingMessage: true });
    try {
      const resp = await api.getMessage(msgId);
      set({ selectedMessage: resp.data, loadingMessage: false });
      // Mark as read
      if (!resp.data.is_read) {
        await get().markRead(msgId, true);
      }
    } catch (err) {
      set({ loadingMessage: false });
      toast.error(`Failed to load message: ${errMsg(err)}`);
    }
  },

  markRead: async (msgId, read) => {
    // Optimistic update
    const prev = get();
    set(state => ({
      messages: state.messages.map(m =>
        m.id === msgId ? { ...m, is_read: read } : m
      ),
      selectedMessage: state.selectedMessage?.id === msgId
        ? { ...state.selectedMessage, is_read: read }
        : state.selectedMessage,
    }));
    try {
      await api.updateMessage(msgId, { is_read: read });
    } catch {
      // Rollback on error
      set({
        messages: prev.messages,
        selectedMessage: prev.selectedMessage,
      });
    }
  },

  markFlagged: async (msgId, flagged) => {
    // Optimistic update
    const prev = get();
    set(state => ({
      messages: state.messages.map(m =>
        m.id === msgId ? { ...m, is_flagged: flagged } : m
      ),
    }));
    try {
      await api.updateMessage(msgId, { is_flagged: flagged });
    } catch {
      // Rollback on error
      set({ messages: prev.messages });
    }
  },

  deleteMsg: async (msgId) => {
    await api.deleteMessage(msgId);
    set(state => ({
      messages: state.messages.filter(m => m.id !== msgId),
      waitingMessages: state.waitingMessages?.filter(m => m.id !== msgId) ?? null,
      selectedMessageId: state.selectedMessageId === msgId ? null : state.selectedMessageId,
      selectedMessage: state.selectedMessage?.id === msgId ? null : state.selectedMessage,
    }));
  },

  setThreadDue: async (mailboxId, threadId, due) => {
    const response = await api.setThreadDue(mailboxId, threadId, due);
    set(state => threadDueChange(state, mailboxId, threadId, response.data.due, response.data.due_source));
  },

  applyThreadSnooze: (mailboxId, threadId, until) => {
    set(state => threadSnoozeChange(state, mailboxId, threadId, until));
  },

  snoozeThread: async (mailboxId, threadId, until) => {
    const response = await api.snoozeThread(mailboxId, threadId, until);
    get().applyThreadSnooze(mailboxId, threadId, response.data.snoozed_until);
    await get().refresh();
  },

  removeAccount: async (accountId) => {
    try {
      await api.deleteAccount(accountId);
      const { accounts, activeAccountId } = get();
      const remaining = accounts.filter(a => a.id !== accountId);
      set({ accounts: remaining });
      if (activeAccountId === accountId) {
        const next = remaining[0] || null;
        set({
          activeAccountId: next?.id ?? null,
          folders: [],
          messages: [],
          selectedMessageId: null,
          selectedMessage: null,
          ...noWaitingView,
        });
        if (next) {
          await get().loadFolders();
          await get().loadMessages();
        }
      }
      toast.success('Account removed');
    } catch (err) {
      toast.error(`Failed to remove account: ${errMsg(err)}`);
    }
  },

  refresh: async () => {
    await get().loadFolders();
    await get().loadMessages();
    if (get().waitingMessages) await get().showWaiting();
  },

  searchMessages: async (query: string) => {
    const { activeAccountId, activeFolder } = get();
    if (!activeAccountId) return;

    if (!query.trim()) {
      get().clearSearch();
      return;
    }

    set({ searchQuery: query, isSearching: true, ...noWaitingView });
    try {
      const resp = await api.searchMessages(activeAccountId, query, activeFolder);
      set({ searchResults: resp.data, isSearching: false });
    } catch (err) {
      set({ isSearching: false });
      toast.error(`Search failed: ${errMsg(err)}`);
    }
  },

  clearSearch: () => {
    set({ searchQuery: '', searchResults: null, isSearching: false });
  },

  showWaiting: async () => {
    const { activeAccountId, activeFolder, searchQuery } = get();
    if (!activeAccountId) return;
    try {
      const page = await api.listAwaitingThreads(activeAccountId);
      // Moving to another folder or starting a search while this loaded keeps that choice.
      set(state => (state.activeFolder !== activeFolder || state.searchQuery !== searchQuery
        ? {}
        : { ...waitingPage(state, activeAccountId, page, false), searchQuery: '', searchResults: null, isSearching: false }));
    } catch (err) {
      toast.error(`Failed to load waiting threads: ${errMsg(err)}`);
    }
  },

  loadMoreWaiting: async () => {
    const { activeAccountId, waitingCursor } = get();
    if (!activeAccountId || !waitingCursor) return;
    try {
      const page = await api.listAwaitingThreads(activeAccountId, waitingCursor);
      set(state => (state.waitingMessages ? waitingPage(state, activeAccountId, page, true) : {}));
    } catch (err) {
      toast.error(`Failed to load more waiting threads: ${errMsg(err)}`);
    }
  },

  clearWaiting: () => {
    set(noWaitingView);
  },
}));
