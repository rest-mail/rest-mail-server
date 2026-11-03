import { useState } from 'react';
import type { MessageDetail } from '@/types';
import { dueLocalInput, dueToSave, initialDueInput, isDueSuggestion, type DueAction } from '@/lib/threadDue';
import { ThreadSignals } from './signals/ThreadSignals';

interface ThreadDueFormProps {
  message: MessageDetail;
  value: string;
  pending: boolean;
  error: string;
  onValue: (value: string) => void;
  onSave: () => void;
  onClear: () => void;
  onAccept: () => void;
}

export function ThreadDueForm({ message, value, pending, error, onValue, onSave, onClear, onAccept }: ThreadDueFormProps) {
  if (!message.thread_id) return null;
  const suggestion = Boolean(message.due) && isDueSuggestion(message.due_source);
  return (
    <div className="mb-3 space-y-2">
      <ThreadSignals awaitingSince={message.awaiting_since} due={message.due} dueSource={message.due_source} />
      {message.due && <time dateTime={message.due} className="block text-xs text-muted-foreground">{new Date(message.due).toLocaleString()}</time>}
      <form className="flex flex-wrap items-center gap-2 text-xs" onSubmit={event => { event.preventDefault(); onSave(); }}>
        <label className="flex items-center gap-2">
          Due date
          <input type="datetime-local" value={value} disabled={pending} onChange={event => onValue(event.target.value)} className="rounded border border-border bg-background px-2 py-1" />
        </label>
        <button type="submit" disabled={pending || !value} className="rounded border border-border px-2 py-1 disabled:opacity-50">{pending ? 'Saving…' : 'Set due date'}</button>
        {suggestion && <button type="button" disabled={pending} onClick={onAccept} className="rounded border border-border px-2 py-1 disabled:opacity-50">Accept suggestion</button>}
        {message.due && <button type="button" disabled={pending} onClick={onClear} className="rounded border border-border px-2 py-1 disabled:opacity-50">{suggestion ? 'Dismiss suggestion' : 'Clear due date'}</button>}
      </form>
      {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    </div>
  );
}

export function ThreadDueControl({ message, setThreadDue }: { message: MessageDetail; setThreadDue: (mailboxId: number, threadId: string, due: string | null) => Promise<void> }) {
  const [value, setValue] = useState(() => initialDueInput(message));
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const save = async (action: DueAction) => {
    setPending(true);
    setError('');
    try {
      const due = dueToSave(action, message, value);
      await setThreadDue(message.mailbox_id, message.thread_id, due);
      setValue(dueLocalInput(due));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not save due date');
    } finally {
      setPending(false);
    }
  };
  return <ThreadDueForm message={message} value={value} pending={pending} error={error} onValue={setValue} onSave={() => void save('set')} onClear={() => void save('clear')} onAccept={() => void save('accept')} />;
}
