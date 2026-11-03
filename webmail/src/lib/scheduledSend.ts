import type { components } from '../api/types';

export type ScheduledMessage = components['schemas']['ScheduledMessage'];

/** What a datetime-local value means once read in the viewer's own time zone. */
export interface ScheduledTime {
  /** The instant, in UTC, as the API stores it (scheduled_at). */
  utc: string;
  /**
   * The local wall time the instant falls on. It differs from what was typed only
   * when the typed time does not exist that day (the hour skipped when clocks go
   * forward), where it is the time the clocks show instead.
   */
  local: string;
  adjusted: boolean;
}

const LOCAL_TIME = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

function pad(n: number): string {
  return String(n).padStart(2, '0');
}

/** toLocalInput writes an instant as a datetime-local value in the viewer's zone. */
export function toLocalInput(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * scheduleTime reads a datetime-local value ("2026-11-03T09:30") as a wall time in
 * the viewer's own time zone and answers the UTC instant the API is sent. The
 * server owns the rules on how far ahead it may be; this only refuses a value that
 * is not a time at all.
 *
 * A time that occurs twice (the hour repeated when clocks go back) is the first of
 * the two. A time that does not occur (the hour skipped when clocks go forward) is
 * moved on by the gap, and adjusted says so.
 */
export function scheduleTime(value: string): ScheduledTime {
  const match = LOCAL_TIME.exec(value.trim());
  if (!match) throw new Error('Choose a date and time to send');
  const [year, month, day, hour, minute, second] = match.slice(1).map(part => Number(part ?? 0));
  const daysInMonth = new Date(year, month, 0).getDate();
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth || hour > 23 || minute > 59 || second > 59) {
    throw new Error('Choose a date and time to send');
  }
  const at = new Date(year, month - 1, day, hour, minute, second);
  const local = toLocalInput(at);
  return {
    utc: at.toISOString(),
    local,
    adjusted: local !== `${match[1]}-${match[2]}-${match[3]}T${match[4]}:${match[5]}`,
  };
}

/** sendTimeLabel shows a stored UTC send time in the viewer's zone, naming the zone. */
export function sendTimeLabel(scheduledAt: string, locale?: string, timeZone?: string): string {
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    timeZoneName: 'short',
    timeZone,
  }).format(new Date(scheduledAt));
}

/** recipientsOf reads the stored to list, which the API describes only loosely. */
export function recipientsOf(message: ScheduledMessage): string[] {
  return Array.isArray(message.to) ? message.to.filter((a): a is string => typeof a === 'string') : [];
}

/** Each status the list can show, in the words the view uses. */
export function statusLabel(message: ScheduledMessage): string {
  switch (message.status) {
    case 'pending': return 'Waiting';
    case 'sending': return 'Sending now';
    case 'held': return 'Held by a send limit';
    default: return message.status;
  }
}

/** Only a waiting or held message can still be called back; one being sent cannot. */
export function canCancel(message: ScheduledMessage): boolean {
  return message.status === 'pending' || message.status === 'held';
}

/**
 * What happens to a cancelled message, as the API does it: it is marked cancelled
 * and never sent, and nothing is put back in Drafts.
 */
export const CANCELLED_OUTCOME = 'It will not be sent, and it is not kept as a draft.';

export function cancelPrompt(message: ScheduledMessage): string {
  return `Cancel "${message.subject || '(no subject)'}"? ${CANCELLED_OUTCOME}`;
}

/** One line of the Scheduled view, as the person reads it. */
export interface ScheduledRow {
  id: number;
  when: string;
  subject: string;
  to: string[];
  from: string;
  status: string;
  cancellable: boolean;
  error?: string;
}

/**
 * scheduledRow is what the Scheduled view shows for a message: its send time in
 * the viewer's zone, and whether it can still be cancelled. The API already
 * answers the list soonest first, so the view keeps its order.
 */
export function scheduledRow(message: ScheduledMessage, locale?: string, timeZone?: string): ScheduledRow {
  return {
    id: message.id,
    when: sendTimeLabel(message.scheduled_at, locale, timeZone),
    subject: message.subject || '(no subject)',
    to: recipientsOf(message),
    from: message.from,
    status: statusLabel(message),
    cancellable: canCancel(message),
    error: message.last_error || undefined,
  };
}

/** withoutCancelled is the list after a cancel the API accepted. */
export function withoutCancelled(list: ScheduledMessage[], id: number): ScheduledMessage[] {
  return list.filter(message => message.id !== id);
}

/** A scheduled send's request: the message as sendMessage takes it, plus when. */
export function scheduledSendRequest<T extends object>(message: T, when: ScheduledTime): T & { scheduled_at: string } {
  return { ...message, scheduled_at: when.utc };
}
