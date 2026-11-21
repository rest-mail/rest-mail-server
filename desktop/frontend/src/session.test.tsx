import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn().mockResolvedValue({}) }, Events: { On: vi.fn(), Emit: vi.fn() } }));
import { Call } from '@wailsio/runtime';
import { session, type SessionBinding } from './session';
import { SignIn } from './SignIn';
import { useAuthStore } from '@webmail/stores/authStore';

describe('native Session integration', () => {
  it('adds HTTPS for a bare server host and reports credential errors inline', async () => {
    const binding: SessionBinding = {
      SignIn: vi.fn().mockRejectedValue(new Error('401 Unauthorized')), SubmitTOTP: vi.fn(), Status: vi.fn(), SignOut: vi.fn(),
    };
    render(<SignIn session={binding} />);
    fireEvent.change(screen.getByTestId('signin-server'), { target: { value: 'mail.example.com' } });
    fireEvent.change(screen.getByTestId('signin-email'), { target: { value: 'chris@test.invalid' } });
    fireEvent.change(screen.getByTestId('signin-password'), { target: { value: 'password' } });
    fireEvent.submit(screen.getByTestId('signin-submit').closest('form')!);
    expect((await screen.findByRole('alert')).textContent).toBe('Wrong email or password.');
    expect(binding.SignIn).toHaveBeenCalledWith('https://mail.example.com', 'chris@test.invalid', 'password');
  });
  it('calls actual Wails main package bindings without an HTTP login', async () => {
    await session.SignIn('https://mail.test', 'chris@test.invalid', 'password');
    expect(Call.ByName).toHaveBeenCalledWith('main.Session.SignIn', 'https://mail.test', 'chris@test.invalid', 'password');
    await session.SubmitTOTP('123456'); expect(Call.ByName).toHaveBeenCalledWith('main.Session.SubmitTOTP', '123456');
    await session.Status(); expect(Call.ByName).toHaveBeenCalledWith('main.Session.Status');
    await session.SignOut(); expect(Call.ByName).toHaveBeenCalledWith('main.Session.SignOut');
  });
  it('shows the TOTP challenge and accepts the native identity, never a token', async () => {
    useAuthStore.setState({ user: null, isAuthenticated: false });
    const binding: SessionBinding = {
      SignIn: vi.fn().mockResolvedValue({ NeedsTOTP: true }), SubmitTOTP: vi.fn().mockResolvedValue(undefined),
      Status: vi.fn().mockResolvedValue({ SignedIn: true, Server: 'https://mail.test', Email: 'chris@test.invalid' }), SignOut: vi.fn(),
    };
    render(<SignIn session={binding} />);
    fireEvent.change(screen.getByTestId('signin-server'), { target: { value: 'https://mail.test' } });
    fireEvent.change(screen.getByTestId('signin-email'), { target: { value: 'chris@test.invalid' } });
    fireEvent.change(screen.getByTestId('signin-password'), { target: { value: 'password' } });
    fireEvent.submit(screen.getByTestId('signin-submit').closest('form')!);
    await screen.findByTestId('signin-totp');
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
    expect(screen.queryByTestId('signin-password')).toBeNull();
    fireEvent.change(screen.getByTestId('signin-totp'), { target: { value: '123456' } });
    fireEvent.submit(screen.getByTestId('signin-submit').closest('form')!);
    await waitFor(() => expect(useAuthStore.getState().isAuthenticated).toBe(true));
    expect(binding.SubmitTOTP).toHaveBeenCalledWith('123456');
    expect(useAuthStore.getState().user?.email).toBe('chris@test.invalid');
    expect(JSON.stringify(useAuthStore.getState())).not.toContain('password');
  });
});
