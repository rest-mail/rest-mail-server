import { displayAddress } from '@/lib/addressDisplay';
import { useState } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { SettingsNavButton, SettingsPage, SettingsSection } from '@/components/ui/settings';
import { VacationView } from '@/components/settings/VacationView';
import { DangerZone } from '@/components/settings/SettingsView';
import { User, Palmtree, Trash2 } from 'lucide-react';
import { ContactReplyTimeSettings } from '@/components/contacts/ContactReplyTimeSettings';
import { QuietHoursSettings } from './QuietHoursSettings';
import { AccountTimeZoneSettings } from './AccountTimeZoneSettings';
import { mailPlatform } from '@/platform';

type Tab = 'details' | 'vacation' | 'danger';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'details',  label: 'Details',     icon: <User className="w-4 h-4" /> },
  { id: 'vacation', label: 'Vacation',    icon: <Palmtree className="w-4 h-4" /> },
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
    <SettingsPage title="Account settings" subtitle={displayAddress(account.address)} navigation={
      TABS.map(item => (
        <SettingsNavButton key={item.id} active={tab === item.id} onClick={() => handleTabClick(item.id)}>
          {item.icon}
          {item.label}
        </SettingsNavButton>
      ))
    }>
      {tab === 'details' && <DetailsTab account={account} isPrimary={isPrimary} />}
      {tab === 'vacation' && <VacationView embedded />}
      {tab === 'danger' && (
        <DangerZone isPrimary={isPrimary} accountId={account.id} onDeleted={() => setView('mail')} />
      )}
    </SettingsPage>
  );
}

function DetailsTab({ account, isPrimary }: { account: { id: number; address: string }; isPrimary: boolean }) {
  const platform = mailPlatform();
  const server = platform.desktop ? platform.serverHost?.() : undefined;
  return (
    <>
      <SettingsSection title="Account details" description={isPrimary ? 'This is your primary account.' : undefined}>
        <dl className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)] gap-x-4 gap-y-2">
          <dt className="text-muted-foreground">Email address</dt>
          <dd className="break-all font-medium">{displayAddress(account.address)}</dd>
          {server && <>
            <dt className="text-muted-foreground">Mail server</dt>
            <dd className="break-all">{server}</dd>
          </>}
          <dt className="text-muted-foreground">Account ID</dt>
          <dd className="tabular-nums">#{account.id}</dd>
        </dl>
      </SettingsSection>
      <QuietHoursSettings key={`quiet-${account.id}`} accountId={account.id} />
      <ContactReplyTimeSettings key={account.id} accountId={account.id} />
      <AccountTimeZoneSettings key={`time-zone-${account.id}`} accountId={account.id} />
      <SettingsSection title="Storage quota" description="Quota information is available via the API.">
        <code className="break-all rounded bg-muted px-2 py-1 text-xs">GET /api/v1/mailboxes/:id</code>
      </SettingsSection>
    </>
  );
}
