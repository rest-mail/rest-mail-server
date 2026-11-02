import { useEffect, useState } from 'react';
import { getContactReplyTimeSetting, setContactReplyTimeSetting } from '@/api/client';
import { ContactReplyTimePreference } from './ContactReplyTime';

export function ContactReplyTimeSettings({ accountId }: { accountId: number }) {
  const [enabled, setEnabled] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let disposed = false;
    getContactReplyTimeSetting(accountId).then(response => {
      if (!disposed) setEnabled(response.data.enabled);
    }).catch(() => {
      if (!disposed) setError('Could not load the contact reply-time setting.');
    });
    return () => { disposed = true; };
  }, [accountId]);

  async function save(value: boolean) {
    setSaving(true);
    setError('');
    try {
      const response = await setContactReplyTimeSetting(accountId, value);
      setEnabled(response.data.enabled);
    } catch {
      setError('Could not save the contact reply-time setting.');
    } finally {
      setSaving(false);
    }
  }

  return <ContactReplyTimePreference enabled={enabled} saving={saving} error={error} onChange={save} />;
}
