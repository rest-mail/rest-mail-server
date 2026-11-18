import { ApiError } from '../api/apiError';

/** The value to send for what the field holds. The API clears with null and refuses an empty name. */
export function timeZoneToStore(draft: string): string | null {
  return draft.trim() || null;
}

/** What to tell the person when a save fails: the API's reason if it refused the name. */
export function timeZoneRefusal(error: unknown): string {
  const reason = error instanceof ApiError ? error.fields.time_zone : undefined;
  return reason ? `Could not save the time zone: ${reason}.` : 'Could not save the time zone.';
}
