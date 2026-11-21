import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() }, Events: { On: vi.fn(), Emit: vi.fn() } }));
import { Call } from '@wailsio/runtime';
import { IMAPAccountSheet, DesktopAccountChoice, accounts } from './IMAPAccounts';
import { useAuthStore } from '@webmail/stores/authStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { useMailStore } from '@webmail/stores/mailStore';
import type { SessionBinding } from './session';

const binding: SessionBinding = {
  SignIn: vi.fn(), SubmitTOTP: vi.fn(), SignOut: vi.fn(), ChangePassword: vi.fn(), EnrollTwoFactor: vi.fn(), ConfirmTwoFactor: vi.fn(),
  Status: vi.fn().mockResolvedValue({ SignedIn: true, Server: 'imap+tls://imap.example.test', Email: 'reader@example.test' }),
};

beforeEach(() => {
  vi.mocked(Call.ByName).mockReset();
  useAuthStore.setState({ isAuthenticated: false, user: null });
  useUIStore.setState({ view: 'mail' });
  vi.spyOn(useMailStore.getState(), 'loadAccounts').mockResolvedValue();
});

describe('native IMAP account sheet', () => {
  it('sends credentials only to the native account binding and retains only identity', async () => {
    vi.mocked(Call.ByName).mockResolvedValue({ id: 4503599627370497, type: 'imap', address: 'reader@example.test' });
    const close = vi.fn();
    render(<IMAPAccountSheet session={binding} onClose={close} />);
    for (const [label, value] of [['IMAP email address', 'reader@example.test'], ['IMAP username', 'reader'], ['IMAP password', 'private-password'], ['IMAP server', 'imap.example.test'], ['SMTP server', 'smtp.example.test']]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.submit(screen.getByRole('button', { name: 'Add IMAP account' }).closest('form')!);
    await waitFor(() => expect(close).toHaveBeenCalled());
    expect(Call.ByName).toHaveBeenCalledWith('main.Accounts.AddIMAPAccount', { host: 'imap.example.test', port: 993, username: 'reader', password: 'private-password', smtp_host: 'smtp.example.test', smtp_port: 587, address: 'reader@example.test' });
    expect(JSON.stringify(useAuthStore.getState())).not.toContain('private-password');
    expect((screen.getByLabelText('IMAP password') as HTMLInputElement).value).toBe('');
  });

  it('does not echo upstream errors and clears the password after refusal', async () => {
    vi.mocked(Call.ByName).mockRejectedValue(new Error('server echoed private-password'));
    render(<IMAPAccountSheet session={binding} onClose={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('IMAP password'), { target: { value: 'private-password' } });
    fireEvent.submit(screen.getByRole('button', { name: 'Add IMAP account' }).closest('form')!);
    expect((await screen.findByRole('alert')).textContent).not.toContain('private-password');
    expect((screen.getByLabelText('IMAP password') as HTMLInputElement).value).toBe('');
  });

  it('uses the existing add account view without changing shared webmail', () => {
    useAuthStore.setState({ isAuthenticated: true }); useUIStore.setState({ view: 'addAccount' });
    render(<DesktopAccountChoice session={binding} />);
    expect(screen.getByRole('dialog', { name: 'Account type' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'IMAP and SMTP' }));
    expect(screen.getByRole('dialog', { name: 'Add IMAP account' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(useUIStore.getState().view).toBe('mail');
  });

  it('lists and removes through secret-free native bindings', async () => {
    vi.mocked(Call.ByName).mockResolvedValue([]);
    await accounts.list(); await accounts.remove(4503599627370497);
    expect(Call.ByName).toHaveBeenCalledWith('main.Accounts.ListAccounts');
    expect(Call.ByName).toHaveBeenCalledWith('main.Accounts.RemoveIMAPAccount', 4503599627370497);
  });
});
