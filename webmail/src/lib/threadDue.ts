import type { MessageDetail, MessageSummary } from '../types';

interface ThreadDueState {
  activeAccountId: number | null;
  messages: MessageSummary[];
  searchResults: MessageSummary[] | null;
  selectedMessage: MessageDetail | null;
}

export function threadDueChange(state: ThreadDueState, mailboxId: number, threadId: string, due: string | null, source: 'user' | null): Partial<ThreadDueState> {
  if (state.activeAccountId !== mailboxId) return {};
  const update = <Message extends MessageSummary>(message: Message): Message =>
    message.mailbox_id === mailboxId && message.thread_id === threadId ? { ...message, due, due_source: source } : message;
  return {
    messages: state.messages.map(update),
    searchResults: state.searchResults?.map(update) ?? null,
    selectedMessage: state.selectedMessage ? update(state.selectedMessage) : null,
  };
}

export function dueInstant(value: string): string {
  const instant = new Date(value);
  if (!Number.isFinite(instant.getTime())) throw new Error('Choose a valid due date');
  return instant.toISOString();
}

export function dueLocalInput(due?: string | null): string {
  if (!due) return '';
  const instant = new Date(due);
  if (!Number.isFinite(instant.getTime())) return '';
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${instant.getFullYear()}-${pad(instant.getMonth() + 1)}-${pad(instant.getDate())}T${pad(instant.getHours())}:${pad(instant.getMinutes())}`;
}

// A date parsed from the message text is only a suggestion until someone accepts it.
export function isDueSuggestion(source?: string | null): boolean {
  return source === 'parsed';
}

// The picker holds the user's own date. It never starts on a suggestion, because pressing
// "Set due date" on one would accept it, with its seconds cut off, by accident.
export function initialDueInput(message: Pick<MessageSummary, 'due' | 'due_source'>): string {
  return isDueSuggestion(message.due_source) ? '' : dueLocalInput(message.due);
}

export type DueAction = 'set' | 'clear' | 'accept';

export function dueToSave(action: DueAction, message: Pick<MessageSummary, 'due' | 'due_source'>, value: string): string | null {
  if (action === 'clear') return null;
  if (action === 'set') return dueInstant(value);
  if (!message.due || !isDueSuggestion(message.due_source)) throw new Error('There is no suggestion to accept');
  return message.due;
}
