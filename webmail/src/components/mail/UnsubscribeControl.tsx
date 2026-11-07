import { useEffect, useState } from 'react';
import { MailX } from 'lucide-react';
import { toast } from 'sonner';
import { getUnsubscribeOptions, unsubscribeMailto, unsubscribeOneClick } from '@/api/client';
import { unsubscribeChoice, type UnsubscribeChoice } from '@/lib/unsubscribe';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';

// The Unsubscribe action of a message whose List-Unsubscribe field offers a target (#1575).
// Nothing is sent until the person confirms, and the dialog says which method is used.
export function UnsubscribeControl({ messageId }: { messageId: number }) {
  const [choice, setChoice] = useState<UnsubscribeChoice | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setChoice(null);
    getUnsubscribeOptions(messageId)
      .then(options => { if (!cancelled) setChoice(unsubscribeChoice(options)); })
      .catch(() => { if (!cancelled) setChoice(null); });
    return () => { cancelled = true; };
  }, [messageId]);

  if (!choice) return null;

  const confirm = async () => {
    if (choice.method === 'link') {
      window.open(choice.target, '_blank', 'noopener,noreferrer');
      setOpen(false);
      return;
    }
    setBusy(true);
    try {
      if (choice.method === 'one_click') {
        await unsubscribeOneClick(messageId);
      } else {
        await unsubscribeMailto(messageId, choice.index);
      }
      setOpen(false);
      toast.success('Unsubscribe request sent');
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Could not unsubscribe');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <button type="button" onClick={() => setOpen(true)} title="Unsubscribe" aria-label="Unsubscribe" className="h-7 px-2 flex items-center gap-1.5 rounded-md text-xs text-muted-foreground hover:text-foreground">
        <MailX className="w-3.5 h-3.5" /> Unsubscribe
      </button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{choice.label}</DialogTitle>
          <DialogDescription>{choice.detail}</DialogDescription>
        </DialogHeader>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" disabled={busy} onClick={() => setOpen(false)}>Cancel</Button>
          <Button type="button" disabled={busy} onClick={() => void confirm()}>Unsubscribe</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
