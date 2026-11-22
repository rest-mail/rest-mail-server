import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn().mockResolvedValue(undefined) } }));
import { Call } from '@wailsio/runtime';
import { tabs } from './tabs';
import { useAuthStore } from '@webmail/stores/authStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { configureMailPlatform } from '@webmail/platform';
import type { MessageDetail } from '@webmail/types';
import { handleNativeMailEvent, inboxUnread, installNative, isEditing, native, notificationMetadata, registerMessageAction, runNativeCommand } from './native';

describe('native mail integration', () => {
  afterEach(() => vi.useRealTimers());
  beforeEach(() => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(true);
    vi.clearAllMocks(); vi.mocked(Call.ByName).mockResolvedValue(true); configureMailPlatform({ desktop: true });
    tabs.clear();
    document.body.innerHTML = '';
    useAuthStore.setState({ isAuthenticated: true, setupRequired: [] });
    useUIStore.setState({ view: 'mail', composeState: null });
    useMailStore.setState({ selectedMessage: { id: 42, is_read: false, is_flagged: false, is_draft: false } as MessageDetail, loadingMessage: false, loadingFolders: false, activeAccountId: 1, folders: [] });
  });
  it('detects inputs and nested rich editors without mistaking reader text for editing', () => {
    document.body.innerHTML = '<input><textarea></textarea><select></select><div contenteditable="true"><p><span>Editor</span></p></div><article>Reader</article>';
    for (const selector of ['input', 'textarea', 'select', 'span']) expect(isEditing(document.querySelector(selector))).toBe(true);
    expect(isEditing(document.querySelector('article'))).toBe(false);
    expect(isEditing(null)).toBe(false);
  });
  it('ignores native commands and tab keys when another app owns focus', () => {
    vi.mocked(document.hasFocus).mockReturnValue(false);
    const callback = vi.fn(); const unregister = registerMessageAction('Reply', callback);
    const stop = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    runNativeCommand('new-message'); runNativeCommand('reply');
    const id = tabs.open({ to: '', cc: '', bcc: '', subject: 'Unchanged' });
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', ctrlKey: true }));
    expect(tabs.get().active).toBe(id); expect(tabs.get().tabs).toHaveLength(1); expect(callback).not.toHaveBeenCalled();
    stop(); unregister();
  });
  it('dispatches the exact callbacks registered by the reader buttons', () => {
    for (const [command, label] of [['reply', 'Reply'], ['reply-all', 'Reply all'], ['forward', 'Forward'], ['archive', 'Archive'], ['delete', 'Delete'], ['toggle-flag', 'Flag']]) {
      const callback = vi.fn(); const stop = registerMessageAction(label, callback);
      runNativeCommand(command); expect(callback).toHaveBeenCalledOnce();
      stop(); runNativeCommand(command); expect(callback).toHaveBeenCalledOnce();
    }
  });
  it('does not invoke mail actions while typing, signed out, loading, or composing', () => {
    const callback = vi.fn(); const stop = registerMessageAction('Reply', callback);
    document.body.innerHTML = '<input>';
    document.querySelector('input')!.focus(); runNativeCommand('reply'); runNativeCommand('new-message');
    expect(Call.ByName).not.toHaveBeenCalled();
    document.querySelector('input')!.blur();
    useAuthStore.setState({ isAuthenticated: false }); runNativeCommand('reply');
    useAuthStore.setState({ isAuthenticated: true });
    useMailStore.setState({ loadingMessage: true }); runNativeCommand('reply');
    useMailStore.setState({ loadingMessage: false }); runNativeCommand('reply', 'compose-1');
    useUIStore.setState({ view: 'settings' }); runNativeCommand('reply');
    expect(callback).not.toHaveBeenCalled(); stop();
  });
  it('opens compose in an independent tab without replacing the main mail pane', () => {
    configureMailPlatform({ desktop: true, openCompose: native.openCompose });
    useUIStore.getState().startCompose({ to: 'friend@test.invalid', cc: '', bcc: '', subject: 'Hello' });
    expect(useUIStore.getState().view).toBe('mail');
    expect(tabs.get().tabs[0].initialState.to).toBe('friend@test.invalid');
    expect(Call.ByName).not.toHaveBeenCalled();
  });
  it('retains the webmail in-pane compose behavior without the opt-in hook', () => {
    configureMailPlatform({}); useUIStore.getState().startCompose();
    expect(useUIStore.getState().view).toBe('compose');
  });
  it('cycles tabs while typing and allows Cmd-W dirty-close routing without firing message actions', () => {
    const handlers = new Map<string, (data: unknown) => void>();
    const stop = installNative({ on: (name, handler) => { handlers.set(name, handler); return () => {}; }, emit: vi.fn() }, 'main');
    const first = tabs.open({ to: '', cc: '', bcc: '', subject: 'One' });
    const second = tabs.open({ to: '', cc: '', bcc: '', subject: 'Two' });
    document.body.innerHTML = '<input>'; document.querySelector('input')!.focus();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', ctrlKey: true, shiftKey: true, cancelable: true }));
    expect(tabs.get().active).toBe(first);
    runNativeCommand('close-tab'); expect(Call.ByName).toHaveBeenCalledWith('main.Native.RequestClose', first);
    handlers.get('native:tab-closed')?.(first); expect(tabs.get().tabs.map(tab => tab.id)).toEqual([second]);
    stop();
  });
  it('badges only unread Inbox messages and clamps negative counts', () => {
    expect(inboxUnread([{ name: 'INBOX', unread: 4 }, { name: 'Archive', unread: 100 }, { name: 'inbox', unread: 2 }, { name: 'INBOX', unread: -1 }])).toBe(6);
    expect(inboxUnread([])).toBe(0);
  });
  it('passes only sender and subject from the SSE event to native notifications', () => {
    vi.useFakeTimers();
    useMailStore.setState({ selectedMessage: null });
    const stop = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    const event = { type: 'new_message', data: { message_id: 42, folder: 'INBOX', sender: 'friend@test.invalid', subject: 'Hello', body_text: 'PRIVATE BODY', body_html: '<p>PRIVATE</p>', token: 'never' } };
    expect(notificationMetadata(event)).toEqual({ id: 42, sender: 'friend@test.invalid', subject: 'Hello' });
    handleNativeMailEvent(event);
    vi.advanceTimersByTime(2000);
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.NewMailBatch', [{ id: 42, sender: 'friend@test.invalid', subject: 'Hello' }]);
    expect(JSON.stringify(vi.mocked(Call.ByName).mock.calls)).not.toContain('PRIVATE');
    for (const data of [{ message_id: 42, folder: 'Sent' }, { message_id: -1 }, { message_id: 'bad' }, {}]) expect(notificationMetadata({ type: 'new_message', data })).toBeNull();
    expect(notificationMetadata({ type: 'folder_update', data: {} })).toBeNull();
    stop();
  });
  it('counts startup unread only after folders load and permission resolves, once per sign-in', async () => {
    useMailStore.setState({ loadingFolders: true });
    const stop = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    await Promise.resolve();
    expect(Call.ByName).not.toHaveBeenCalledWith('main.Native.UnreadSummary', expect.anything());
    useMailStore.setState({ loadingFolders: false, folders: [{ name: 'INBOX', unread: 3 }, { name: 'Sent', unread: 7 }] as never });
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.UnreadSummary', 3);
    useMailStore.setState({ folders: [{ name: 'INBOX', unread: 4 }] as never });
    expect(vi.mocked(Call.ByName).mock.calls.filter(call => call[0] === 'main.Native.UnreadSummary')).toHaveLength(1);
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.SetUnread', 4);
    useAuthStore.setState({ isAuthenticated: false });
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.SetUnread', 0);
    useAuthStore.setState({ isAuthenticated: true });
    await Promise.resolve();
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.UnreadSummary', 4);
    stop();
  });
  it('does not announce an empty Inbox or denied permission', async () => {
    useMailStore.setState({ folders: [{ name: 'INBOX', unread: 3 }] as never });
    vi.mocked(Call.ByName).mockResolvedValue(false);
    const stop = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    await Promise.resolve();
    expect(vi.mocked(Call.ByName).mock.calls.filter(call => call[0] === 'main.Native.UnreadSummary')).toHaveLength(0);
    stop();
    vi.mocked(Call.ByName).mockResolvedValue(true);
    useMailStore.setState({ folders: [{ name: 'INBOX', unread: 0 }] as never });
    const stopEmpty = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    await Promise.resolve();
    expect(vi.mocked(Call.ByName).mock.calls.filter(call => call[0] === 'main.Native.UnreadSummary')).toHaveLength(0);
    stopEmpty();
  });
  it('excludes the viewed message and cancels a pending batch on logout', () => {
    vi.useFakeTimers();
    const stop = installNative({ on: () => () => {}, emit: vi.fn() }, 'main');
    for (const id of [42, 43, 44, 43]) handleNativeMailEvent({ type: 'new_message', data: { message_id: id, folder: 'INBOX', sender: 'Sender', subject: 'Subject' } });
    vi.advanceTimersByTime(2000);
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.NewMailBatch', [{ id: 43, sender: 'Sender', subject: 'Subject' }, { id: 44, sender: 'Sender', subject: 'Subject' }]);
    vi.mocked(Call.ByName).mockClear();
    handleNativeMailEvent({ type: 'new_message', data: { message_id: 45 } });
    useAuthStore.setState({ isAuthenticated: false });
    vi.advanceTimersByTime(2000);
    expect(vi.mocked(Call.ByName).mock.calls.filter(call => call[0] === 'main.Native.NewMailBatch')).toHaveLength(0);
    stop();
  });
  it('routes notification clicks to the main window and refreshes after another window sends', async () => {
    const handlers = new Map<string, (data: unknown) => void>();
    const refresh = vi.fn().mockResolvedValue(undefined); const selectMessage = vi.fn().mockResolvedValue(undefined); const selectFolder = vi.fn().mockResolvedValue(undefined);
    const previous = useMailStore.getState(); useMailStore.setState({ refresh, selectMessage, selectFolder });
    const stop = installNative({ on: (name, handler) => { handlers.set(name, handler); return () => { handlers.delete(name); }; }, emit: vi.fn() }, 'main');
    handlers.get('native:mail-changed')?.({ sent: true }); expect(refresh).toHaveBeenCalledOnce();
    await handlers.get('native:open-message')?.(42); expect(selectMessage).toHaveBeenCalledWith(42);
    await handlers.get('native:open-inbox')?.(undefined); expect(selectFolder).toHaveBeenCalledWith('INBOX');
    expect(Call.ByName).toHaveBeenCalledWith('main.Native.RequestNotificationPermission');
    stop(); useMailStore.setState(previous);
  });
});
