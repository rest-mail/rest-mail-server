import { useEffect, useRef } from 'react';
import { getToken } from '@/api/client';

export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

const EVENT_TYPES = ['new_message', 'folder_update', 'message_updated', 'message_deleted', 'message_sent'];

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

    let es: EventSource | null = null;
    let delay = 1000;
    const maxDelay = 30000;
    let closed = false;

    function connect() {
      if (closed) return;

      const url = `/api/v1/accounts/${accountId}/events?token=${encodeURIComponent(token)}`;
      es = new EventSource(url);

      const handleEvent = (e: MessageEvent) => {
        try {
          const data = JSON.parse(e.data);
          onEventRef.current({ type: e.type, data });
        } catch {
          // ignore malformed events
        }
      };

      EVENT_TYPES.forEach(type => es!.addEventListener(type, handleEvent));

      es.onopen = () => {
        delay = 1000; // reset backoff on successful connection
      };

      es.onerror = () => {
        es?.close();
        if (!closed) {
          setTimeout(connect, delay);
          delay = Math.min(delay * 2, maxDelay);
        }
      };
    }

    connect();

    return () => {
      closed = true;
      es?.close();
    };
  }, [accountId]);
}

/**
 * Subscribe to SSE events for multiple accounts simultaneously.
 * Opens one EventSource per account and forwards all events to the callback.
 * Includes exponential backoff on reconnect.
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

    const cleanups: (() => void)[] = [];

    for (const id of accountIds) {
      let es: EventSource | null = null;
      let delay = 1000;
      const maxDelay = 30000;
      let closed = false;

      function connect() {
        if (closed) return;

        const url = `/api/v1/accounts/${id}/events?token=${encodeURIComponent(token)}`;
        es = new EventSource(url);

        const handleEvent = (e: MessageEvent) => {
          try {
            const data = JSON.parse(e.data);
            onEventRef.current({ type: e.type, data });
          } catch {
            // ignore malformed events
          }
        };

        EVENT_TYPES.forEach(type => es!.addEventListener(type, handleEvent));

        es.onopen = () => {
          delay = 1000;
        };

        es.onerror = () => {
          es?.close();
          if (!closed) {
            setTimeout(connect, delay);
            delay = Math.min(delay * 2, maxDelay);
          }
        };
      }

      connect();

      cleanups.push(() => {
        closed = true;
        es?.close();
      });
    }

    return () => {
      cleanups.forEach(fn => fn());
    };
  }, [idsKey]); // eslint-disable-line react-hooks/exhaustive-deps
}
