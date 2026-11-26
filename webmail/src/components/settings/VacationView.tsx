import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import * as api from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { SettingsActions, SettingsField, SettingsSection, SettingsStatus } from '@/components/ui/settings';

export function VacationView({ embedded = false }: { embedded?: boolean }) {
  const { activeAccountId } = useMailStore();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');

  useEffect(() => {
    if (!activeAccountId) return;
    setLoading(true);
    api.getVacation(activeAccountId)
      .then(res => {
        if (res.data) {
          setEnabled(res.data.enabled);
          setSubject(res.data.subject || '');
          setBody(res.data.body || '');
          setStartDate(res.data.start_date?.split('T')[0] || '');
          setEndDate(res.data.end_date?.split('T')[0] || '');
        }
      })
      .catch(() => { /* no vacation config yet */ })
      .finally(() => setLoading(false));
  }, [activeAccountId]);

  const handleSave = async () => {
    if (!activeAccountId) return;
    setSaving(true);
    try {
      await api.setVacation(activeAccountId, {
        enabled,
        subject,
        body,
        start_date: startDate || undefined,
        end_date: endDate || undefined,
      });
      toast.success(enabled ? 'Vacation auto-reply enabled' : 'Vacation settings saved');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save vacation settings');
    } finally {
      setSaving(false);
    }
  };

  const handleDisable = async () => {
    if (!activeAccountId) return;
    setSaving(true);
    try {
      await api.disableVacation(activeAccountId);
      setEnabled(false);
      setSubject('');
      setBody('');
      setStartDate('');
      setEndDate('');
      toast.success('Vacation auto-reply disabled');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to disable vacation');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="p-5"><SettingsStatus error="" pending="Loading vacation settings..." /></div>;
  }

  const section = (
    <SettingsSection title="Vacation auto-reply" description="Automatically reply to incoming mail while you're away.">
      <div className="flex items-center gap-3">
        <Label htmlFor="vacation-enabled" className="font-medium">Enable auto-reply</Label>
        <button
          id="vacation-enabled"
          role="switch"
          aria-checked={enabled}
          onClick={() => setEnabled(!enabled)}
          className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
            enabled ? 'bg-primary' : 'bg-muted'
          }`}
        >
          <span className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
            enabled ? 'translate-x-6' : 'translate-x-1'
          }`} />
        </button>
        <span className="text-sm text-muted-foreground">
          {enabled ? 'Active' : 'Inactive'}
        </span>
      </div>

      <SettingsField label="Subject">
        <Input
          id="vacation-subject"
          placeholder="Out of office"
          value={subject}
          onChange={e => setSubject(e.target.value)}
        />
      </SettingsField>

      <SettingsField label="Message">
        <textarea
          id="vacation-body"
          rows={6}
          placeholder="I'm currently out of the office and will return on..."
          value={body}
          onChange={e => setBody(e.target.value)}
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
      </SettingsField>

      <div className="grid grid-cols-2 gap-4">
        <SettingsField label="Start date (optional)">
          <Input
            id="vacation-start"
            type="date"
            value={startDate}
            onChange={e => setStartDate(e.target.value)}
          />
        </SettingsField>
        <SettingsField label="End date (optional)">
          <Input
            id="vacation-end"
            type="date"
            value={endDate}
            onChange={e => setEndDate(e.target.value)}
          />
        </SettingsField>
      </div>

      <SettingsActions>
        <Button onClick={handleSave} disabled={saving}>
          {saving ? 'Saving...' : 'Save'}
        </Button>
        {enabled && (
          <Button variant="outline" onClick={handleDisable} disabled={saving}>
            Disable & Clear
          </Button>
        )}
      </SettingsActions>
    </SettingsSection>
  );
  return embedded ? section : <div className="@container mx-auto max-w-5xl px-5">{section}</div>;
}
