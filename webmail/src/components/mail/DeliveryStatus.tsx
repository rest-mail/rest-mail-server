import { cn } from '@/lib/utils';
import { RECIPIENT_STATE_LABELS, deliverySummary, type RecipientStatus } from '@/lib/deliveryStatus';

function stateColor(state: RecipientStatus['state']): string {
  if (state === 'delivered') return 'var(--color-success)';
  if (state === 'failed') return 'var(--color-danger)';
  if (state === 'unknown') return 'var(--color-text-secondary)';
  return 'var(--color-primary)';
}

// DeliveryStatus shows a sent message's recipients when any copy is not
// delivered: each one's state and, from its failure category, why (#1568).
export function DeliveryStatus({ recipients, className }: { recipients: RecipientStatus[]; className?: string }) {
  const summary = deliverySummary(recipients);
  if (!summary) return null;
  return (
    <div className={cn('rounded-2xl border bg-card px-3 py-2 text-xs', className)} role="status">
      <p className="font-medium">{summary}</p>
      <ul className="mt-1 space-y-1">
        {recipients.map(r => (
          <li key={r.address} className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-mono">{r.address}</span>
            <span className="font-medium" style={{ color: stateColor(r.state) }}>{RECIPIENT_STATE_LABELS[r.state]}</span>
            {r.reason && <span className="text-muted-foreground">{r.reason}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}
