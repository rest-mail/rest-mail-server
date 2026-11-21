import { useState, type FormEvent } from 'react';
import { useAuthStore } from '@webmail/stores/authStore';
import { identity, type SessionBinding } from './session';

export function SignIn({ session }: { session: SessionBinding }) {
  const [server, setServer] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [challenge, setChallenge] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('');
    try {
      if (challenge) await session.SubmitTOTP(totp);
      else {
        const normalizedServer = /^[a-z][a-z\d+.-]*:\/\//i.test(server) ? server : `https://${server}`;
        const result = await session.SignIn(normalizedServer, email, password);
        setPassword('');
        if (result.NeedsTOTP) { setChallenge(true); return; }
      }
      const user = identity(await session.Status());
      useAuthStore.setState({ user, isAuthenticated: !!user, sessionChecked: true, setupRequired: user?.setup_required ?? [] });
    } catch (err) {
      const message = err instanceof Error ? err.message : '';
      const host = server.replace(/^https?:\/\//, '').split('/')[0];
      setError(/keychain/i.test(message) ? "Can't save your sign-in securely. Check access to the system keychain." : challenge ? 'That code did not work. Try again.' : /401|credential|password|unauthor/i.test(message) ? 'Wrong email or password.' : `Can't reach ${host}.`);
    }
    finally { setBusy(false); }
  }
  return <main className="desktop-signin">
    <span aria-hidden="true" className="desktop-brace desktop-brace-left">{'{'}</span>
    <span aria-hidden="true" className="desktop-brace desktop-brace-right">{'}'}</span>
    <form onSubmit={submit} className="desktop-signin-sheet">
      <img src="/rest-mail-icon.svg" alt="" width="64" height="64" />
      <h1>REST Mail</h1>
      <p className="desktop-signin-description">{challenge ? 'Enter your authenticator code' : 'Sign in to your mail server'}</p>
      {challenge ? <label>Authenticator code<input data-testid="signin-totp" aria-label="Authenticator code" className="desktop-input desktop-totp" autoComplete="one-time-code" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={totp} onChange={event => setTotp(event.target.value)} required autoFocus /></label> : <>
        <label>Server<input data-testid="signin-server" aria-label="Server URL" className="desktop-input" type="text" inputMode="url" placeholder="mail.example.com" value={server} onChange={event => setServer(event.target.value)} required /></label>
        <label className="block">Email<input data-testid="signin-email" aria-label="Email" className="desktop-input" type="email" autoComplete="username" value={email} onChange={event => setEmail(event.target.value)} required /></label>
        <label className="block">Password<input data-testid="signin-password" aria-label="Password" className="desktop-input" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required /></label>
      </>}
      <button data-testid="signin-submit" className="desktop-primary" disabled={busy}>{busy && <span className="desktop-spinner" aria-hidden="true" />}{busy ? 'Signing in…' : challenge ? 'Verify' : 'Sign in'}</button>
      {error && <p role="alert" className="desktop-signin-error">{error}</p>}
      {challenge && <button type="button" className="desktop-signin-back" data-testid="signin-back" onClick={() => { setChallenge(false); setTotp(''); }}>Back to sign in</button>}
    </form>
  </main>;
}
