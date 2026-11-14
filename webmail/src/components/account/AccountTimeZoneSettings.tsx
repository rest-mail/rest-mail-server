import { useEffect, useState } from 'react';
import { getAccountTimeZone, setAccountTimeZone } from '@/api/client';
import { timeZoneRefusal, timeZoneToStore } from '@/lib/accountTimeZone';
import { AccountTimeZonePreference } from './AccountTimeZonePreference';

export function AccountTimeZoneSettings({ accountId }: { accountId: number }) {
  const [saved, setSaved] = useState<string | null>();
  const [draft, setDraft] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    let disposed = false;
    getAccountTimeZone(accountId).then(response => {
      if (disposed) return;
      setSaved(response.data.time_zone);
      setDraft(response.data.time_zone ?? '');
    }).catch(() => {
      if (!disposed) setError('Could not load the time zone.');
    });
    return () => { disposed = true; };
  }, [accountId]);

  async function store(timeZone: string | null) {
    setSaving(true);
    setError('');
    try {
      const response = await setAccountTimeZone(accountId, timeZone);
      setSaved(response.data.time_zone);
      setDraft(response.data.time_zone ?? '');
    } catch (refusal) {
      setError(timeZoneRefusal(refusal));
    } finally {
      setSaving(false);
    }
  }

  return (
    <AccountTimeZonePreference saved={saved} draft={draft} saving={saving} error={error} onDraftChange={setDraft}
      onSave={() => store(timeZoneToStore(draft))} onClear={() => store(null)} />
  );
}
