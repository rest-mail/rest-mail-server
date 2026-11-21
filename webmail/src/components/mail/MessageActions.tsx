import { useState } from 'react';
import { toast } from 'sonner';
import { updateMessage, deleteMessage } from '@/api/client';
import { useMailStore } from '@/stores/mailStore';

export function MessageActions({ ids, prefix, onComplete }: { ids: number[]; prefix: string; onComplete?: () => void }) {
  const { folders, refresh } = useMailStore();
  const [busy, setBusy] = useState(false);
  const [destination, setDestination] = useState('');
  async function act(updates: Record<string, unknown> | 'delete') {
    setBusy(true);
    try {
      for (const id of ids) {
        if (updates === 'delete') await deleteMessage(id);
        else await updateMessage(id, updates);
      }
      await refresh(); onComplete?.();
    } catch (error) { toast.error(error instanceof Error ? error.message : 'Message action failed'); }
    finally { setBusy(false); }
  }
  return <fieldset disabled={busy || ids.length === 0} className="flex gap-2 flex-wrap px-3 py-2 text-xs border-b border-border">
    <button data-testid={`${prefix}-read`} onClick={() => void act({ is_read: true })}>Read</button>
    <button data-testid={`${prefix}-unread`} onClick={() => void act({ is_read: false })}>Unread</button>
    <button data-testid={`${prefix}-star`} onClick={() => void act({ is_starred: true })}>Star</button>
    <button data-testid={`${prefix}-unstar`} onClick={() => void act({ is_starred: false })}>Unstar</button>
    <button data-testid={`${prefix}-flag`} onClick={() => void act({ is_flagged: true })}>Flag</button>
    <button data-testid={`${prefix}-unflag`} onClick={() => void act({ is_flagged: false })}>Unflag</button>
    <button data-testid={`${prefix}-archive`} onClick={() => void act({ folder: 'Archive' })}>Archive</button>
    <button data-testid={`${prefix}-delete`} onClick={() => void act('delete')}>Delete</button>
    <select data-testid={`${prefix}-destination`} aria-label="Destination folder" value={destination} onChange={event => setDestination(event.target.value)}>
      <option value="">Move to…</option>{folders.map(folder => <option key={folder.name} value={folder.name}>{folder.name}</option>)}
    </select>
    <button data-testid={`${prefix}-move`} disabled={!destination} onClick={() => void act({ folder: destination })}>Move</button>
  </fieldset>;
}
