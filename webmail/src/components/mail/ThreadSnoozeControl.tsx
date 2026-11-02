import { useState } from 'react';
import { Clock } from 'lucide-react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import { snoozeDeadline } from '@/lib/threadSnooze';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { MessageDetail } from '@/types';

export function ThreadSnoozeControl({ message }: { message: MessageDetail }) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const snoozeThread = useMailStore(state => state.snoozeThread);

  const save = async (until: string | null) => {
    setBusy(true);
    try {
      await snoozeThread(message.mailbox_id, message.thread_id, until);
      setOpen(false);
      toast.success(until ? 'Thread snoozed' : 'Snooze cancelled');
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Could not snooze thread');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <button type="button" disabled={!message.thread_id} onClick={() => setOpen(true)} title="Snooze thread" aria-label="Snooze thread" className="h-7 px-2 flex items-center gap-1.5 rounded-md text-xs text-muted-foreground hover:text-foreground disabled:opacity-50">
        <Clock className="w-3.5 h-3.5" /> Snooze
      </button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Snooze thread</DialogTitle>
          <DialogDescription>Hide this thread from folders until the chosen time. Search can still find it. Times use your local time zone.</DialogDescription>
        </DialogHeader>
        {message.snoozed_until && <p className="text-sm">Snoozed until {new Date(message.snoozed_until).toLocaleString()}</p>}
        <form onSubmit={event => {
          event.preventDefault();
          try {
            void save(snoozeDeadline(value));
          } catch (error) {
            toast.error(error instanceof Error ? error.message : 'Choose a valid time');
          }
        }} className="grid gap-3">
          <Label htmlFor="thread-snooze-until">Return at</Label>
          <Input id="thread-snooze-until" type="datetime-local" required value={value} onChange={event => setValue(event.target.value)} />
          <Button type="submit" disabled={busy}>Snooze thread</Button>
          {message.snoozed_until && <Button type="button" variant="outline" disabled={busy} onClick={() => void save(null)}>Cancel snooze</Button>}
        </form>
      </DialogContent>
    </Dialog>
  );
}
