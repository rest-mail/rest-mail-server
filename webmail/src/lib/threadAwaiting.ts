import type { AwaitingThreadsPage, MessageSummary } from '../types';

export interface WaitingView {
  activeAccountId: number | null;
  /** The waiting threads' last messages; null when the mail list is showing a folder. */
  waitingMessages: MessageSummary[] | null;
  waitingHasMore: boolean;
  waitingCursor: string | null;
}

/** The view a page of waiting threads gives. An answer for an account no longer shown changes nothing. */
export function waitingPage(
  state: WaitingView,
  mailboxId: number,
  page: AwaitingThreadsPage,
  append: boolean,
): Partial<WaitingView> {
  if (state.activeAccountId !== mailboxId) return {};
  const rows = page.data.map(thread => ({ ...thread.last_message, awaiting_since: thread.awaiting_since }));
  return {
    waitingMessages: append ? [...(state.waitingMessages ?? []), ...rows] : rows,
    waitingHasMore: page.pagination?.has_more || false,
    waitingCursor: page.pagination?.cursor || null,
  };
}

export const noWaitingView = { waitingMessages: null, waitingHasMore: false, waitingCursor: null };
