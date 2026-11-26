import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { Sidebar } from '@webmail/components/layout/Sidebar';
import { MessageList } from '@webmail/components/mail/MessageList';
import { TooltipProvider } from '@webmail/components/ui/tooltip';
import { configureMailPlatform } from '@webmail/platform';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { createDesktopTransport } from './transport';
import { handleUIRequest } from './bridge';
import type { MessageSummary } from '@webmail/types';

vi.mock('@webmail/components/ui/scroll-area', () => ({ ScrollArea: ({ children }: { children: ReactNode }) => <div>{children}</div> }));

const response: { data: (MessageSummary & { recipients_cc: string[] })[]; pagination: { has_more: boolean; total: number } } = {
  data: [
    { id: 6, class: 'person', mailbox_id: 1, folder: 'INBOX', message_id: '<n2@navy.example>', thread_id: '<n2@navy.example>', sender: 'grace@navy.example', sender_name: 'Grace Hopper', recipients_to: ['admin@desktop.test'], recipients_cc: [], subject: 'Compiler release on Monday', body_html: '<h2>Release plan</h2><script>alert(1)</script>', size_bytes: 260, has_attachments: false, is_read: false, is_flagged: false, is_starred: false, is_draft: false, received_at: '2026-11-21T15:30:00+01:00' },
    { id: 5, class: 'person', mailbox_id: 1, folder: 'INBOX', message_id: '<n1@analytical.example>', thread_id: '<n1@analytical.example>', sender: 'ada@analytical.example', sender_name: 'Ada Lovelace', recipients_to: ['admin@desktop.test'], recipients_cc: [], subject: 'Notes on the engine', body_text: 'The second set of punched cards is ready for review.', size_bytes: 112, has_attachments: false, is_read: false, is_flagged: false, is_starred: false, is_draft: false, received_at: '2026-11-21T10:12:00+01:00' },
    { id: 7, class: 'person', mailbox_id: 1, folder: 'INBOX', message_id: '<n3@kernel.example>', thread_id: '<n3@kernel.example>', sender: 'linus@kernel.example', sender_name: 'Linus', recipients_to: ['admin@desktop.test'], recipients_cc: [], subject: 'Re: patch series v3', body_text: 'Looks fine. Applied to the testing branch.', size_bytes: 62, has_attachments: false, is_read: false, is_flagged: false, is_starred: false, is_draft: false, received_at: '2026-11-20T23:05:00+01:00' },
  ],
  pagination: { has_more: false, total: 3 },
};

const bridge = { route: () => '/mail', navigate: vi.fn(), screenshot: async () => '' };

describe('desktop inbox integration', () => {
  beforeEach(() => {
    useAuthStore.setState({ user: { id: 0, email: 'admin@desktop.test', display_name: '' }, isAuthenticated: true });
    useMailStore.setState({ accounts: [], activeAccountId: null, activeFolder: 'INBOX', folders: [], messages: [], loadingMessages: false, loadingFolders: false, searchResults: null, waitingMessages: null, selectedMessageId: null, hasMore: false });
    useUIStore.setState({ sidebarCollapsed: { 1: true } });
  });

  it('renders the real server response on first account load without an SSE open event', async () => {
    const send = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      const body = path === '/api/v1/accounts' ? { data: [{ id: 1, address: 'admin@desktop.test', display_name: '', is_primary: true }] }
        : path.endsWith('/folders') ? { data: [{ name: 'INBOX', role: 'inbox', total: 3, unread: 3 }, { name: 'Sent', role: 'sent', total: 1, unread: 0 }] }
          : path.includes('/folders/INBOX/messages?limit=50') ? response : { data: [] };
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    configureMailPlatform({ desktop: true, fetch: createDesktopTransport(send), serverHost: () => 'localhost:18080' });
    render(<TooltipProvider><Sidebar /><MessageList /></TooltipProvider>);
    expect(await screen.findByTestId('message-6')).toBeTruthy();
    expect(screen.getByTestId('message-6').querySelector('.desktop-message-preview')?.textContent).toBe('Release plan');
    expect(screen.getByTestId('message-5').textContent).toContain('Notes on the engine');
    expect(screen.getByTestId('message-7').textContent).toContain('Re: patch series v3');
    expect(screen.queryByText('No messages in Inbox.')).toBeNull();
    expect(screen.getAllByText('admin@desktop.test')).toHaveLength(1);
    const snapshot = await handleUIRequest({ id: 'snapshot', action: 'snapshot' }, bridge) as { elements: { testId: string }[] };
    const ids = snapshot.elements.map(element => element.testId);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.filter(id => id === 'folder-1-INBOX')).toHaveLength(1);
    await handleUIRequest({ id: 'click-inbox', action: 'click', args: { testId: 'folder-1-INBOX' } }, bridge);
    await waitFor(() => expect(screen.getByTestId('message-6')).toBeTruthy());
    fireEvent.click(screen.getByTestId('folder-1-Sent'));
    expect(await screen.findByText('No messages in Sent.')).toBeTruthy();
  });

  it('prefers plain text and safely derives HTML-only previews without active content', () => {
    configureMailPlatform({ desktop: true });
    useMailStore.setState({ messages: [
      { ...response.data[0], body_text: '  Plain text\n wins ', body_html: '<p>Not the preview</p>' },
      { ...response.data[1], body_text: ' ', body_html: '<style>hidden style</style><p>Release &amp; review</p><p>Monday</p><script>window.previewPwned=true</script><img src="https://tracker.invalid/pixel" onerror="window.previewPwned=true">' },
      { ...response.data[2], body_text: '', has_attachments: true },
    ] });
    render(<MessageList />);
    expect(screen.getByTestId('message-6').querySelector('.desktop-message-preview')?.textContent).toBe('Plain text wins');
    expect(screen.getByTestId('message-5').querySelector('.desktop-message-preview')?.textContent).toBe('Release & review Monday');
    expect(screen.getByTestId('message-5').querySelector('.desktop-message-preview img, .desktop-message-preview script')).toBeNull();
    expect((window as unknown as { previewPwned?: boolean }).previewPwned).toBeUndefined();
    expect(screen.getByTestId('message-7').querySelector('.desktop-message-preview')?.textContent).toBe('Includes attachments');
  });

  it('uses plain desktop empty-state copy and preserves webmail copy', () => {
    configureMailPlatform({ desktop: true });
    const view = render(<MessageList />);
    expect(screen.getByText('No messages in Inbox.')).toBeTruthy();
    configureMailPlatform({ desktop: false });
    view.rerender(<MessageList />);
    expect(screen.getByText('// no_messages in inbox')).toBeTruthy();
  });
});
