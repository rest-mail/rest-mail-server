import { useState } from 'react';
import { useSettingsStore, type ReadingPane, type Density } from '@/stores/settingsStore';
import { useMailStore } from '@/stores/mailStore';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { VacationView } from '@/components/settings/VacationView';
import { Settings, Bell, Users, LayoutPanelTop, MonitorCheck, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

type Tab = 'general' | 'accounts' | 'notifications';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'general',       label: 'General',       icon: <Settings className="w-4 h-4" /> },
  { id: 'accounts',      label: 'Accounts',      icon: <Users className="w-4 h-4" /> },
  { id: 'notifications', label: 'Notifications', icon: <Bell className="w-4 h-4" /> },
];

export function SettingsView() {
  const [tab, setTab] = useState<Tab>('general');

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-5 pb-0 border-b border-border">
        <h2 className="text-lg font-semibold mb-4">Settings</h2>
        <div className="flex gap-0">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={cn(
                "flex items-center gap-1.5 px-4 py-2 text-sm border-b-2 transition-colors -mb-px",
                tab === t.id
                  ? "border-primary text-foreground font-medium"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              )}
            >
              {t.icon}
              {t.label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-auto p-6">
        {tab === 'general'       && <GeneralTab />}
        {tab === 'accounts'      && <AccountsTab />}
        {tab === 'notifications' && <NotificationsTab />}
      </div>
    </div>
  );
}

// ── General tab ───────────────────────────────────────────────────

function GeneralTab() {
  const { readingPane, density, autoSaveDrafts, update } = useSettingsStore();

  return (
    <div className="max-w-2xl space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <LayoutPanelTop className="w-4 h-4" /> Reading Pane
          </CardTitle>
          <CardDescription>Where the message viewer appears.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-3">
          {(['bottom', 'right', 'off'] as ReadingPane[]).map(v => (
            <button
              key={v}
              onClick={() => update({ readingPane: v })}
              className={cn(
                "px-4 py-2 rounded-md border text-sm capitalize transition-colors",
                readingPane === v
                  ? "border-primary bg-primary/10 text-primary font-medium"
                  : "border-border hover:bg-accent"
              )}
            >
              {v}
            </button>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <MonitorCheck className="w-4 h-4" /> Display Density
          </CardTitle>
          <CardDescription>Controls message list row spacing.</CardDescription>
        </CardHeader>
        <CardContent className="flex gap-3">
          {(['comfortable', 'compact'] as Density[]).map(v => (
            <button
              key={v}
              onClick={() => update({ density: v })}
              className={cn(
                "px-4 py-2 rounded-md border text-sm capitalize transition-colors",
                density === v
                  ? "border-primary bg-primary/10 text-primary font-medium"
                  : "border-border hover:bg-accent"
              )}
            >
              {v}
            </button>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Composer</CardTitle>
        </CardHeader>
        <CardContent>
          <Toggle
            label="Auto-save drafts"
            description="Automatically save drafts while composing."
            checked={autoSaveDrafts}
            onChange={v => update({ autoSaveDrafts: v })}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Keyboard Shortcuts</CardTitle>
          <CardDescription>Available in the message list.</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="text-sm space-y-1.5">
            {[
              ['c', 'Compose new message'],
              ['r', 'Reply to selected message'],
              ['f', 'Forward selected message'],
              ['Backspace / Delete', 'Move to trash'],
              ['Esc', 'Clear search / close compose'],
            ].map(([key, desc]) => (
              <div key={key} className="flex items-center gap-3">
                <kbd className="px-2 py-0.5 bg-muted text-muted-foreground rounded text-xs font-mono whitespace-nowrap">{key}</kbd>
                <span className="text-muted-foreground">{desc}</span>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

// ── Accounts tab ──────────────────────────────────────────────────

function AccountsTab() {
  const { accounts, activeAccountId, setActiveAccount } = useMailStore();
  const { user } = useAuthStore();
  const { setView, setSelectedAccountId } = useUIStore();
  const [accountTab, setAccountTab] = useState<'vacation' | 'danger'>('vacation');

  const selected = accounts.find(a => a.id === activeAccountId) ?? accounts[0];

  function openFullSettings(id: number) {
    setSelectedAccountId(id);
    setView('accountSettings');
  }

  if (!selected) return <p className="text-muted-foreground text-sm">No accounts found.</p>;

  const isPrimary = selected.address === user?.email;

  return (
    <div className="max-w-2xl space-y-4">
      {/* Account selector */}
      <div className="flex flex-wrap gap-2">
        {accounts.map(a => (
          <button
            key={a.id}
            onClick={() => setActiveAccount(a.id)}
            className={cn(
              "px-3 py-1.5 rounded-full border text-sm transition-colors",
              a.id === selected.id
                ? "border-primary bg-primary/10 text-primary font-medium"
                : "border-border hover:bg-accent text-muted-foreground"
            )}
          >
            {a.address}
            {a.address === user?.email && <span className="ml-1.5 text-xs opacity-60">(primary)</span>}
          </button>
        ))}
      </div>

      {/* Sub-tabs */}
      <div className="flex gap-0 border-b border-border">
        {(['vacation', 'danger'] as const).map(t => (
          <button
            key={t}
            onClick={() => setAccountTab(t)}
            className={cn(
              "px-4 py-2 text-sm border-b-2 -mb-px transition-colors capitalize",
              accountTab === t
                ? "border-primary text-foreground font-medium"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {t === 'danger' ? 'Danger Zone' : 'Vacation'}
          </button>
        ))}
      </div>

      {accountTab === 'vacation' && <VacationView />}

      {accountTab === 'danger' && (
        <DangerZone isPrimary={isPrimary} accountId={selected.id} onDeleted={() => openFullSettings(selected.id)} />
      )}

      <p className="text-xs text-muted-foreground pt-2">
        Full account settings:{' '}
        <button className="underline hover:no-underline" onClick={() => openFullSettings(selected.id)}>
          open account settings panel &rarr;
        </button>
      </p>
    </div>
  );
}

// ── Notifications tab ─────────────────────────────────────────────

function NotificationsTab() {
  const { desktopNotifications, newMailSound, update } = useSettingsStore();

  return (
    <div className="max-w-2xl">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Notifications</CardTitle>
          <CardDescription>Control how you are alerted to new mail.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Toggle
            label="Desktop notifications"
            description="Show a system notification when new mail arrives."
            checked={desktopNotifications}
            onChange={async (v) => {
              if (v && Notification.permission !== 'granted') {
                const perm = await Notification.requestPermission();
                if (perm !== 'granted') return;
              }
              update({ desktopNotifications: v });
            }}
          />
          <Separator />
          <Toggle
            label="New mail sound"
            description="Play a sound when new mail arrives."
            checked={newMailSound}
            onChange={v => update({ newMailSound: v })}
          />
        </CardContent>
      </Card>
    </div>
  );
}

// ── Danger Zone card (reused in AccountSettings too) ──────────────

export function DangerZone({ isPrimary, accountId, onDeleted }: {
  isPrimary: boolean;
  accountId: number;
  onDeleted?: () => void;
}) {
  const { removeAccount } = useMailStore();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  async function handleRemove() {
    setBusy(true);
    try {
      await removeAccount(accountId);
      onDeleted?.();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <Card className="border-destructive/50">
      <CardHeader>
        <CardTitle className="text-base text-destructive flex items-center gap-2">
          <Trash2 className="w-4 h-4" /> Danger Zone
        </CardTitle>
        <CardDescription>Removing an account only removes it from this webmail session. It does not delete the mailbox.</CardDescription>
      </CardHeader>
      <CardContent>
        {isPrimary ? (
          <p className="text-sm text-muted-foreground">
            You cannot remove your primary account. Log out instead.
          </p>
        ) : confirming ? (
          <div className="flex items-center gap-3">
            <p className="text-sm text-muted-foreground flex-1">Are you sure? This cannot be undone.</p>
            <Button variant="destructive" size="sm" disabled={busy} onClick={handleRemove}>
              {busy ? 'Removing\u2026' : 'Yes, remove'}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>Cancel</Button>
          </div>
        ) : (
          <Button variant="outline" size="sm" className="border-destructive/50 text-destructive hover:bg-destructive hover:text-destructive-foreground" onClick={() => setConfirming(true)}>
            Remove this account from webmail
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

// ── Toggle helper ─────────────────────────────────────────────────

function Toggle({ label, description, checked, onChange }: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (v: boolean) => void | Promise<void>;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div>
        <Label className="font-medium">{label}</Label>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors mt-0.5",
          checked ? "bg-primary" : "bg-muted"
        )}
      >
        <span className={cn(
          "inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform",
          checked ? "translate-x-6" : "translate-x-1"
        )} />
      </button>
    </div>
  );
}
