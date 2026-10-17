import { useState } from 'react';
import { toast } from 'sonner';
import { useUIStore } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import * as api from '@/api/client';

// AddAccountView links an existing account by its password: the same proof as
// signing in to it, its 2FA code included, after which this account owns it.
export function AddAccountView() {
  const { setView } = useUIStore();
  const { loadAccounts } = useMailStore();

  const [address, setAddress] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [code, setCode] = useState('');
  const [needsCode, setNeedsCode] = useState(false);
  const [saving, setSaving] = useState(false);

  const handleSave = async () => {
    if (!address.trim() || !password.trim()) {
      toast.error('Email address and password are required');
      return;
    }
    if (needsCode && !code.trim()) {
      toast.error('Enter the code from that account\'s authenticator');
      return;
    }
    setSaving(true);
    try {
      await api.linkAccount({
        address,
        password,
        display_name: displayName,
        ...(needsCode ? { totp_code: code.trim() } : {}),
      });
      toast.success('Account linked successfully');
      await loadAccounts();
      setView('mail');
    } catch (err) {
      if (err instanceof api.ApiError && err.code === 'totp_required') {
        setNeedsCode(true);
        toast.message('That account has two-factor authentication: enter its code');
      } else {
        toast.error(err instanceof Error ? err.message : 'Failed to link account');
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="h-full overflow-y-auto p-6">
      <Card className="max-w-lg mx-auto">
        <CardHeader>
          <CardTitle>Add Account</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label>Email Address</Label>
            <Input
              value={address}
              onChange={e => setAddress(e.target.value)}
              placeholder="alice@mail1.test"
            />
          </div>
          <div className="space-y-2">
            <Label>Password</Label>
            <Input
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
            />
          </div>
          {needsCode && (
            <div className="space-y-2">
              <Label>Authenticator code</Label>
              <Input
                value={code}
                inputMode="numeric"
                autoComplete="one-time-code"
                onChange={e => setCode(e.target.value)}
              />
            </div>
          )}
          <div className="space-y-2">
            <Label>Display Name</Label>
            <Input
              value={displayName}
              onChange={e => setDisplayName(e.target.value)}
              placeholder="Alice Smith (optional)"
            />
          </div>

          <div className="flex items-center gap-2 pt-2">
            <div className="flex-1" />
            <Button variant="ghost" onClick={() => setView('mail')}>Cancel</Button>
            <Button onClick={handleSave} disabled={saving}>
              {saving ? 'Linking...' : 'Link'}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
