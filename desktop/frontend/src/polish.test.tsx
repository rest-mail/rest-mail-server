import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { readFileSync } from 'node:fs';
import { Sidebar } from '@webmail/components/layout/Sidebar';
import { configureMailPlatform } from '@webmail/platform';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { SignIn } from './SignIn';
import { FirstRunSetup } from './FirstRunSetup';
import type { SessionBinding } from './session';

vi.mock('@webmail/components/ui/scroll-area', () => ({ ScrollArea: ({ children }: { children: ReactNode }) => <div>{children}</div> }));

describe('desktop sheet spacing', () => {
  it('keeps an expanding sign-in error inside the same fixed brace composition', async () => {
    const session = { SignIn: vi.fn().mockRejectedValue(new Error('network')) } as unknown as SessionBinding;
    const { container } = render(<SignIn session={session} />);
    const composition = container.querySelector('.desktop-braced-sheet')!;
    expect(composition.querySelectorAll('.desktop-brace')).toHaveLength(2);
    fireEvent.submit(screen.getByTestId('signin-submit').closest('form')!);
    expect(composition.contains(await screen.findByRole('alert'))).toBe(true);
    expect(container.querySelector('.desktop-braced-sheet')).toBe(composition);
  });

  it('frames the taller setup sheet in the same scrolling composition', () => {
    useAuthStore.setState({ user: null, setupRequired: ['password'] });
    const { container } = render(<FirstRunSetup session={{} as SessionBinding} onFinished={vi.fn()} />);
    const composition = container.querySelector('.desktop-braced-sheet')!;
    expect(composition.querySelectorAll('.desktop-brace')).toHaveLength(2);
    expect(composition.querySelector('.desktop-setup-sheet')).not.toBeNull();
    expect(composition.contains(screen.getByTestId('setup-confirm-password'))).toBe(true);
  });

  it('keeps fixed brace overhang and gap around a sheet-relative scrollable composition', () => {
    const theme = readFileSync('src/theme.css', 'utf8');
    expect(theme).toMatch(/\.desktop-signin \{[^}]*--brace-overhang: 70px; --brace-gap: 24px; --brace-width: 160px;/);
    expect(theme).toMatch(/\.desktop-signin \{[^}]*overflow: auto;[^}]*padding: 32px;/);
    expect(theme).toMatch(/\.desktop-braced-sheet \{[^}]*flex-shrink: 0;[^}]*margin: auto 0; padding: var\(--brace-overhang\) 0;/);
    expect(theme).toMatch(/\.desktop-brace \{[^}]*position: absolute; top: 0; height: 100%;/);
    expect(theme).toMatch(/\.desktop-signin-sheet \{[^}]*flex-shrink: 0; margin: auto 0;/);
    expect(theme).toMatch(/\.desktop-setup-sheet \{[^}]*margin: auto;/);
  });
});

describe('desktop-only sidebar polish', () => {
  beforeEach(() => {
    useAuthStore.setState({ user: null, isAuthenticated: true, setupRequired: [] });
    useMailStore.setState({ accounts: [{ id: 1, address: 'admin@desktop.test', display_name: '', is_primary: true }, { id: 2, address: 'setup@desktop.test', display_name: '', is_primary: false }], activeAccountId: 1, activeFolder: 'INBOX', folders: [{ name: 'INBOX', unread: 2, total: 3 }], loadingFolders: false, loadFolders: vi.fn(), loadMessages: vi.fn() });
    useUIStore.setState({ sidebarCollapsed: {} });
  });

  it('gives desktop accounts accessible chevrons and all sidebar actions aligned row hooks', () => {
    configureMailPlatform({ desktop: true, serverHost: () => 'localhost:18080' });
    const { container } = render(<Sidebar />);
    const account = screen.getByRole('button', { name: 'admin@desktop.test' });
    expect(account.getAttribute('aria-expanded')).toBe('true');
    expect(account.querySelector('svg')).not.toBeNull();
    expect(screen.getByRole('button', { name: 'New folder' }).classList.contains('desktop-sidebar-action')).toBe(true);
    expect(screen.getByRole('button', { name: 'Add account' }).classList.contains('desktop-sidebar-action')).toBe(true);
    expect(screen.getByTestId('sidebar-tools').querySelector('svg')).not.toBeNull();
    expect(screen.getByTestId('folder-1-INBOX').getAttribute('aria-current')).toBe('page');
    expect(container.querySelectorAll('.desktop-account-row')).toHaveLength(2);
  });

  it('preserves webmail branding, text chevrons, actions, and original sidebar classes', () => {
    configureMailPlatform({});
    const { container } = render(<Sidebar />);
    expect(screen.getByRole('heading', { name: 'REST MAIL' })).not.toBeNull();
    expect(screen.getByRole('button', { name: '▾ admin@desktop.test' }).getAttribute('aria-expanded')).toBeNull();
    expect(screen.getByRole('button', { name: '[+ new_folder]' })).not.toBeNull();
    expect(screen.getByRole('button', { name: '[+ add_account]' }).classList.contains('justify-center')).toBe(true);
    expect(container.querySelector('[class*="desktop-"]')).toBeNull();
    expect(screen.queryByTestId('sidebar-tools')).toBeNull();
    expect(screen.queryByTestId('session-signout')).toBeNull();
  });
});
