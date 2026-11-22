import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() } }));
import { Call } from '@wailsio/runtime';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { LocalMailStatus, LocalStorageSettings, formatBytes, highlightedSnippet, type IndexResponse } from './localMail';
import { createDesktopTransport } from './transport';

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({ isAuthenticated: true });
  useMailStore.setState({ activeAccountId: 1, activeFolder: 'INBOX', searchQuery: 'cafe', searchResults: [], selectMessage: vi.fn().mockResolvedValue(undefined) });
  vi.mocked(Call.ByName).mockResolvedValue({ account_id: 1, messages: 10, bodies: 8, bytes: 5242880, complete: false, offline: true });
});

function answer(source = 'local', offline = false) {
  return new Response(JSON.stringify({ data: [{ id: 1, subject: 'Café notes', snippet: 'A ⟦café⟧ <script> is text', source: 'local' }], source, complete: false }), { headers: { 'Content-Type': 'application/json', 'X-Restmail-Source': source, 'X-Restmail-Offline': String(offline) } });
}

describe('local SQLite mail UI', () => {
  it('formats store bytes and renders untrusted snippets as text with highlighted matches', () => {
    expect(formatBytes(100)).toBe('100 B'); expect(formatBytes(2048)).toBe('2.0 KiB'); expect(formatBytes(5242880)).toBe('5.0 MiB');
    const { container } = render(<div>{highlightedSnippet('<script>⟦café⟧ & text')}</div>);
    expect(container.querySelector('script')).toBeNull(); expect(container.querySelector('mark')?.textContent).toBe('café'); expect(container.textContent).toContain('<script>');
  });
  it('shows offline availability, source labels and safely highlighted ranked snippets', async () => {
    render(<LocalMailStatus />);
    await screen.findByTestId('local-mail-status');
    expect(screen.getByTestId('local-mail-status').textContent).toContain('Offline — synced mail available');
    const detail: IndexResponse = { path: '/api/v1/accounts/1/search?q=cafe&folder=INBOX', source: 'local', offline: true, result: { data: [{ id: 1, subject: 'Café notes', snippet: '⟦café⟧ <script>', source: 'local' } as never] } };
    fireEvent(window, new CustomEvent('desktop:index-response', { detail }));
    expect(document.querySelector('.desktop-local-snippets mark')?.textContent).toBe('café'); expect(document.querySelector('.desktop-local-snippets script')).toBeNull();
    fireEvent.click(screen.getByText('Café notes'));
    expect(useMailStore.getState().selectMessage).toHaveBeenCalledWith(1);
    fireEvent(window, new CustomEvent('desktop:index-response', { detail: { ...detail, source: 'local+server' } }));
    expect(useMailStore.getState().searchResults?.[0].id).toBe(1);
  });
  it('shows store size beside Appearance and requests reindex without deleting cached mail', async () => {
    render(<LocalStorageSettings />);
    expect((await screen.findByTestId('local-store-settings')).textContent).toContain('5.0 MiB');
    fireEvent.click(screen.getByText('Reindex mail'));
    await waitFor(() => expect(Call.ByName).toHaveBeenCalledWith('main.LocalMail.Reindex', 1));
    expect(await screen.findByText('Reindex started. Cached mail stays available.')).toBeTruthy();
  });
  it('returns local results without waiting for the background server supplement', async () => {
    let complete!: (response: Response) => void;
    const send = vi.fn<typeof fetch>().mockResolvedValueOnce(answer()).mockImplementationOnce(() => new Promise(resolve => { complete = resolve; }));
    const report = vi.fn(); const transport = createDesktopTransport(send, report);
    const response = await transport('/api/v1/accounts/1/search?q=cafe&folder=INBOX');
    expect((await response.json()).source).toBe('local');
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(report).toHaveBeenCalledTimes(1);
    complete(answer('local+server'));
    await waitFor(() => expect(report).toHaveBeenCalledTimes(2));
    expect(report.mock.calls[1][0].source).toBe('local+server');
  });
  it('discards stale background results after the query changes', async () => {
    let oldComplete!: (response: Response) => void;
    const send = vi.fn<typeof fetch>().mockResolvedValueOnce(answer()).mockImplementationOnce(() => new Promise(resolve => { oldComplete = resolve; })).mockResolvedValueOnce(new Response('{}'));
    const report = vi.fn(); const transport = createDesktopTransport(send, report);
    await transport('/api/v1/accounts/1/search?q=old&folder=INBOX');
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    await transport('/api/v1/accounts/1/search?q=new&folder=INBOX');
    oldComplete(answer('local+server'));
    await new Promise(resolve => setTimeout(resolve, 10));
    expect(report).toHaveBeenCalledTimes(1);
  });
});
