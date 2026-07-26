import { useState, type FormEvent } from 'react';
import { Eye, EyeOff } from 'lucide-react';
import { useAuthStore } from '@/stores/authStore';

export function LoginPage() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [code, setCode] = useState('');
  const [useRecovery, setUseRecovery] = useState(false);
  const { login, isLoading, error, totpRequired, resetTotpChallenge } = useAuthStore();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (totpRequired) {
      const trimmed = code.trim();
      if (!trimmed) return;
      await login(email, password, useRecovery ? { recovery_code: trimmed } : { totp_code: trimmed });
    } else {
      await login(email, password);
    }
  };

  const backToCredentials = () => {
    resetTotpChallenge();
    setCode('');
    setUseRecovery(false);
  };

  return (
    <div className="h-full flex items-center justify-center bg-background">
      <div className="w-[340px] rounded-2xl bg-card p-6 space-y-5">
        {/* Header */}
        <div className="flex flex-col items-center gap-2">
          {/* Icon */}
          <div className="w-10 h-10 rounded-2xl bg-secondary flex items-center justify-center text-lg">
            ✉
          </div>
          {/* Title */}
          <h1 className="font-heading-oswald text-2xl font-bold tracking-wider text-foreground">
            REST MAIL
          </h1>
          {/* Subtitle */}
          <p className="font-mono text-[11px] text-muted-foreground">
            {totpRequired ? '// two_factor_authentication' : '// sign_in_to_your_account'}
          </p>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-3">
          {!totpRequired && (
            <>
              {/* Email field */}
              <div className="space-y-1">
                <label className="font-mono text-xs font-medium text-muted-foreground">
                  email
                </label>
                <div className="flex items-center h-9 rounded-2xl bg-secondary px-3">
                  <input
                    type="email"
                    value={email}
                    onChange={e => setEmail(e.target.value)}
                    placeholder="alice@restmail.dev"
                    autoFocus
                    required
                    className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
                  />
                </div>
              </div>

              {/* Password field */}
              <div className="space-y-1">
                <label className="font-mono text-xs font-medium text-muted-foreground">
                  password
                </label>
                <div className="flex items-center h-9 rounded-2xl bg-secondary px-3">
                  <input
                    type={showPassword ? 'text' : 'password'}
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    placeholder="••••••••"
                    required
                    className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword(!showPassword)}
                    className="text-muted-foreground hover:text-foreground shrink-0 ml-2"
                  >
                    {showPassword ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                  </button>
                </div>
              </div>
            </>
          )}

          {totpRequired && (
            /* Second step: the account has 2FA active, so the API asked for a
               code. Credentials are kept in state and resubmitted with it. */
            <div className="space-y-1">
              <label className="font-mono text-xs font-medium text-muted-foreground">
                {useRecovery ? 'recovery_code' : 'authenticator_code'}
              </label>
              <div className="flex items-center h-9 rounded-2xl bg-secondary px-3">
                <input
                  type="text"
                  value={code}
                  onChange={e => setCode(e.target.value)}
                  placeholder={useRecovery ? 'one of your recovery codes' : '123456'}
                  inputMode={useRecovery ? 'text' : 'numeric'}
                  autoComplete="one-time-code"
                  autoFocus
                  required
                  className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
                />
              </div>
              <div className="flex items-center justify-between pt-1">
                <button
                  type="button"
                  onClick={() => { setUseRecovery(v => !v); setCode(''); }}
                  className="font-mono text-[11px] text-primary hover:underline"
                >
                  {useRecovery ? 'use_authenticator_code' : 'use_recovery_code'}
                </button>
                <button
                  type="button"
                  onClick={backToCredentials}
                  className="font-mono text-[11px] text-muted-foreground hover:text-foreground"
                >
                  &larr; back
                </button>
              </div>
            </div>
          )}

          {/* Error */}
          {error && (
            <p className="font-mono text-xs text-destructive">// error: {error}</p>
          )}

          {/* Sign in button */}
          <button
            type="submit"
            disabled={isLoading}
            className="w-full h-10 rounded-2xl bg-primary text-primary-foreground font-mono text-xs font-semibold hover:opacity-90 transition-opacity disabled:opacity-50"
          >
            {isLoading
              ? (totpRequired ? '// verifying...' : '// signing_in...')
              : (totpRequired ? 'verify' : 'sign_in')}
          </button>
        </form>
      </div>
    </div>
  );
}
