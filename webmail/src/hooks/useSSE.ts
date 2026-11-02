import { useEffect, useRef } from 'react';
import { API_BASE } from '../api/base';
import { openMailStream, type SSEEvent } from '../lib/mailStream';
export type { SSEEvent } from '../lib/mailStream';

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
  return openMailStream(url, onEvent);
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
