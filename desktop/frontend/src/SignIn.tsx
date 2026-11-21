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
        const result = await session.SignIn(server, email, password);
        setPassword('');
        if (result.NeedsTOTP) { setChallenge(true); return; }
      }
      const user = identity(await session.Status());
      useAuthStore.setState({ user, isAuthenticated: !!user, sessionChecked: true });
    } catch (err) { setError(err instanceof Error ? err.message : 'Sign-in failed'); }
    finally { setBusy(false); }
  }
  return <main className="h-full flex items-center justify-center bg-background">
    <form onSubmit={submit} className="w-96 p-8 space-y-5 rounded-2xl border border-border bg-card">
      <img src="/rest-mail-icon.svg" alt="" width="56" height="56" />
      <h1 className="text-2xl font-semibold">REST Mail</h1>
      <p className="text-sm text-muted-foreground">{challenge ? 'Enter your authenticator code' : 'Sign in to your mail server'}</p>
      {challenge ? <label className="block">Authenticator code<input data-testid="signin-totp" aria-label="Authenticator code" className="desktop-input" autoComplete="one-time-code" inputMode="numeric" value={totp} onChange={event => setTotp(event.target.value)} required autoFocus /></label> : <>
        <label className="block">Server URL<input data-testid="signin-server" aria-label="Server URL" className="desktop-input" type="url" placeholder="https://mail.example.com" value={server} onChange={event => setServer(event.target.value)} required /></label>
        <label className="block">Email<input data-testid="signin-email" aria-label="Email" className="desktop-input" type="email" autoComplete="username" value={email} onChange={event => setEmail(event.target.value)} required /></label>
        <label className="block">Password<input data-testid="signin-password" aria-label="Password" className="desktop-input" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required /></label>
      </>}
      {error && <p role="alert" className="text-destructive">{error}</p>}
      <button data-testid="signin-submit" className="w-full rounded-xl bg-primary text-primary-foreground p-3" disabled={busy}>{busy ? 'Signing in…' : challenge ? 'Verify code' : 'Sign in'}</button>
      {challenge && <button type="button" data-testid="signin-back" onClick={() => { setChallenge(false); setTotp(''); }}>Back to sign in</button>}
    </form>
  </main>;
}
