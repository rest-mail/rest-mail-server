import { useState } from 'react';
import { Check, Copy, KeyRound, Loader2, Trash2 } from 'lucide-react';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { AppPassword, CreatedAppPassword } from '@/api/client';
import { formatCredentialDate, type AppPasswordRefusal } from '@/lib/appPasswords';

export interface AppPasswordsPanelProps {
  items: AppPassword[];
  loading: boolean;
  /** The app password just made, with its secret; shown this once. */
  created: CreatedAppPassword | null;
  confirmingRevoke: number | null;
  busy: boolean;
  error: AppPasswordRefusal | null;
  onCreate: (name: string, password: string) => void;
  onDismissCreated: () => void;
  onConfirmRevoke: (id: number | null) => void;
  onRevoke: (id: number) => void;
  onSignInAgain: () => void;
}

/**
 * AppPasswordsPanel lists the signed-in account's app passwords, makes a new
 * one (asking for a name and the current password again, as the API does) and
 * revokes one after a confirm. A new app password is shown once, with a copy
 * button; the list never holds a secret.
 */
export function AppPasswordsPanel(props: AppPasswordsPanelProps) {
  const { items, loading, created, confirmingRevoke, busy, error } = props;
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !password) return;
    props.onCreate(name.trim(), password);
    setPassword('');
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <KeyRound className="w-4 h-4" /> App Passwords
        </CardTitle>
        <CardDescription>
          A mail app such as Apple Mail, Outlook or Thunderbird signs in with your address and an app
          password. If your account uses two-factor authentication, your own password will not work
          there: make an app password for each app.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && (
          <div role="alert" className="space-y-2">
            <p className="text-sm text-destructive">{error.message}</p>
            {error.signIn && (
              <Button variant="outline" size="sm" onClick={props.onSignInAgain}>
                Sign in again
              </Button>
            )}
          </div>
        )}

        {created && (
          <div className="space-y-2 rounded-md border border-primary/40 bg-primary/5 p-3" aria-live="polite">
            <p className="text-sm font-medium">New app password for “{created.name}”</p>
            <p className="text-xs text-muted-foreground">
              Copy it into your mail app now. You won't see it again: if you lose it, revoke it and make
              a new one.
            </p>
            <CopySecret value={created.password} />
            <Button variant="outline" size="sm" onClick={props.onDismissCreated}>
              Done
            </Button>
          </div>
        )}

        <form className="space-y-3" onSubmit={submit}>
          <div className="space-y-1.5">
            <Label htmlFor="app-password-name">Name</Label>
            <Input
              id="app-password-name"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder="iPhone Mail"
              maxLength={100}
              autoComplete="off"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="app-password-current">Your current password</Label>
            <Input
              id="app-password-current"
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              autoComplete="current-password"
              aria-describedby="app-password-current-help"
            />
            <p id="app-password-current-help" className="text-xs text-muted-foreground">
              Entered again to confirm it is you.
            </p>
          </div>
          <Button type="submit" size="sm" disabled={busy || !name.trim() || !password}>
            {busy ? 'Making…' : 'Make app password'}
          </Button>
        </form>

        {loading ? (
          <p className="text-sm text-muted-foreground flex items-center gap-2">
            <Loader2 className="w-4 h-4 animate-spin" /> Loading…
          </p>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground">You have no app passwords.</p>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border" aria-label="App passwords">
            {items.map(item => (
              <li key={item.id} className="flex flex-wrap items-center justify-between gap-3 p-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium truncate">{item.name}</p>
                  <p className="text-xs text-muted-foreground">
                    Made {formatCredentialDate(item.created_at)}
                    {' · '}
                    {item.last_used_at ? `Last used ${formatCredentialDate(item.last_used_at)}` : 'Never used'}
                    {item.expires_at ? ` · Expires ${formatCredentialDate(item.expires_at)}` : ''}
                  </p>
                </div>
                {confirmingRevoke === item.id ? (
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">Apps using it will stop working.</span>
                    <Button variant="destructive" size="sm" disabled={busy} onClick={() => props.onRevoke(item.id)}>
                      {busy ? 'Revoking…' : 'Revoke'}
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => props.onConfirmRevoke(null)}>
                      Cancel
                    </Button>
                  </div>
                ) : (
                  <Button
                    variant="outline"
                    size="sm"
                    className="border-destructive/50 text-destructive hover:bg-destructive hover:text-destructive-foreground"
                    aria-label={`Revoke ${item.name}`}
                    onClick={() => props.onConfirmRevoke(item.id)}
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                    <span className="ml-1.5">Revoke</span>
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function CopySecret({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable: the value is still visible to select */
    }
  }

  return (
    <div className="flex items-center gap-2">
      <code className="flex-1 min-w-0 break-all rounded-md border border-border bg-muted/40 px-3 py-1.5 font-mono text-xs select-all">
        {value}
      </code>
      <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
        {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
        <span className="ml-1.5">{copied ? 'Copied' : 'Copy'}</span>
      </Button>
    </div>
  );
}
