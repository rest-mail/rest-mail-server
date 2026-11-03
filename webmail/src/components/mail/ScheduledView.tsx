import { displayAddress } from '@/lib/addressDisplay';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import * as api from '@/api/client';
import {
  CANCELLED_OUTCOME, cancelPrompt, scheduledRow, withoutCancelled,
  type ScheduledMessage,
} from '@/lib/scheduledSend';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Clock, X } from 'lucide-react';

// ScheduledView lists the messages this session's account asked to send later,
// soonest first, with each send time in the person's own zone.
export function ScheduledView() {
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [items, setItems] = useState<ScheduledMessage[]>([]);
  const [cancelling, setCancelling] = useState<number | null>(null);
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

  const load = async () => {
    setLoading(true);
    setError('');
    try {
      const res = await api.listScheduledMessages();
      setItems(res.data || []);
    } catch (err) {
      setItems([]);
      setError(err instanceof Error ? err.message : 'Failed to load scheduled messages');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const handleCancel = async (item: ScheduledMessage) => {
    if (!confirm(cancelPrompt(item))) return;
    setCancelling(item.id);
    try {
      await api.cancelScheduledMessage(item.id);
      setItems(prev => withoutCancelled(prev, item.id));
      toast.success('Scheduled message cancelled', { description: CANCELLED_OUTCOME });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to cancel the scheduled message');
      void load(); // it may have started sending meanwhile
    } finally {
      setCancelling(null);
    }
  };

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading scheduled messages...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Clock className="w-5 h-5" />
            Scheduled
            {items.length > 0 && <Badge variant="secondary" className="ml-2">{items.length}</Badge>}
          </CardTitle>
          <CardDescription>
            Messages waiting to be sent later. Times are in your time zone ({timeZone}).
          </CardDescription>
        </CardHeader>
        <CardContent>
          {error ? (
            <div role="alert" className="text-center py-8 text-destructive">{error}</div>
          ) : items.length === 0 ? (
            <div className="text-center py-8 text-muted-foreground">
              No scheduled messages. Use Send later when composing to add one.
            </div>
          ) : (
            <ScrollArea className="max-h-[60vh]">
              <ul className="space-y-2">
                {items.map(item => ({ item, row: scheduledRow(item) })).map(({ item, row }) => (
                  <li key={row.id} className="flex items-start gap-3 p-3 rounded-lg border bg-card">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="font-medium text-sm">{row.when}</span>
                        <Badge variant={item.status === 'held' ? 'destructive' : 'secondary'} className="text-xs">
                          {row.status}
                        </Badge>
                      </div>
                      <div className="text-sm truncate">{row.subject}</div>
                      <div className="text-xs text-muted-foreground mt-1 truncate">
                        To {row.to.map(displayAddress).join(', ') || '(no recipients)'} · from {displayAddress(row.from)}
                      </div>
                      {row.error && (
                        <div className="text-xs text-destructive mt-1">{row.error}</div>
                      )}
                    </div>
                    {row.cancellable && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={cancelling === row.id}
                        onClick={() => void handleCancel(item)}
                        title="Cancel this scheduled message"
                      >
                        <X className="w-4 h-4 mr-1" /> {cancelling === row.id ? 'Cancelling...' : 'Cancel'}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </ScrollArea>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
