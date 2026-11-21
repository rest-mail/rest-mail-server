import { useState, type FormEvent } from 'react';
import { AuthenticatorSetup } from '@webmail/components/auth/AuthenticatorSetup';
import { useAuthStore } from '@webmail/stores/authStore';
import { identity, type SessionBinding, type TwoFactorEnrollment } from './session';

export function FirstRunSetup({ session, onFinished }: { session: SessionBinding; onFinished: () => void }) {
  const steps = useAuthStore(state => state.setupRequired);
  const [email] = useState(() => useAuthStore.getState().user?.email);
  const [current, setCurrent] = useState('');
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [challenge, setChallenge] = useState(false);
  const [enrollment, setEnrollment] = useState<TwoFactorEnrollment | null>(null);
  const [savedCodes, setSavedCodes] = useState(false);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const changingPassword = !challenge && steps[0] === 'password';
  const addingAuthenticator = !challenge && steps[0] === 'two_factor';

  async function sync() {
    const status = await session.Status();
    const user = identity(status);
    useAuthStore.setState({ user, isAuthenticated: !!user, sessionChecked: true, setupRequired: status.SetupRequired ?? [] });
    if (status.SignedIn && !status.SetupRequired?.length) onFinished();
  }

  async function run(action: () => Promise<void>) {
    setBusy(true); setError('');
    try { await action(); }
    catch (failure) { setError(failure instanceof Error ? failure.message : typeof failure === 'string' ? failure : 'Could not finish account setup. Try again.'); }
    finally { setBusy(false); }
  }

  function changePassword(event: FormEvent) {
    event.preventDefault();
    if (password !== confirmation) { setError('The new passwords do not match.'); return; }
    if (new TextEncoder().encode(password).length < 12) { setError('Use at least 12 characters for your password.'); return; }
    void run(async () => {
      const result = await session.ChangePassword(current, password);
      setCurrent(''); setPassword(''); setConfirmation('');
      setCode(''); setChallenge(result.NeedsTOTP);
      if (!result.NeedsTOTP) await sync();
    });
  }

  function verify(event: FormEvent) {
    event.preventDefault();
    if (!challenge && !savedCodes) return;
    void run(async () => {
      if (challenge) await session.SubmitTOTP(code.trim());
      else await session.ConfirmTwoFactor(code.trim());
      setCode(''); setChallenge(false); setEnrollment(null); setSavedCodes(false);
      await sync();
    });
  }

  return <main className="desktop-signin">
    <span aria-hidden="true" className="desktop-brace desktop-brace-left">{'{'}</span>
    <span aria-hidden="true" className="desktop-brace desktop-brace-right">{'}'}</span>
    <section className="desktop-signin-sheet desktop-setup-sheet" aria-labelledby="setup-heading">
      <h1 id="setup-heading">Finish account setup</h1>
      <p className="desktop-signin-description">{email}</p>
      <p className="desktop-setup-intro">{changingPassword ? 'First, choose a password only you know.' : challenge ? 'Enter your current authenticator code to continue.' : addingAuthenticator ? 'Protect your account with an authenticator before opening your mail.' : busy ? 'Finishing account setup…' : 'This server requires an additional setup step. Contact your administrator.'}</p>
      {changingPassword ? <form onSubmit={changePassword}>
        <label>Current password<input data-testid="setup-current-password" className="desktop-input" type="password" autoComplete="current-password" value={current} onChange={event => setCurrent(event.target.value)} required /></label>
        <label>New password<input data-testid="setup-new-password" className="desktop-input" type="password" autoComplete="new-password" minLength={12} value={password} onChange={event => setPassword(event.target.value)} required /></label>
        <label>Confirm new password<input data-testid="setup-confirm-password" className="desktop-input" type="password" autoComplete="new-password" minLength={12} value={confirmation} onChange={event => setConfirmation(event.target.value)} required /></label>
        <button data-testid="setup-change-password" className="desktop-primary" disabled={busy}>{busy ? 'Changing password…' : 'Change password'}</button>
      </form> : challenge || enrollment ? <form onSubmit={verify}>
        {enrollment && <>
          <div data-ui-sensitive="true" data-testid="setup-authenticator"><AuthenticatorSetup secret={enrollment.Secret} otpauthUrl={enrollment.OTPAuthURL} qrSize={200} /></div>
          <p className="desktop-setup-intro">Save these recovery codes somewhere safe. Each code works once if you lose access to your authenticator. They are shown only now.</p>
          <pre data-ui-sensitive="true" data-testid="setup-recovery-codes" className="desktop-recovery-codes">{enrollment.RecoveryCodes.join('\n')}</pre>
          <label className="desktop-setup-check"><input data-testid="setup-codes-saved" type="checkbox" checked={savedCodes} onChange={event => setSavedCodes(event.target.checked)} />I have saved my recovery codes</label>
        </>}
        <label>Authenticator code<input data-testid="setup-totp" data-ui-sensitive="true" className="desktop-input desktop-totp" autoComplete="one-time-code" inputMode="numeric" pattern="[0-9]{6}" maxLength={6} value={code} onChange={event => setCode(event.target.value)} required /></label>
        <button data-testid="setup-verify" className="desktop-primary" disabled={busy || (!challenge && !savedCodes)}>{busy ? 'Verifying…' : 'Verify and continue'}</button>
      </form> : addingAuthenticator ? <button data-testid="setup-enroll" className="desktop-primary" disabled={busy} onClick={() => void run(async () => { setEnrollment(await session.EnrollTwoFactor()); setCode(''); setSavedCodes(false); })}>{busy ? 'Starting…' : 'Set up authenticator'}</button> : null}
      {error && <p role="alert" className="desktop-signin-error">{error}</p>}
      <button data-testid="setup-signout" className="desktop-signin-back" disabled={busy} onClick={() => void run(async () => { await session.SignOut(); useAuthStore.setState({ user: null, isAuthenticated: false, setupRequired: [] }); onFinished(); })}>Back to sign in</button>
    </section>
  </main>;
}
