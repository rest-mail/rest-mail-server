import { CalendarClock, Clock } from 'lucide-react';
import { cn } from '@/lib/utils';
import { describeDue, formatAge } from '@/lib/mailTrust';

function chipStyle(color: string) {
  return {
    color,
    borderColor: `color-mix(in srgb, ${color} 38%, transparent)`,
    backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`,
  };
}

const SOURCE_LABEL: Record<string, string> = {
  user: 'set by you',
  rule: 'set by a rule',
  parsed: 'parsed from the message text',
};

export interface WaitingBadgeProps {
  since?: string | null;
  className?: string;
}

export function WaitingBadge({ since, className }: WaitingBadgeProps) {
  if (!since) return null;
  const age = formatAge(since);
  if (!age) return null;

  const elapsed = Date.now() - new Date(since).getTime();
  const stale = elapsed > 3 * 24 * 60 * 60 * 1000;
  const color = stale ? 'var(--color-primary)' : 'var(--color-text-secondary)';

  return (
    <span
      role="status"
      title="The last message in this thread is yours — it is waiting on a reply."
      style={chipStyle(color)}
      className={cn(
        'inline-flex w-fit shrink-0 items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 font-mono text-[11px] font-medium',
        className,
      )}
    >
      <Clock className="h-3 w-3" />
      waiting {age}
    </span>
  );
}

export interface DueBadgeProps {
  due?: string | null;
  source?: 'user' | 'rule' | 'parsed' | string | null;
  className?: string;
}

export function DueBadge({ due, source, className }: DueBadgeProps) {
  if (!due) return null;
  const described = describeDue(due);
  if (!described) return null;

  const color = described.overdue ? 'var(--color-danger)' : 'var(--color-primary)';
  const label = described.overdue
    ? `past due ${described.text.replace(' over', '')}`
    : `due ${described.text}`;
  const sourceLabel = source ? SOURCE_LABEL[source] ?? source : undefined;

  return (
    <span
      role="status"
      title={sourceLabel ? `Deadline ${sourceLabel}.` : 'Deadline.'}
      style={chipStyle(color)}
      className={cn(
        'inline-flex w-fit shrink-0 items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 font-mono text-[11px] font-medium',
        className,
      )}
    >
      <CalendarClock className="h-3 w-3" />
      {label}
      {sourceLabel && <span className="text-[10px] opacity-70">· {sourceLabel}</span>}
    </span>
  );
}

export interface ThreadSignalsProps {
  awaitingSince?: string | null;
  due?: string | null;
  dueSource?: string | null;
  className?: string;
}

export function ThreadSignals({ awaitingSince, due, dueSource, className }: ThreadSignalsProps) {
  if (!awaitingSince && !due) return null;
  return (
    <div className={cn('flex flex-wrap items-center gap-1.5', className)}>
      <DueBadge due={due} source={dueSource} />
      <WaitingBadge since={awaitingSince} />
    </div>
  );
}
