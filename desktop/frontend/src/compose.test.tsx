import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
vi.mock('@webmail/components/compose/RichTextEditor', () => ({ RichTextEditor: () => <div data-testid="test-editor" /> }));
vi.mock('@webmail/api/client', () => ({
  createDraft: vi.fn().mockResolvedValue({ data: { id: 88 } }), updateDraft: vi.fn().mockResolvedValue({ data: { id: 88 } }),
  sendDraft: vi.fn().mockResolvedValue(undefined), sendMessage: vi.fn().mockResolvedValue(undefined), forwardMessage: vi.fn().mockResolvedValue(undefined),
  suggestContacts: vi.fn().mockResolvedValue({ data: [] }), deleteMessage: vi.fn().mockResolvedValue(undefined),
}));
import { ComposeView } from '@webmail/components/compose/ComposeView';
import { configureMailPlatform } from '@webmail/platform';
import { useUIStore } from '@webmail/stores/uiStore';
import { useMailStore } from '@webmail/stores/mailStore';
import * as api from '@webmail/api/client';

describe('desktop compose', () => {
  it('shows Go-held attachment chips and blocks send, schedule and save without losing files', () => {
    const remove = vi.fn(); const pick = vi.fn();
    render(<ComposeView heldAttachments={[{ id: 'file-1', filename: 'notes.txt', size: 2048 }]} onRemoveAttachment={remove} onPickAttachments={pick} />);
    expect(screen.getByText('2.0 KB')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain("Attachments can't be sent yet — the server update is on its way");
    for (const id of ['compose-send', 'compose-save']) expect((screen.getByTestId(id) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId('compose-send')); expect(api.sendMessage).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId('compose-attach')); expect(pick).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId('compose-remove-held-file-1')); expect(remove).toHaveBeenCalledWith('file-1');
  });
  it('keeps mounted compose tab fields independent of the global pane state', () => {
    const state = { to: '', cc: '', bcc: '', subject: 'First draft' };
    const rendered = render(<ComposeView initialState={state} initialFrom="chris@test.invalid" />);
    fireEvent.change(screen.getByTestId('compose-subject'), { target: { value: 'Edited first' } });
    useUIStore.setState({ composeState: { ...state, subject: 'Other tab' } });
    rendered.rerender(<ComposeView initialState={state} initialFrom="chris@test.invalid" />);
    expect((screen.getByTestId('compose-subject') as HTMLInputElement).value).toBe('Edited first');
  });
  beforeEach(() => {
    vi.clearAllMocks(); configureMailPlatform({ desktop: true, canSendAttachments: true });
    useMailStore.setState({ accounts: [{ id: 1, address: 'chris@test.invalid', display_name: 'Chris', is_primary: true }], activeAccountId: 1 });
    useUIStore.getState().startCompose({ to: 'alex@test.invalid', cc: '', bcc: '', subject: 'Hello' });
  });
  it('saves a server draft then updates and sends it', async () => {
    render(<ComposeView />); fireEvent.click(screen.getByTestId('compose-save'));
    await waitFor(() => expect(api.createDraft).toHaveBeenCalledWith(expect.objectContaining({ subject: 'Hello', to: ['alex@test.invalid'] })));
    await waitFor(() => expect((screen.getByTestId('compose-save') as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByTestId('compose-send'));
    await waitFor(() => expect(api.sendDraft).toHaveBeenCalledWith(88));
    expect(api.updateDraft).toHaveBeenCalledWith(88, expect.objectContaining({ subject: 'Hello' }));
  });
  it('native selections block send, draft save and schedule rather than dropping files', async () => {
    configureMailPlatform({ desktop: true, canSendAttachments: true, pickAttachments: async () => [{ filename: 'report.pdf', content_type: 'application/pdf', content: 'ZmlsZQ==' }] });
    render(<ComposeView />); fireEvent.click(screen.getByTestId('compose-attach'));
    await screen.findByText('report.pdf');
    expect(screen.getByRole('alert').textContent).toContain("Attachments can't be sent yet");
    expect(screen.getByRole('link', { name: 'Track #2212' }).getAttribute('href')).toContain('/issues/2212');
    expect((screen.getByTestId('compose-send') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId('compose-save') as HTMLButtonElement).disabled).toBe(true);
    expect(api.sendMessage).not.toHaveBeenCalled(); expect(api.createDraft).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId('compose-remove-attachment-0'));
    expect((screen.getByTestId('compose-send') as HTMLButtonElement).disabled).toBe(false);
  });
  it('forwards through the original message route to preserve existing attachments', async () => {
    useUIStore.getState().startCompose({ to: 'alex@test.invalid', cc: '', bcc: '', subject: 'Fwd: Original', forwardMessageId: 17 });
    render(<ComposeView />); fireEvent.click(screen.getByTestId('compose-send'));
    await waitFor(() => expect(api.forwardMessage).toHaveBeenCalledWith(17, expect.objectContaining({ from: 'chris@test.invalid', to: ['alex@test.invalid'] })));
    expect(api.sendMessage).not.toHaveBeenCalled();
  });
  it('disables native attachment selection until the server capability exists', () => {
    const pickAttachments = vi.fn(); configureMailPlatform({ desktop: true, pickAttachments });
    render(<ComposeView />);
    const button = screen.getByTestId('compose-attach') as HTMLButtonElement;
    expect(button.disabled).toBe(true); expect(button.title).toContain('#2212');
    fireEvent.click(button); expect(pickAttachments).not.toHaveBeenCalled();
  });
  it('preserves reply headers in server drafts', async () => {
    useUIStore.getState().startCompose({ to: 'alex@test.invalid', cc: '', bcc: '', subject: 'Re: Hello', inReplyTo: '<original@test.invalid>' });
    render(<ComposeView />); fireEvent.click(screen.getByTestId('compose-save'));
    await waitFor(() => expect(api.createDraft).toHaveBeenCalledWith(expect.objectContaining({ in_reply_to: '<original@test.invalid>' })));
  });
  it('sends Bcc directly rather than losing recipients through the draft API', async () => {
    useUIStore.getState().startCompose({ to: 'alex@test.invalid', cc: '', bcc: 'private@test.invalid', subject: 'Private copy', draftId: 88 });
    render(<ComposeView />);
    expect((screen.getByTestId('compose-save') as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId('compose-send'));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith(expect.objectContaining({ bcc: ['private@test.invalid'] })));
    expect(api.sendDraft).not.toHaveBeenCalled();
  });
  it('reports dirty state and clears it only after saving through the existing draft path', async () => {
    const onStateChange = vi.fn();
    let save: () => Promise<boolean> = async () => false;
    render(<ComposeView onStateChange={onStateChange} onSaveHandler={handler => { save = handler; }} />);
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true, canSave: true })));
    expect(await save()).toBe(true);
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: false })));
    fireEvent.change(screen.getByTestId('compose-subject'), { target: { value: 'Changed' } });
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true })));
  });
  it('keeps the dirty window open if saving fails', async () => {
    vi.mocked(api.createDraft).mockRejectedValueOnce(new Error('Offline'));
    const onStateChange = vi.fn();
    let save: () => Promise<boolean> = async () => false;
    render(<ComposeView onStateChange={onStateChange} onSaveHandler={handler => { save = handler; }} />);
    expect(await save()).toBe(false);
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true, busy: false })));
    expect(screen.getByTestId('compose-subject')).toBeTruthy();
  });
  it('uses the native completion callback only after a successful send', async () => {
    const onSent = vi.fn();
    const onClose = vi.fn();
    render(<ComposeView onSent={onSent} onClose={onClose} />);
    fireEvent.click(screen.getByTestId('compose-send'));
    await waitFor(() => expect(onSent).toHaveBeenCalledOnce());
    expect(onClose).not.toHaveBeenCalled();
  });
  it('marks even an entirely cleared existing draft as dirty', async () => {
    useUIStore.getState().startCompose({ to: '', cc: '', bcc: '', subject: 'Saved', draftId: 88 });
    const onStateChange = vi.fn();
    render(<ComposeView onStateChange={onStateChange} />);
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: false })));
    fireEvent.change(screen.getByTestId('compose-subject'), { target: { value: '' } });
    await waitFor(() => expect(onStateChange).toHaveBeenLastCalledWith(expect.objectContaining({ dirty: true })));
  });
});
