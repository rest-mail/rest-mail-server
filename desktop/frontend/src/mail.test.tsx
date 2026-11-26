import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
vi.mock('@webmail/api/client', () => ({ updateMessage: vi.fn().mockResolvedValue({}), deleteMessage: vi.fn().mockResolvedValue({}) }));
vi.mock('@webmail/components/ui/scroll-area', () => ({ ScrollArea: ({ children }: { children: ReactNode }) => <div>{children}</div> }));
import { MessageActions } from '@webmail/components/mail/MessageActions';
import { MessageList } from '@webmail/components/mail/MessageList';
import { configureMailPlatform } from '@webmail/platform';
import { useMailStore } from '@webmail/stores/mailStore';
import type { MessageSummary } from '@webmail/types';
import * as api from '@webmail/api/client';

const messages = [
  { id: 1, thread_id: 'conversation', subject: 'Zebra', sender: 'z@example.test', received_at: '2026-11-21T12:00:00Z' },
  { id: 2, thread_id: 'conversation', subject: 'Apple', sender: 'a@example.test', received_at: '2026-11-21T11:00:00Z' },
  { id: 3, thread_id: 'other', subject: 'Notes', sender: 'n@example.test', received_at: '2026-11-21T10:00:00Z' },
].map(message => ({ ...message, class: 'person', sender_name: '', folder: 'INBOX', is_read: false, is_starred: false, is_flagged: false, is_draft: false, has_attachments: false, recipients_to: [], mailbox_id: 1, size_bytes: 20, message_id: String(message.id) }) as MessageSummary);

describe('desktop shared message controls', () => {
  beforeEach(() => {
    vi.clearAllMocks(); configureMailPlatform({ desktop: true });
    useMailStore.setState({ messages, selectedMessageId: null, loadingMessages: false, activeFolder: 'INBOX', searchResults: null, searchQuery: '', waitingMessages: null, hasMore: true, folders: [{ name: 'Archive', total: 0, unread: 0 }], refresh: vi.fn().mockResolvedValue(undefined), selectMessage: vi.fn().mockResolvedValue(undefined), loadMoreMessages: vi.fn().mockResolvedValue(undefined) });
  });
  it.each([
    ['read', { is_read: true }], ['unread', { is_read: false }], ['star', { is_starred: true }], ['unstar', { is_starred: false }],
    ['flag', { is_flagged: true }], ['unflag', { is_flagged: false }], ['archive', { folder: 'Archive' }],
  ])('applies bulk %s to every selected message', async (action, update) => {
    render(<MessageActions ids={[1, 2]} prefix="bulk" />);
    fireEvent.click(screen.getByTestId(`bulk-${action}`));
    await waitFor(() => expect(useMailStore.getState().refresh).toHaveBeenCalledOnce());
    expect(api.updateMessage).toHaveBeenCalledWith(1, update);
    expect(api.updateMessage).toHaveBeenCalledWith(2, update);
  });
  it('moves and deletes selected messages through the server API', async () => {
    render(<MessageActions ids={[1]} prefix="reader" />);
    fireEvent.change(screen.getByTestId('reader-destination'), { target: { value: 'Archive' } });
    fireEvent.click(screen.getByTestId('reader-move'));
    await waitFor(() => expect(api.updateMessage).toHaveBeenCalledWith(1, { folder: 'Archive' }));
    await waitFor(() => expect((screen.getByTestId('reader-delete') as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId('reader-delete'));
    await waitFor(() => expect(api.deleteMessage).toHaveBeenCalledWith(1));
  });
  it('sorts, groups threads and loads the next server page', async () => {
    render(<MessageList />);
    const rows = () => [...document.querySelectorAll('[data-testid^="message-"]')].filter(element => element.tagName === 'BUTTON').map(element => element.getAttribute('data-testid'));
    expect(rows()).toEqual(['message-1', 'message-2', 'message-3']);
    fireEvent.change(screen.getByTestId('messages-sort'), { target: { value: 'subject' } });
    expect(rows()).toEqual(['message-2', 'message-3', 'message-1']);
    fireEvent.click(screen.getByTestId('messages-threaded'));
    expect(rows()).toEqual(['message-2', 'message-3']);
    fireEvent.click(screen.getByTestId('messages-load-more'));
    expect(useMailStore.getState().loadMoreMessages).toHaveBeenCalledOnce();
  });
  it('selects with arrows and archives with E without hijacking text fields', async () => {
    useMailStore.setState({ selectedMessageId: 1 });
    render(<MessageList />);
    fireEvent.keyDown(window, { key: 'ArrowDown' });
    expect(useMailStore.getState().selectMessage).toHaveBeenCalledWith(2);
    fireEvent.keyDown(window, { key: 'e' });
    await waitFor(() => expect(api.updateMessage).toHaveBeenCalledWith(1, { folder: 'Archive' }));
    const input = screen.getByTestId('messages-select-all');
    vi.clearAllMocks(); fireEvent.keyDown(input, { key: 'Delete' });
    expect(api.deleteMessage).not.toHaveBeenCalled();
  });
});
