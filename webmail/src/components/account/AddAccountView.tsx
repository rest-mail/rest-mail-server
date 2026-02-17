import { useState } from 'react';
import { toast } from 'sonner';
import { useUIStore } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { runDetection, detectionTests, type DetectionProgress } from '@/hooks/useAccountDetection';
import * as api from '@/api/client';

export function AddAccountView() {
  const { setView } = useUIStore();
  const { loadAccounts } = useMailStore();

  const [address, setAddress] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [progress, setProgress] = useState<DetectionProgress | null>(null);

  const handleTest = async () => {
    if (!address.trim() || !password.trim()) {
      toast.error('Email address and password are required');
      return;
    }
    setTesting(true);
    setProgress(null);

    const result = await runDetection(address, password, setProgress);

    if (result.success) {
      toast.success(`Connection OK via ${result.method}`);
      if (!displayName && result.display_name) {
        setDisplayName(result.display_name);
      }
    } else {
      toast.error(result.error || 'All connection tests failed');
    }
    setTesting(false);
  };

  const handleSave = async () => {
    if (!address.trim() || !password.trim()) {
      toast.error('Email address and password are required');
      return;
    }
    setSaving(true);
    try {
      await api.linkAccount({ address, password, display_name: displayName });
      toast.success('Account linked successfully');
      await loadAccounts();
      setView('mail');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to link account');
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
          <div className="space-y-2">
            <Label>Display Name</Label>
            <Input
              value={displayName}
              onChange={e => setDisplayName(e.target.value)}
              placeholder="Alice Smith (optional)"
            />
          </div>

          {/* Detection progress */}
          {testing && progress && (
            <div className="text-sm text-muted-foreground space-y-1">
              <p>Testing: {progress.currentTest}...</p>
              <div className="flex gap-1 flex-wrap">
                {detectionTests.map(t => {
                  const done = progress.completedTests.includes(t.id);
                  const active = progress.currentTest === t.label && !done;
                  return (
                    <span
                      key={t.id}
                      className={`text-xs px-2 py-0.5 rounded-full ${
                        done
                          ? progress.result?.success && progress.result?.method === t.id
                            ? 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200'
                            : 'bg-muted text-muted-foreground line-through'
                          : active
                            ? 'bg-primary/10 text-primary'
                            : 'bg-muted text-muted-foreground'
                      }`}
                    >
                      {t.label}
                    </span>
                  );
                })}
              </div>
            </div>
          )}

          <div className="flex items-center gap-2 pt-2">
            <Button variant="outline" onClick={handleTest} disabled={testing}>
              {testing ? 'Testing...' : 'Test Connection'}
            </Button>
            <div className="flex-1" />
            <Button variant="ghost" onClick={() => setView('mail')}>Cancel</Button>
            <Button onClick={handleSave} disabled={saving}>
              {saving ? 'Saving...' : 'Save'}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
