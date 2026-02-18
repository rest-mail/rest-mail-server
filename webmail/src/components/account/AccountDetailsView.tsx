import { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import { useAuthStore } from '@/stores/authStore';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { getAccountQuota, type QuotaData } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Separator } from '@/components/ui/separator';

function formatBytes(bytes: number): string {
  const mb = bytes / (1024 * 1024);
  if (mb >= 1024) {
    return `${(mb / 1024).toFixed(1)} GB`;
  }
  return `${mb.toFixed(1)} MB`;
}

export function AccountDetailsView() {
  const { user } = useAuthStore();
  const { accounts, removeAccount } = useMailStore();
  const { setView } = useUIStore();
  const [quota, setQuota] = useState<QuotaData | null>(null);
  const [removingId, setRemovingId] = useState<number | null>(null);

  useEffect(() => {
    const primaryAccount = accounts.find(a => a.is_primary) ?? accounts[0];
    if (!primaryAccount) return;

    getAccountQuota(primaryAccount.id)
      .then(res => setQuota(res.data))
      .catch(() => {
        // quota fetch failed; leave as null
      });
  }, [accounts]);

  const handleRemove = async (accountId: number) => {
    if (!confirm('Remove this linked account? Messages will not be deleted from the mail server.')) return;
    setRemovingId(accountId);
    await removeAccount(accountId);
    setRemovingId(null);
  };

  return (
    <div className="h-full overflow-y-auto p-6">
      <Card className="max-w-lg mx-auto">
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle>Account Details</CardTitle>
          <Button variant="ghost" size="sm" onClick={() => setView('mail')}>
            <X className="w-4 h-4" />
          </Button>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Profile info */}
          <div>
            <h3 className="text-sm font-medium text-muted-foreground mb-2">Profile</h3>
            <div className="space-y-1 text-sm">
              <p><span className="font-medium">Email:</span> {user?.email}</p>
              <p><span className="font-medium">Display Name:</span> {user?.display_name || 'Not set'}</p>
            </div>
          </div>

          <Separator />

          {/* Linked accounts */}
          <div>
            <h3 className="text-sm font-medium text-muted-foreground mb-2">Linked Accounts</h3>
            <div className="space-y-2">
              {accounts.map(a => (
                <div key={a.id} className="flex items-center justify-between text-sm p-2 rounded-md bg-muted">
                  <div>
                    <span className="font-medium">{a.address}</span>
                    {a.is_primary && (
                      <span className="ml-2 text-xs text-primary">(primary)</span>
                    )}
                  </div>
                  {!a.is_primary && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs text-destructive hover:text-destructive"
                      onClick={() => handleRemove(a.id)}
                      disabled={removingId === a.id}
                    >
                      {removingId === a.id ? 'Removing...' : 'Remove'}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          </div>

          <Separator />

          {/* Quota */}
          <div>
            <h3 className="text-sm font-medium text-muted-foreground mb-2">Storage</h3>
            <div className="w-full h-2 rounded-full bg-muted overflow-hidden">
              <div
                className={`h-full rounded-full ${
                  quota && quota.percent_used >= 95
                    ? 'bg-destructive'
                    : quota && quota.percent_used >= 80
                      ? 'bg-yellow-500'
                      : 'bg-primary'
                }`}
                style={{ width: `${quota ? Math.min(quota.percent_used, 100) : 0}%` }}
              />
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              {quota
                ? `${formatBytes(quota.quota_used_bytes)} of ${formatBytes(quota.quota_bytes)} used`
                : 'Loading usage information…'}
            </p>
            {quota && quota.percent_used >= 80 && (
              <p className={`text-xs mt-1 ${quota.percent_used >= 95 ? 'text-destructive font-medium' : 'text-yellow-600 dark:text-yellow-400'}`}>
                {quota.percent_used >= 95 ? 'Storage almost full! Delete messages or contact your administrator.' : 'Storage usage is high. Consider cleaning up old messages.'}
              </p>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
