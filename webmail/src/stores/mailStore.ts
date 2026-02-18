import { create } from 'zustand';
import { toast } from 'sonner';
import type { Folder, MessageSummary, MessageDetail, Account } from '../types';
import * as api from '../api/client';

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
  removeAccount: (accountId: number) => Promise<void>;
  refresh: () => Promise<void>;
  searchMessages: (query: string) => Promise<void>;
  clearSearch: () => void;
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
  loadingFolders: false,
  loadingMessages: false,
  loadingMessage: false,

  setActiveAccount: (accountId) => {
    set({ activeAccountId: accountId, activeFolder: 'INBOX', messages: [], selectedMessageId: null, selectedMessage: null });
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
    set({ activeFolder: folder, messages: [], cursor: null, selectedMessageId: null, selectedMessage: null });
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
    await api.updateMessage(msgId, { is_read: read });
    set(state => ({
      messages: state.messages.map(m =>
        m.id === msgId ? { ...m, is_read: read } : m
      ),
      selectedMessage: state.selectedMessage?.id === msgId
        ? { ...state.selectedMessage, is_read: read }
        : state.selectedMessage,
    }));
  },

  markFlagged: async (msgId, flagged) => {
    await api.updateMessage(msgId, { is_flagged: flagged });
    set(state => ({
      messages: state.messages.map(m =>
        m.id === msgId ? { ...m, is_flagged: flagged } : m
      ),
    }));
  },

  deleteMsg: async (msgId) => {
    await api.deleteMessage(msgId);
    set(state => ({
      messages: state.messages.filter(m => m.id !== msgId),
      selectedMessageId: state.selectedMessageId === msgId ? null : state.selectedMessageId,
      selectedMessage: state.selectedMessage?.id === msgId ? null : state.selectedMessage,
    }));
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
  },

  searchMessages: async (query: string) => {
    const { activeAccountId, activeFolder } = get();
    if (!activeAccountId) return;

    if (!query.trim()) {
      get().clearSearch();
      return;
    }

    set({ searchQuery: query, isSearching: true });
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
}));
