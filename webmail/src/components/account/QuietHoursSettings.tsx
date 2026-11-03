import { useEffect, useState } from 'react';
import { getQuietHours, setQuietHours } from '@/api/client';
import type { QuietHoursSetting } from '@/api/client';
import { QuietHoursPreference } from './QuietHoursPreference';

export function QuietHoursSettings({ accountId }: { accountId: number }) {
  const [setting, setSetting] = useState<QuietHoursSetting>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    let disposed = false;
    getQuietHours(accountId).then(response => {
      if (!disposed) setSetting(response.data);
    }).catch(() => {
      if (!disposed) setError('Could not load quiet hours.');
    });
    return () => { disposed = true; };
  }, [accountId]);

  async function save() {
    if (!setting) return;
    setSaving(true);
    setError('');
    try {
      const response = await setQuietHours(accountId, setting);
      setSetting(response.data);
    } catch {
      setError('Could not save quiet hours. Check both times and the IANA time zone.');
    } finally {
      setSaving(false);
    }
  }

  return <QuietHoursPreference setting={setting} saving={saving} error={error} onChange={setSetting} onSave={save} />;
}
