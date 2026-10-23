import { Forward, Shield, ShieldAlert, ShieldCheck, ShieldOff } from 'lucide-react';
import { cn } from '@/lib/utils';
import { trustFromHeaders, type MailHeaders, type TrustState, type TrustVerdict } from '@/lib/mailTrust';

const TONE_COLOR: Record<TrustVerdict['tone'], string> = {
  ok: 'var(--color-success)',
  info: 'var(--color-primary)',
  warn: 'var(--color-primary)',
  danger: 'var(--color-danger)',
  none: 'var(--color-text-secondary)',
};

function TrustIcon({ state, className }: { state: TrustState; className?: string }) {
  if (state === 'signed') return <ShieldCheck className={className} />;
  if (state === 'mismatch') return <ShieldAlert className={className} />;
  if (state === 'unsigned') return <ShieldOff className={className} />;
  if (state === 'forwarded') return <Forward className={className} />;
  return <Shield className={className} />;
}

export interface TrustBadgeProps {
  headers?: MailHeaders;
  verdict?: TrustVerdict;
  compact?: boolean;
  className?: string;
}

export function TrustBadge({ headers, verdict, compact = false, className }: TrustBadgeProps) {
  const trust = verdict ?? trustFromHeaders(headers);
  if (trust.state === 'unchecked') return null;

  const color = TONE_COLOR[trust.tone];

  return (
    <span
      role="status"
      aria-label={`Sender verification: ${trust.label}. ${trust.detail}`}
      title={trust.detail}
      data-trust-state={trust.state}
      style={{
        color,
        borderColor: `color-mix(in srgb, ${color} 38%, transparent)`,
        backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`,
      }}
      className={cn(
        'inline-flex w-fit shrink-0 items-center gap-1 whitespace-nowrap rounded-full border font-mono font-medium',
        compact ? 'px-1.5 py-0 text-[10px]' : 'px-2 py-0.5 text-[11px]',
        className,
      )}
    >
      <TrustIcon state={trust.state} className={compact ? 'h-3 w-3' : 'h-3.5 w-3.5'} />
      {trust.label}
    </span>
  );
}
