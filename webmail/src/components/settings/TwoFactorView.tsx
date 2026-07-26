import { useCallback, useEffect, useState } from 'react';
import { ShieldCheck, ShieldOff, Copy, Check, Loader2, KeyRound } from 'lucide-react';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Input } from '@/components/ui/input';
import { Separator } from '@/components/ui/separator';
import {
  getTwoFactorStatus,
  enrollTwoFactor,
  confirmTwoFactor,
  disableTwoFactor,
  type TwoFactorStatus,
  type TwoFactorEnrollment,
} from '@/api/client';

/**
 * TwoFactorView surfaces the server's TOTP 2FA (two_factor.go) for the signed-in
 * account: it shows enabled/disabled status and drives the enable (enroll →
 * show secret + recovery codes → confirm a code) and disable (prove a code)
 * flows against the /auth/2fa endpoints. A dedicated QR image is intentionally
 * omitted; the base32 secret and otpauth URL are shown for manual entry.
 */
export function TwoFactorView() {
  const [status, setStatus] = useState<TwoFactorStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Enable flow
  const [enrollment, setEnrollment] = useState<TwoFactorEnrollment | null>(null);
  const [confirmCode, setConfirmCode] = useState('');

  // Disable flow
  const [disabling, setDisabling] = useState(false);
  const [disableCode, setDisableCode] = useState('');
  const [disableUseRecovery, setDisableUseRecovery] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const { data } = await getTwoFactorStatus();
      setStatus(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load 2FA status');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function startEnroll() {
    setBusy(true);
    setError(null);
    try {
      const { data } = await enrollTwoFactor();
      setEnrollment(data);
      setConfirmCode('');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to start enrollment');
    } finally {
      setBusy(false);
    }
  }

  async function confirmEnroll() {
    const code = confirmCode.trim();
    if (!code) return;
    setBusy(true);
    setError(null);
    try {
      await confirmTwoFactor(code);
      setEnrollment(null);
      setConfirmCode('');
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Invalid code');
    } finally {
      setBusy(false);
    }
  }

  function cancelEnroll() {
    setEnrollment(null);
    setConfirmCode('');
    setError(null);
  }

  async function disable() {
    const code = disableCode.trim();
    if (!code) return;
    setBusy(true);
    setError(null);
    try {
      await disableTwoFactor(disableUseRecovery ? { recovery_code: code } : { totp_code: code });
      setDisabling(false);
      setDisableCode('');
      setDisableUseRecovery(false);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Invalid code');
    } finally {
      setBusy(false);
    }
  }

  const enabled = status?.enabled ?? false;

  return (
    <div className="max-w-2xl space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            {enabled ? <ShieldCheck className="w-4 h-4 text-primary" /> : <ShieldOff className="w-4 h-4" />}
            Two-Factor Authentication
            <span
              className={
                'ml-auto text-xs font-medium px-2 py-0.5 rounded-full ' +
                (enabled ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground')
              }
            >
              {loading ? 'checking…' : enabled ? 'Enabled' : 'Disabled'}
            </span>
          </CardTitle>
          <CardDescription>
            Require a time-based one-time code from an authenticator app when signing in to your
            account.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          {loading ? (
            <p className="text-sm text-muted-foreground flex items-center gap-2">
              <Loader2 className="w-4 h-4 animate-spin" /> Loading…
            </p>
          ) : enabled ? (
            /* ── Enabled: offer disable ─────────────────────────────── */
            !disabling ? (
              <div className="flex items-center justify-between gap-4">
                <p className="text-sm text-muted-foreground">
                  Two-factor authentication is protecting your account.
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  className="border-destructive/50 text-destructive hover:bg-destructive hover:text-destructive-foreground"
                  onClick={() => { setDisabling(true); setError(null); }}
                >
                  Disable
                </Button>
              </div>
            ) : (
              <div className="space-y-3">
                <Label>
                  {disableUseRecovery ? 'Recovery code' : 'Authenticator code'}
                </Label>
                <Input
                  value={disableCode}
                  onChange={e => setDisableCode(e.target.value)}
                  placeholder={disableUseRecovery ? 'one of your recovery codes' : '123456'}
                  inputMode={disableUseRecovery ? 'text' : 'numeric'}
                  autoComplete="one-time-code"
                  className="font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  Confirm with a current code to turn 2FA off.{' '}
                  <button
                    type="button"
                    className="text-primary hover:underline"
                    onClick={() => { setDisableUseRecovery(v => !v); setDisableCode(''); }}
                  >
                    {disableUseRecovery ? 'Use an authenticator code' : 'Use a recovery code instead'}
                  </button>
                </p>
                <div className="flex gap-2">
                  <Button variant="destructive" size="sm" disabled={busy || !disableCode.trim()} onClick={disable}>
                    {busy ? 'Disabling…' : 'Confirm disable'}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => { setDisabling(false); setDisableCode(''); setDisableUseRecovery(false); setError(null); }}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            )
          ) : enrollment ? (
            /* ── Enrolling: show secret + recovery codes, confirm a code ── */
            <div className="space-y-4">
              <div className="space-y-1.5">
                <Label>1. Add this secret to your authenticator app</Label>
                <CopyRow value={enrollment.secret} mono />
                <p className="text-xs text-muted-foreground">
                  Or use the provisioning URL:
                </p>
                <CopyRow value={enrollment.otpauth_url} mono small />
              </div>

              <div className="space-y-1.5">
                <Label className="flex items-center gap-1.5">
                  <KeyRound className="w-3.5 h-3.5" /> 2. Save your recovery codes
                </Label>
                <p className="text-xs text-muted-foreground">
                  Store these somewhere safe. Each can be used once if you lose your authenticator.
                  They are shown only now.
                </p>
                <div className="grid grid-cols-2 gap-2 rounded-md border border-border p-3 bg-muted/40">
                  {enrollment.recovery_codes.map(c => (
                    <code key={c} className="text-xs font-mono">{c}</code>
                  ))}
                </div>
                <CopyRow value={enrollment.recovery_codes.join('\n')} label="Copy all recovery codes" />
              </div>

              <Separator />

              <div className="space-y-2">
                <Label>3. Enter a code from the app to finish</Label>
                <Input
                  value={confirmCode}
                  onChange={e => setConfirmCode(e.target.value)}
                  placeholder="123456"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  className="font-mono"
                />
                <div className="flex gap-2">
                  <Button size="sm" disabled={busy || !confirmCode.trim()} onClick={confirmEnroll}>
                    {busy ? 'Verifying…' : 'Enable 2FA'}
                  </Button>
                  <Button variant="outline" size="sm" onClick={cancelEnroll}>Cancel</Button>
                </div>
              </div>
            </div>
          ) : (
            /* ── Disabled: offer enable ─────────────────────────────── */
            <div className="flex items-center justify-between gap-4">
              <p className="text-sm text-muted-foreground">
                Add an extra layer of security to your account.
              </p>
              <Button size="sm" disabled={busy} onClick={startEnroll}>
                {busy ? 'Starting…' : 'Enable 2FA'}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ── Copy-to-clipboard row ────────────────────────────────────────────

function CopyRow({ value, label, mono, small }: {
  value: string;
  label?: string;
  mono?: boolean;
  small?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — the value is still visible to select manually */
    }
  }

  return (
    <div className="flex items-center gap-2">
      {!label && (
        <code
          className={
            'flex-1 min-w-0 truncate rounded-md border border-border bg-muted/40 px-3 py-1.5 ' +
            (mono ? 'font-mono ' : '') + (small ? 'text-[11px] ' : 'text-xs ')
          }
          title={value}
        >
          {value}
        </code>
      )}
      <Button variant="outline" size="sm" onClick={copy} className={label ? '' : 'shrink-0'}>
        {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
        {label ? <span className="ml-1.5">{copied ? 'Copied' : label}</span> : null}
      </Button>
    </div>
  );
}
