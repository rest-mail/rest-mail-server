import type { components } from '../api/types';

export type RecipientStatus = components['schemas']['RecipientStatus'];
export type RecipientState = RecipientStatus['state'];

export const RECIPIENT_STATE_LABELS: Record<RecipientState, string> = {
  queued: 'Waiting to be sent',
  retrying: 'Not delivered yet, trying again',
  delivered: 'Delivered',
  failed: 'Not delivered',
  unknown: 'No delivery record',
};

// allDelivered reports whether every copy of a sent message was delivered.
export function allDelivered(recipients: RecipientStatus[]): boolean {
  return recipients.length > 0 && recipients.every(r => r.state === 'delivered');
}

// deliverySummary says in a line how a sent message's copies went, or null when
// there are no recipients to report on. A send delivered to everyone says so,
// so the reader never has to guess from a missing line whether it worked.
export function deliverySummary(recipients: RecipientStatus[]): string | null {
  if (recipients.length === 0) return null;
  const delivered = recipients.filter(r => r.state === 'delivered').length;
  if (delivered === recipients.length) {
    return delivered === 1 ? 'Delivered to its recipient' : `Delivered to all ${delivered} recipients`;
  }
  const failed = recipients.filter(r => r.state === 'failed').length;
  const pending = recipients.filter(r => r.state === 'queued' || r.state === 'retrying').length;
  const parts = [`Delivered to ${delivered} of ${recipients.length} recipients`];
  if (failed > 0) parts.push(`${failed} not delivered`);
  if (pending > 0) parts.push(`${pending} still being sent`);
  return parts.join('; ');
}
