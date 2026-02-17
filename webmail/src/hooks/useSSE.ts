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
