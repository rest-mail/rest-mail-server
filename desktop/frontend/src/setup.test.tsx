import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useAuthStore } from '@webmail/stores/authStore';
import { configureMailPlatform } from '@webmail/platform';
import { AuthenticatorSetup } from '@webmail/components/auth/AuthenticatorSetup';
import { FirstRunSetup } from './FirstRunSetup';
import { identity, type SessionBinding, type SessionStatus } from './session';
import App from './App';

vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() }, Events: { On: vi.fn(), Emit: vi.fn() } }));
vi.mock('@webmail/App', () => ({ default: () => <div data-testid="mailbox">Mailbox</div> }));

function bindings(): SessionBinding {
  return {
    SignIn: vi.fn(), SubmitTOTP: vi.fn(), SignOut: vi.fn().mockResolvedValue(undefined),
    ChangePassword: vi.fn(), EnrollTwoFactor: vi.fn(), ConfirmTwoFactor: vi.fn(),
    Status: vi.fn().mockResolvedValue({ SignedIn: true, Server: 'https://mail.test', Email: 'fresh@example.test', SetupRequired: ['two_factor'] }),
  };
}

function fillPassword(replacement = 'A long secure password') {
  fireEvent.change(screen.getByTestId('setup-current-password'), { target: { value: 'Temporary password' } });
  fireEvent.change(screen.getByTestId('setup-new-password'), { target: { value: replacement } });
  fireEvent.change(screen.getByTestId('setup-confirm-password'), { target: { value: replacement } });
}

describe('native first-run account setup', () => {
  beforeEach(() => {
    configureMailPlatform({ desktop: true });
    useAuthStore.setState({ user: { id: 0, email: 'fresh@example.test', display_name: '' }, isAuthenticated: true, sessionChecked: true, setupRequired: ['password', 'two_factor'] });
  });

  it('restores server setup requirements instead of opening mail', async () => {
    const status: SessionStatus = { SignedIn: true, Server: 'https://mail.test', Email: 'fresh@example.test', SetupRequired: ['password', 'two_factor'] };
    configureMailPlatform({ desktop: true, restoreSession: async () => identity(status) });
    useAuthStore.setState({ setupRequired: [] });
    await useAuthStore.getState().checkSession();
    render(<App session={bindings()} />);
    expect(screen.getByTestId('setup-current-password')).toBeTruthy();
    expect(screen.queryByTestId('mailbox')).toBeNull();
  });

  it('changes password through Go, acknowledges codes and confirms enrollment before mail', async () => {
    const session = bindings();
    vi.mocked(session.ChangePassword).mockResolvedValue({ NeedsTOTP: false, SetupRequired: ['two_factor'] });
    vi.mocked(session.EnrollTwoFactor).mockResolvedValue({ Secret: 'fixture-secret', OTPAuthURL: 'otpauth://totp/RESTMail?secret=JBSWY3DPEHPK3PXP', RecoveryCodes: ['fixture-recovery-code'] });
    vi.mocked(session.ConfirmTwoFactor).mockImplementation(async () => { vi.mocked(session.Status).mockResolvedValue({ SignedIn: true, Server: 'https://mail.test', Email: 'fresh@example.test', SetupRequired: [] }); });
    render(<App session={session} />);
    fillPassword(); fireEvent.submit(screen.getByTestId('setup-change-password').closest('form')!);
    fireEvent.click(await screen.findByTestId('setup-enroll'));
    expect(session.ChangePassword).toHaveBeenCalledWith('Temporary password', 'A long secure password');
    const qrCode = await screen.findByRole('img', { name: 'Authenticator setup QR code' });
    expect(qrCode.getAttribute('width')).toBe('200');
    expect(qrCode.getAttribute('height')).toBe('200');
    expect(qrCode.querySelector('path')?.getAttribute('fill')).toBe('#FFFFFF');
    expect(screen.getByTestId('setup-recovery-codes').textContent).toContain('fixture-recovery-code');
    expect(screen.getByTestId('setup-authenticator').getAttribute('data-ui-sensitive')).toBe('true');
    expect((screen.getByTestId('setup-verify') as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByTestId('setup-totp'), { target: { value: '123456' } });
    fireEvent.submit(screen.getByTestId('setup-verify').closest('form')!);
    expect(session.ConfirmTwoFactor).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId('setup-codes-saved'));
    fireEvent.submit(screen.getByTestId('setup-verify').closest('form')!);
    expect(await screen.findByTestId('mailbox')).toBeTruthy();
    expect(session.ConfirmTwoFactor).toHaveBeenCalledWith('123456');
    expect(useAuthStore.getState().setupRequired).toEqual([]);
    expect(JSON.stringify(useAuthStore.getState())).not.toContain('fixture-secret');
  });

  it('displays server field refusals verbatim and validates confirmation', async () => {
    const session = bindings();
    vi.mocked(session.ChangePassword).mockRejectedValue(new Error('New password must not contain the account name'));
    render(<FirstRunSetup session={session} onFinished={vi.fn()} />);
    fillPassword();
    fireEvent.change(screen.getByTestId('setup-confirm-password'), { target: { value: 'Different password' } });
    fireEvent.submit(screen.getByTestId('setup-change-password').closest('form')!);
    expect(screen.getByRole('alert').textContent).toBe('The new passwords do not match.');
    expect(session.ChangePassword).not.toHaveBeenCalled();
    fillPassword(); fireEvent.submit(screen.getByTestId('setup-change-password').closest('form')!);
    expect((await screen.findByRole('alert')).textContent).toBe('New password must not contain the account name');
    expect(useAuthStore.getState().setupRequired).toEqual(['password', 'two_factor']);
  });

  it('keeps the setup screen mounted across password reauthentication with TOTP', async () => {
    const session = bindings();
    vi.mocked(session.ChangePassword).mockImplementation(async () => {
      useAuthStore.setState({ user: null, isAuthenticated: false, setupRequired: [] });
      return { NeedsTOTP: true };
    });
    render(<App session={session} />);
    fillPassword(); fireEvent.submit(screen.getByTestId('setup-change-password').closest('form')!);
    expect(await screen.findByTestId('setup-totp')).toBeTruthy();
    expect(screen.queryByTestId('mailbox')).toBeNull();
    fireEvent.change(screen.getByTestId('setup-totp'), { target: { value: '123456' } });
    fireEvent.submit(screen.getByTestId('setup-verify').closest('form')!);
    await waitFor(() => expect(session.SubmitTOTP).toHaveBeenCalledWith('123456'));
    expect(await screen.findByTestId('setup-enroll')).toBeTruthy();
  });

  it('clears the setup session when cancelled', async () => {
    const finished = vi.fn();
    const session = bindings();
    render(<FirstRunSetup session={session} onFinished={finished} />);
    fireEvent.click(screen.getByTestId('setup-signout'));
    await waitFor(() => expect(finished).toHaveBeenCalledOnce());
    expect(session.SignOut).toHaveBeenCalledOnce();
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
  });

  it('preserves the shared webmail QR size and white quiet zone', () => {
    render(<AuthenticatorSetup secret="fixture-secret" otpauthUrl="otpauth://totp/RESTMail?secret=JBSWY3DPEHPK3PXP" />);
    const qrCode = screen.getByRole('img', { name: 'Authenticator setup QR code' });
    expect(qrCode.getAttribute('width')).toBe('256');
    expect(qrCode.getAttribute('height')).toBe('256');
    const paths = qrCode.querySelectorAll('path');
    expect(paths[0].getAttribute('fill')).toBe('#FFFFFF');
    expect(paths[1].getAttribute('d')).toMatch(/^M4 4h/);
  });
});
