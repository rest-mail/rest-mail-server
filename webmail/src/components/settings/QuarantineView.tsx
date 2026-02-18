import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import * as api from '@/api/client';
import type { QuarantineItem } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ScrollArea } from '@/components/ui/scroll-area';
import { ShieldAlert, Check, Trash2 } from 'lucide-react';

export function QuarantineView() {
  const { activeAccountId } = useMailStore();
  const [loading, setLoading] = useState(true);
  const [items, setItems] = useState<QuarantineItem[]>([]);
  const [actionInProgress, setActionInProgress] = useState<number | null>(null);

  const loadItems = async () => {
    if (!activeAccountId) return;
    setLoading(true);
    try {
      const res = await api.listQuarantine(activeAccountId);
      setItems(res.data || []);
    } catch {
      setItems([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadItems();
  }, [activeAccountId]);

  const handleRelease = async (item: QuarantineItem) => {
    if (!activeAccountId) return;
    setActionInProgress(item.id);
    try {
      await api.releaseQuarantine(activeAccountId, item.id);
      setItems(prev => prev.filter(i => i.id !== item.id));
      toast.success(`Released "${item.subject}" to inbox`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to release message');
    } finally {
      setActionInProgress(null);
    }
  };

  const handleDelete = async (item: QuarantineItem) => {
    if (!activeAccountId) return;
    setActionInProgress(item.id);
    try {
      await api.deleteQuarantine(activeAccountId, item.id);
      setItems(prev => prev.filter(i => i.id !== item.id));
      toast.success('Quarantined message deleted');
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete message');
    } finally {
      setActionInProgress(null);
    }
  };

  const reasonBadgeVariant = (reason: string) => {
    if (reason.includes('spam')) return 'destructive' as const;
    if (reason.includes('virus') || reason.includes('malware')) return 'destructive' as const;
    return 'secondary' as const;
  };

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center">
        <div className="animate-pulse text-muted-foreground">Loading quarantine...</div>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldAlert className="w-5 h-5" />
            Quarantine
            {items.length > 0 && (
              <Badge variant="secondary" className="ml-2">{items.length}</Badge>
            )}
          </CardTitle>
          <CardDescription>
            Messages held by spam and security filters. Release to deliver to your inbox, or delete permanently.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {items.length === 0 ? (
            <div className="text-center py-8 text-muted-foreground">
              No quarantined messages. Your inbox is clean!
            </div>
          ) : (
            <ScrollArea className="max-h-[60vh]">
              <div className="space-y-2">
                {items.map(item => (
                  <div
                    key={item.id}
                    className="flex items-start gap-3 p-3 rounded-lg border bg-card hover:bg-accent/50 transition-colors"
                  >
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="font-medium text-sm truncate">{item.sender}</span>
                        <Badge variant={reasonBadgeVariant(item.quarantine_reason)} className="text-xs">
                          {item.quarantine_reason}
                        </Badge>
                        {item.spam_score != null && (
                          <span className="text-xs text-muted-foreground">
                            score: {item.spam_score.toFixed(1)}
                          </span>
                        )}
                      </div>
                      <div className="text-sm truncate">{item.subject || '(no subject)'}</div>
                      {item.body_preview && (
                        <div className="text-xs text-muted-foreground mt-1 line-clamp-2">
                          {item.body_preview}
                        </div>
                      )}
                      <div className="text-xs text-muted-foreground mt-1">
                        {new Date(item.received_at).toLocaleString()}
                      </div>
                    </div>
                    <div className="flex gap-1 shrink-0">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={actionInProgress === item.id}
                        onClick={() => handleRelease(item)}
                        title="Release to inbox"
                      >
                        <Check className="w-4 h-4 mr-1" /> Release
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        disabled={actionInProgress === item.id}
                        onClick={() => handleDelete(item)}
                        title="Delete permanently"
                      >
                        <Trash2 className="w-4 h-4" />
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </ScrollArea>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
