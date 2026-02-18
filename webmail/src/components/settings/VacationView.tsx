import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import * as api from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Palmtree } from 'lucide-react';

export function VacationView() {
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
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading vacation settings...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-2xl mx-auto">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Palmtree className="w-5 h-5" />
            Vacation Auto-Reply
          </CardTitle>
          <CardDescription>
            Automatically reply to incoming messages when you&apos;re away.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
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

          <div>
            <Label htmlFor="vacation-subject">Subject</Label>
            <Input
              id="vacation-subject"
              placeholder="Out of office"
              value={subject}
              onChange={e => setSubject(e.target.value)}
            />
          </div>

          <div>
            <Label htmlFor="vacation-body">Message</Label>
            <textarea
              id="vacation-body"
              rows={6}
              placeholder="I'm currently out of the office and will return on..."
              value={body}
              onChange={e => setBody(e.target.value)}
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label htmlFor="vacation-start">Start Date (optional)</Label>
              <Input
                id="vacation-start"
                type="date"
                value={startDate}
                onChange={e => setStartDate(e.target.value)}
              />
            </div>
            <div>
              <Label htmlFor="vacation-end">End Date (optional)</Label>
              <Input
                id="vacation-end"
                type="date"
                value={endDate}
                onChange={e => setEndDate(e.target.value)}
              />
            </div>
          </div>

          <div className="flex gap-2 pt-2">
            <Button onClick={handleSave} disabled={saving}>
              {saving ? 'Saving...' : 'Save'}
            </Button>
            {enabled && (
              <Button variant="outline" onClick={handleDisable} disabled={saving}>
                Disable & Clear
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
