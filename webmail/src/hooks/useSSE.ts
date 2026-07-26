import { useEffect, useRef } from 'react';
import { API_BASE } from '../api/base';

export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

const EVENT_TYPES = ['new_message', 'folder_update', 'message_updated', 'message_deleted', 'message_sent'];

/**
 * Open a native EventSource for one account. Authentication is by cookie: the
 * browser attaches the httpOnly `restmail_access` cookie automatically
 * (withCredentials), so there is no token in JavaScript and none in the URL. The
 * browser also handles reconnection and Last-Event-ID replay natively.
 *
 * Returns a cleanup function that closes the stream.
 */
function openStream(accountId: number, onEvent: (e: SSEEvent) => void): () => void {
  const url = `${API_BASE}/accounts/${accountId}/events`;
  const es = new EventSource(url, { withCredentials: true });

  const handler = (type: string) => (evt: MessageEvent) => {
    try {
      onEvent({ type, data: JSON.parse(evt.data) });
    } catch {
      // ignore malformed JSON
    }
  };
  // The server names every event (`event: <type>`), so we subscribe per type
  // rather than the default `message` listener.
  for (const type of EVENT_TYPES) {
    es.addEventListener(type, handler(type) as EventListener);
  }

  return () => es.close();
}

/**
 * Subscribe to SSE events for a single account.
 */
export function useSSE(
  accountId: number | null,
  onEvent: (event: SSEEvent) => void,
) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!accountId) return;
    return openStream(accountId, (e) => onEventRef.current(e));
  }, [accountId]);
}

/**
 * Subscribe to SSE events for multiple accounts simultaneously.
 * Opens one EventSource per account and forwards all events to the callback.
 */
export function useMultiAccountSSE(
  accountIds: number[],
  onEvent: (event: SSEEvent) => void,
) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  const idsKey = accountIds.join(',');

  useEffect(() => {
    if (accountIds.length === 0) return;
    const cleanups = accountIds.map((id) => openStream(id, (e) => onEventRef.current(e)));
    return () => cleanups.forEach((fn) => fn());
  }, [idsKey]); // eslint-disable-line react-hooks/exhaustive-deps
}
