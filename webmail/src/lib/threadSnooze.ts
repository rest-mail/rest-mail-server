import type { MessageDetail, MessageSummary } from '../types';

interface ThreadSnoozeState {
  activeAccountId: number | null;
  messages: MessageSummary[];
  selectedMessageId: number | null;
  selectedMessage: MessageDetail | null;
  searchResults: MessageSummary[] | null;
}

export function threadSnoozeChange(state: ThreadSnoozeState, mailboxId: number, threadId: string, until: string | null): Partial<ThreadSnoozeState> {
  if (state.activeAccountId !== mailboxId) return {};
  const matches = (message: MessageSummary) => message.mailbox_id === mailboxId && message.thread_id === threadId;
  const selected = state.selectedMessage;
  const hideSelected = until !== null && state.searchResults === null && selected !== null && matches(selected);
  return {
    messages: until === null ? state.messages : state.messages.filter(message => !matches(message)),
    searchResults: state.searchResults?.map(message => matches(message) ? { ...message, snoozed_until: until } : message) ?? null,
    selectedMessageId: hideSelected ? null : state.selectedMessageId,
    selectedMessage: hideSelected ? null : selected && matches(selected) ? { ...selected, snoozed_until: until } : selected,
  };
}

export function snoozeDeadline(value: string, now = Date.now()): string {
  const deadline = new Date(value);
  if (!Number.isFinite(deadline.getTime()) || deadline.getTime() <= now) {
    throw new Error('Choose a time in the future');
  }
  return deadline.toISOString();
}

export function handleThreadSnoozeEvent(
  data: Record<string, unknown>,
  apply: (mailboxId: number, threadId: string, until: string | null) => void,
  refresh: () => void | Promise<void>,
): boolean {
  const { mailbox_id, thread_id, snoozed_until } = data;
  if (typeof mailbox_id !== 'number' || typeof thread_id !== 'string' || (snoozed_until !== null && typeof snoozed_until !== 'string')) return false;
  apply(mailbox_id, thread_id, snoozed_until);
  void refresh();
  return true;
}
