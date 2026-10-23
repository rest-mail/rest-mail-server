import { useState } from 'react';
import { ChevronDown, ImageOff, Lock } from 'lucide-react';
import { cn } from '@/lib/utils';
import { trustFromHeaders, type AuthOutcome, type MailHeaders, type TrustVerdict } from '@/lib/mailTrust';
import { TrustBadge } from './TrustBadge';

function outcomeColor(result: AuthOutcome): string {
  if (result === 'pass') return 'var(--color-success)';
  if (result === 'fail' || result === 'softfail' || result === 'permerror') return 'var(--color-danger)';
  if (result === 'none' || result === 'neutral') return 'var(--color-text-secondary)';
  return 'var(--color-primary)';
}

function CheckRow({ method, result, detail }: { method: string; result: AuthOutcome; detail?: string }) {
  return (
    <div className="flex items-baseline gap-2 font-mono text-[11px] leading-relaxed">
      <span className="w-10 shrink-0 text-muted-foreground">{method}</span>
      <span className="w-16 shrink-0 font-medium" style={{ color: outcomeColor(result) }}>
        {result}
      </span>
      {detail && <span className="min-w-0 truncate text-muted-foreground">{detail}</span>}
    </div>
  );
}

export interface TrustReceiptProps {
  headers?: MailHeaders;
  verdict?: TrustVerdict;
  receivedAt?: string;
  sealedHash?: string | null;
  remoteImagesBlocked?: number;
  defaultOpen?: boolean;
  className?: string;
}

export function TrustReceipt({
  headers,
  verdict,
  receivedAt,
  sealedHash,
  remoteImagesBlocked = 0,
  defaultOpen = false,
  className,
}: TrustReceiptProps) {
  const trust = verdict ?? trustFromHeaders(headers);
  const [open, setOpen] = useState(defaultOpen);

  const hasContent = trust.state !== 'unchecked' || Boolean(sealedHash) || remoteImagesBlocked > 0;
  if (!hasContent) return null;

  return (
    <div
      className={cn('overflow-hidden rounded-2xl border border-border bg-card text-card-foreground', className)}
      data-trust-state={trust.state}
    >
      <button
        type="button"
        onClick={() => setOpen(value => !value)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-accent/60"
      >
        <span className="font-mono text-[11px] text-muted-foreground">// trust</span>
        <TrustBadge verdict={trust} compact />
        <span className="flex-1" />
        <ChevronDown
          className={cn('h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')}
        />
      </button>

      {open && (
        <div className="space-y-2 border-t border-border px-3 py-2.5">
          {trust.auth?.checks.map((check, index) => (
            <CheckRow
              key={`${check.method}-${index}`}
              method={check.method}
              result={check.result}
              detail={
                check.domain
                  ? `${check.identity ?? 'domain'}=${check.domain}`
                  : check.reason
                    ? `reason=${check.reason}`
                    : check.raw
              }
            />
          ))}

          {trust.signedBy && trust.state !== 'mismatch' && (
            <CheckRow method="signed" result="pass" detail={trust.signedBy} />
          )}

          {receivedAt && (
            <div className="flex items-baseline gap-2 font-mono text-[11px]">
              <span className="w-10 shrink-0 text-muted-foreground">rcvd</span>
              <span className="min-w-0 truncate text-foreground">{new Date(receivedAt).toLocaleString()}</span>
            </div>
          )}

          {sealedHash && (
            <div className="flex items-center gap-2 font-mono text-[11px]">
              <Lock className="h-3 w-3 shrink-0" style={{ color: 'var(--color-success)' }} />
              <span className="text-foreground">headers unchanged since receipt</span>
              <span className="ml-auto shrink-0 text-muted-foreground">
                {sealedHash.slice(0, 8)}…{sealedHash.slice(-4)}
              </span>
            </div>
          )}

          {remoteImagesBlocked > 0 && (
            <div className="flex items-center gap-2 font-mono text-[11px]">
              <ImageOff className="h-3 w-3 shrink-0 text-muted-foreground" />
              <span className="text-foreground">
                {remoteImagesBlocked} remote {remoteImagesBlocked === 1 ? 'image' : 'images'} blocked
              </span>
              <span className="ml-auto shrink-0 text-muted-foreground">no open tracking</span>
            </div>
          )}

          <p className="border-t border-border pt-2 font-mono text-[10.5px] leading-relaxed text-muted-foreground">
            {trust.detail} This says who sent it and whether it changed on the way — not whether the content is safe.
          </p>
        </div>
      )}
    </div>
  );
}
