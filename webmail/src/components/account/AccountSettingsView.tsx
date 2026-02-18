import { useState } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { VacationView } from '@/components/settings/VacationView';
import { DangerZone } from '@/components/settings/SettingsView';
import { User, HardDrive, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

type Tab = 'details' | 'vacation' | 'danger';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'details',  label: 'Details',     icon: <User className="w-4 h-4" /> },
  { id: 'vacation', label: 'Vacation',    icon: <HardDrive className="w-4 h-4" /> },
  { id: 'danger',   label: 'Danger Zone', icon: <Trash2 className="w-4 h-4" /> },
];

export function AccountSettingsView() {
  const { selectedAccountId, setView } = useUIStore();
  const { accounts, setActiveAccount } = useMailStore();
  const { user } = useAuthStore();
  const [tab, setTab] = useState<Tab>('details');

  const account = accounts.find(a => a.id === selectedAccountId);

  if (!account) {
    return (
      <div className="p-6 text-muted-foreground text-sm">
        Account not found.{' '}
        <button className="underline" onClick={() => setView('mail')}>Go back</button>
      </div>
    );
  }

  const isPrimary = account.address === user?.email;

  function handleTabClick(t: Tab) {
    setTab(t);
    if (account) setActiveAccount(account.id);
  }

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-5 pb-0 border-b border-border">
        <p className="text-xs text-muted-foreground mb-0.5">Account settings</p>
        <h2 className="text-lg font-semibold mb-4 truncate">{account.address}</h2>
        <div className="flex gap-0">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => handleTabClick(t.id)}
              className={cn(
                "flex items-center gap-1.5 px-4 py-2 text-sm border-b-2 transition-colors -mb-px",
                tab === t.id
                  ? "border-primary text-foreground font-medium"
                  : "border-transparent text-muted-foreground hover:text-foreground",
                t.id === 'danger' && tab !== 'danger' && "hover:text-destructive"
              )}
            >
              {t.icon}
              {t.label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-auto p-6">
        {tab === 'details'  && <DetailsTab account={account} isPrimary={isPrimary} />}
        {tab === 'vacation' && <VacationView />}
        {tab === 'danger'   && (
          <div className="max-w-2xl">
            <DangerZone
              isPrimary={isPrimary}
              accountId={account.id}
              onDeleted={() => setView('mail')}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function DetailsTab({ account, isPrimary }: { account: { id: number; address: string }; isPrimary: boolean }) {
  return (
    <div className="max-w-2xl space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <User className="w-4 h-4" /> Account Details
          </CardTitle>
          {isPrimary && (
            <CardDescription>This is your primary account.</CardDescription>
          )}
        </CardHeader>
        <CardContent className="space-y-3">
          <div>
            <p className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Email address</p>
            <p className="text-sm font-mono">{account.address}</p>
          </div>
          <div>
            <p className="text-xs text-muted-foreground uppercase tracking-wide mb-1">Account ID</p>
            <p className="text-sm font-mono text-muted-foreground">#{account.id}</p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <HardDrive className="w-4 h-4" /> Storage Quota
          </CardTitle>
          <CardDescription>Quota information is available via the API.</CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Check your quota via{' '}
            <span className="font-mono text-xs bg-muted px-1 py-0.5 rounded">GET /api/v1/mailboxes/:id</span>
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
