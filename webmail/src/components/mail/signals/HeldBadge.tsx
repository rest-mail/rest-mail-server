import { Inbox } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface HeldBadgeProps {
  count?: number | null;
  onClick?: () => void;
  className?: string;
}

export function HeldBadge({ count, onClick, className }: HeldBadgeProps) {
  if (!count || count <= 0) return null;

  const color = 'var(--color-danger)';
  const style = {
    color,
    borderColor: `color-mix(in srgb, ${color} 38%, transparent)`,
    backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`,
  };
  const classes = cn(
    'inline-flex w-fit shrink-0 items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 font-mono text-[11px] font-medium',
    onClick && 'transition-opacity hover:opacity-80',
    className,
  );
  const title = 'Held by the server. These messages did not reach your inbox — review them in quarantine.';
  const content = (
    <>
      <Inbox className="h-3 w-3" />
      {count} held
    </>
  );

  if (!onClick) {
    return (
      <span role="status" title={title} style={style} className={classes} data-held-count={count}>
        {content}
      </span>
    );
  }

  return (
    <button type="button" onClick={onClick} title={title} style={style} className={classes} data-held-count={count}>
      {content}
    </button>
  );
}
