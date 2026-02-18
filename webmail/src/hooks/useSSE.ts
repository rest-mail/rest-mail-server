import { useEffect, useRef } from 'react';
import { getToken } from '@/api/client';

export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

export function useSSE(
  accountId: number | null,
  onEvent: (event: SSEEvent) => void,
) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!accountId) return;

    const token = getToken();
    if (!token) return;

    const url = `/api/v1/accounts/${accountId}/events?token=${encodeURIComponent(token)}`;
    const es = new EventSource(url);

    const handleEvent = (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data);
        onEventRef.current({ type: e.type, data });
      } catch {
        // ignore malformed events
      }
    };

    es.addEventListener('new_message', handleEvent);
    es.addEventListener('folder_update', handleEvent);
    es.addEventListener('message_updated', handleEvent);
    es.addEventListener('message_deleted', handleEvent);

    es.onerror = () => {
      // EventSource auto-reconnects on error
    };

    return () => {
      es.close();
    };
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

  // Stable serialization of account IDs for the dependency array
  const idsKey = accountIds.join(',');

  useEffect(() => {
    const token = getToken();
    if (!token || accountIds.length === 0) return;

    const sources: EventSource[] = [];

    for (const id of accountIds) {
      const url = `/api/v1/accounts/${id}/events?token=${encodeURIComponent(token)}`;
      const es = new EventSource(url);

      const handleEvent = (e: MessageEvent) => {
        try {
          const data = JSON.parse(e.data);
          onEventRef.current({ type: e.type, data });
        } catch {
          // ignore malformed events
        }
      };

      es.addEventListener('new_message', handleEvent);
      es.addEventListener('folder_update', handleEvent);
      es.addEventListener('message_updated', handleEvent);
      es.addEventListener('message_deleted', handleEvent);

      es.onerror = () => {
        // EventSource auto-reconnects on error
      };

      sources.push(es);
    }

    return () => {
      sources.forEach(es => es.close());
    };
  }, [idsKey]); // eslint-disable-line react-hooks/exhaustive-deps
}
