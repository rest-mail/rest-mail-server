import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { configureMailPlatform } from '@webmail/platform';
import { DesktopTabs } from './DesktopTabs';
import { layoutPreferences, normalizeLayout } from './layout';
import { tabs } from './tabs';
import { installNative, runNativeCommand } from './native';
import { Call } from '@wailsio/runtime';

vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn().mockResolvedValue(undefined) } }));
vi.mock('@webmail/components/compose/ComposeView', () => ({ ComposeView: () => <div>Draft editor</div> }));

const events = { on: () => () => {}, emit: vi.fn() };

describe('desktop mail layout preferences', () => {
  beforeEach(() => {
    localStorage.clear();
    layoutPreferences.restore('localhost:18080', 'admin@desktop.test');
    configureMailPlatform({ desktop: true, serverHost: () => 'localhost:18080' });
    useAuthStore.setState({ user: { id: 0, email: 'admin@desktop.test', display_name: 'Admin' }, isAuthenticated: true, setupRequired: [] });
    useMailStore.setState({ selectedMessageId: null, selectedMessage: null, folders: [], accounts: [] });
    useUIStore.setState({ view: 'mail' });
    tabs.clear();
    vi.spyOn(document, 'hasFocus').mockReturnValue(true);
    vi.mocked(Call.ByName).mockClear();
  });

  it('normalizes invalid saved data and clamps both independent split sizes', () => {
    expect(normalizeLayout(null)).toEqual({ layout: 'columns', folders: true, reader: true, columnsWidth: 340, stackedHeight: 230 });
    expect(normalizeLayout({ layout: 'invalid', folders: 'yes', reader: false, columnsWidth: -2, stackedHeight: 2000 })).toEqual({ layout: 'columns', folders: true, reader: false, columnsWidth: 260, stackedHeight: 900 });
    expect(normalizeLayout({ columnsWidth: NaN, stackedHeight: Infinity }).columnsWidth).toBe(340);
    expect(normalizeLayout({ columnsWidth: NaN, stackedHeight: Infinity }).stackedHeight).toBe(230);
  });

  it('persists layout, panes, and independent splits per server and user', () => {
    layoutPreferences.resize(420);
    layoutPreferences.select('stacked');
    layoutPreferences.resize(310);
    layoutPreferences.toggleFolders();
    layoutPreferences.toggleReader();
    const saved = layoutPreferences.get();
    layoutPreferences.restore('other.example', 'admin@desktop.test');
    expect(layoutPreferences.get().layout).toBe('columns');
    layoutPreferences.restore('localhost:18080', 'other@desktop.test');
    expect(layoutPreferences.get().folders).toBe(true);
    layoutPreferences.restore('localhost:18080', 'admin@desktop.test');
    expect(layoutPreferences.get()).toEqual(saved);
    expect(saved).toEqual({ layout: 'stacked', folders: false, reader: false, columnsWidth: 420, stackedHeight: 310 });
  });

  it('recovers from malformed storage without preventing mail from opening', () => {
    localStorage.setItem('restmail:desktop-layout:localhost%3A18080:admin%40desktop.test', '{broken');
    layoutPreferences.restore('localhost:18080', 'admin@desktop.test');
    expect(layoutPreferences.get().layout).toBe('columns');
    const blocked = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(() => layoutPreferences.select('stacked')).not.toThrow();
    expect(layoutPreferences.get().layout).toBe('stacked');
    blocked.mockRestore();
  });

  it('shows branded, accessible layout and pane controls with selected states', () => {
    const { container } = render(<DesktopTabs events={events}><div>Mailbox</div></DesktopTabs>);
    expect(screen.getByLabelText('REST Mail').querySelector('img')?.getAttribute('src')).toBe('/rest-mail-icon.svg');
    expect(screen.getByRole('button', { name: 'Three columns' }).getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: 'List above reader' }));
    expect(container.firstElementChild?.getAttribute('data-layout')).toBe('stacked');
    expect(screen.getByRole('button', { name: 'List above reader' }).getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: 'Show folder pane' }));
    expect(container.firstElementChild?.getAttribute('data-folders')).toBe('false');
    expect(screen.getByRole('button', { name: 'Show folder pane' }).getAttribute('aria-pressed')).toBe('false');
  });

  it('opens a full-pane message with a working back action when the reader is hidden', () => {
    useMailStore.setState({ selectedMessageId: 2 });
    const { container } = render(<DesktopTabs events={events}><div>Mailbox</div></DesktopTabs>);
    fireEvent.click(screen.getByRole('button', { name: 'Show reader pane' }));
    expect(useMailStore.getState().selectedMessageId).toBeNull();
    expect(container.firstElementChild?.getAttribute('data-reading')).toBe('false');
    fireEvent.click(screen.getByRole('tab', { name: 'Mail' }));
    act(() => useMailStore.setState({ selectedMessageId: 2 }));
    fireEvent.click(screen.getByRole('tab', { name: 'Mail' }));
    expect(container.firstElementChild?.getAttribute('data-reading')).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: 'Back to messages' }));
    expect(useMailStore.getState().selectedMessageId).toBeNull();
    expect(container.firstElementChild?.getAttribute('data-reading')).toBe('false');
  });

  it('routes native View commands and synchronizes their selected states', () => {
    const stop = installNative(events, 'main');
    runNativeCommand('view.layout.stacked');
    runNativeCommand('view.pane.folders');
    runNativeCommand('view.pane.reader');
    expect(layoutPreferences.get()).toMatchObject({ layout: 'stacked', folders: false, reader: false });
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.SyncState', 'main', expect.objectContaining({ layout: 'stacked', folders: false, reader: false }));
    runNativeCommand('view.layout.columns');
    expect(layoutPreferences.get().layout).toBe('columns');
    runNativeCommand('view.layout.stacked', 'compose-1');
    expect(layoutPreferences.get().layout).toBe('columns');
    stop();
  });

  it('restores selected layout and hidden panes on a new mounted window', () => {
    layoutPreferences.select('stacked');
    layoutPreferences.toggleFolders();
    layoutPreferences.toggleReader();
    layoutPreferences.restore('other.example', 'other@example.com');
    const { container } = render(<DesktopTabs events={events}><div>Mailbox</div></DesktopTabs>);
    expect(container.firstElementChild?.getAttribute('data-layout')).toBe('stacked');
    expect(container.firstElementChild?.getAttribute('data-folders')).toBe('false');
    expect(container.firstElementChild?.getAttribute('data-reader')).toBe('false');
  });
});
