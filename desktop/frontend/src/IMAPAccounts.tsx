import { useEffect, useState, type FormEvent } from 'react';
import { Call } from '@wailsio/runtime';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { identity, type SessionBinding } from './session';

export interface IMAPAccount {
  id: number;
  type: 'imap' | 'restmail';
  address: string;
  host: string;
  port: number;
  username: string;
  smtp_host: string;
  smtp_port: number;
}

export interface IMAPAccountInput {
  host: string; port: number; username: string; password: string;
  smtp_host: string; smtp_port: number; address: string;
}

export const accounts = {
  add: (input: IMAPAccountInput) => Call.ByName('main.Accounts.AddIMAPAccount', input) as Promise<IMAPAccount>,
  list: () => Call.ByName('main.Accounts.ListAccounts') as Promise<IMAPAccount[]>,
  remove: (id: number) => Call.ByName('main.Accounts.RemoveIMAPAccount', id) as Promise<void>,
};

export function IMAPAccountSheet({ session, onClose }: { session: SessionBinding; onClose: () => void }) {
  const [host, setHost] = useState('');
  const [port, setPort] = useState(993);
  const [smtpHost, setSMTPHost] = useState('');
  const [smtpPort, setSMTPPort] = useState(587);
  const [username, setUsername] = useState('');
  const [address, setAddress] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('');
    try {
      await accounts.add({ host: host.trim(), port, username: username.trim(), password, smtp_host: smtpHost.trim(), smtp_port: smtpPort, address: address.trim() });
      setPassword('');
      const user = identity(await session.Status());
      useAuthStore.setState({ user, isAuthenticated: !!user, sessionChecked: true, setupRequired: [] });
      await useMailStore.getState().loadAccounts();
      onClose();
    } catch {
      setPassword('');
      setError('Could not add account. Check server settings, credentials and system keychain access. Both servers must support verified TLS.');
    } finally { setBusy(false); }
  }

  return <div role="dialog" aria-modal="true" aria-label="Add IMAP account" style={{ position: 'fixed', inset: 0, zIndex: 100, background: 'var(--background)', overflowY: 'auto', padding: 32 }}>
    <form onSubmit={submit} className="desktop-signin-sheet" style={{ margin: 'auto', maxWidth: 480 }}>
      <h2>Add IMAP account</h2>
      <p>Passwords stay in the system keychain. IMAP uses TLS on 993 or STARTTLS on 143; SMTP uses STARTTLS on 587 or TLS on 465.</p>
      <label>Email address<input aria-label="IMAP email address" className="desktop-input" type="email" value={address} onChange={event => setAddress(event.target.value)} required /></label>
      <label>Username<input aria-label="IMAP username" className="desktop-input" autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} required /></label>
      <label>Password<input aria-label="IMAP password" data-ui-sensitive className="desktop-input" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required /></label>
      <label>IMAP server<input aria-label="IMAP server" className="desktop-input" value={host} onChange={event => setHost(event.target.value)} required /></label>
      <label>IMAP port<select aria-label="IMAP port" className="desktop-input" value={port} onChange={event => setPort(Number(event.target.value))}><option value={993}>993 — TLS</option><option value={143}>143 — STARTTLS</option></select></label>
      <label>SMTP server<input aria-label="SMTP server" className="desktop-input" value={smtpHost} onChange={event => setSMTPHost(event.target.value)} required /></label>
      <label>SMTP port<select aria-label="SMTP port" className="desktop-input" value={smtpPort} onChange={event => setSMTPPort(Number(event.target.value))}><option value={587}>587 — STARTTLS</option><option value={465}>465 — TLS</option></select></label>
      <button className="desktop-primary" disabled={busy}>{busy ? 'Connecting…' : 'Add IMAP account'}</button>
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
      {error && <p role="alert">{error}</p>}
    </form>
  </div>;
}

export function DesktopAccountChoice({ session }: { session: SessionBinding }) {
  const view = useUIStore(state => state.view);
  const [choice, setChoice] = useState<'choose' | 'rest' | 'imap'>('choose');
  const signedIn = useAuthStore(state => state.isAuthenticated);
  useEffect(() => { setChoice('choose'); }, [view]);
  if (!signedIn || view !== 'addAccount' || choice === 'rest') return null;
  const close = () => { setChoice('choose'); useUIStore.getState().setView('mail'); };
  if (choice === 'imap') return <IMAPAccountSheet session={session} onClose={close} />;
  return <div role="dialog" aria-modal="true" aria-label="Account type" style={{ position: 'fixed', inset: 0, zIndex: 100, background: 'var(--background)', padding: 32 }}>
    <h2>Add account</h2>
    <button onClick={() => setChoice('rest')}>REST Mail</button>{' '}
    <button onClick={() => setChoice('imap')}>IMAP and SMTP</button>{' '}
    <button onClick={close}>Cancel</button>
  </div>;
}
