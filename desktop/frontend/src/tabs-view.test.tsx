import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { useAuthStore } from '@webmail/stores/authStore';
import { tabs } from './tabs';
import type { SessionBinding } from './session';

vi.mock('@webmail/App', () => ({ default: ({ showToasts }: { showToasts?: boolean }) => <div data-testid="mail-stub">{String(showToasts)}</div> }));
vi.mock('@webmail/components/compose/ComposeView', () => ({ ComposeView: () => <div>Draft editor</div> }));
vi.mock('./native', () => ({ native: { openCompose: vi.fn(), requestClose: vi.fn() } }));
vi.mock('sonner', () => ({ Toaster: () => <div data-testid="desktop-toasts" /> }));
import App from './App';

describe('desktop tab notification surface', () => {
  it('keeps one global toast surface outside the hidden mailbox while composing', () => {
    useAuthStore.setState({ isAuthenticated: true, setupRequired: [] });
    tabs.clear(); tabs.open({ to: '', cc: '', bcc: '', subject: 'Draft' });
    render(<App session={{} as SessionBinding} events={{ on: () => () => {}, emit: vi.fn() }} />);
    expect(screen.getByTestId('mail-stub').closest('[hidden]')).not.toBeNull();
    expect(screen.getByTestId('mail-stub').textContent).toBe('false');
    expect(screen.getAllByTestId('desktop-toasts')).toHaveLength(1);
    expect(screen.getByTestId('desktop-toasts').closest('[hidden]')).toBeNull();
  });
});
