import { useEffect, useRef } from 'react';
import { getToken } from '@/api/client';

export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

const EVENT_TYPES = new Set(['new_message', 'folder_update', 'message_updated', 'message_deleted', 'message_sent']);

/**
 * Parse a single SSE event block (the text between two blank lines).
 * Returns null for comment-only or empty blocks (e.g. keepalives).
 */
function parseBlock(block: string): { type: string; data: string; id: string } | null {
  let type = 'message';
  let data = '';
  let id = '';

  for (const line of block.split('\n')) {
    if (line.startsWith('event: ')) type = line.slice(7).trim();
    else if (line.startsWith('data: ')) data += (data ? '\n' : '') + line.slice(6);
    else if (line.startsWith('id: ')) id = line.slice(4).trim();
  }

  if (!data) return null;
  return { type, data, id };
}

/**
 * Open a fetch-based SSE stream using Authorization: Bearer header.
 * Returns a cleanup function that aborts the stream.
 */
function openStream(
  url: string,
  token: string,
  lastEventId: string,
  onEvent: (e: SSEEvent) => void,
  onError: () => void,
  onOpen: () => void,
): () => void {
  const controller = new AbortController();

  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
    Accept: 'text/event-stream',
    'Cache-Control': 'no-cache',
  };
  if (lastEventId) headers['Last-Event-ID'] = lastEventId;

  fetch(url, { headers, signal: controller.signal })
    .then(async (res) => {
      if (!res.ok || !res.body) {
        onError();
        return;
      }
      onOpen();

      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });

        // SSE events are separated by double newlines
        const blocks = buffer.split('\n\n');
        buffer = blocks.pop() ?? '';

        for (const block of blocks) {
          const parsed = parseBlock(block);
          if (!parsed) continue;
          if (!EVENT_TYPES.has(parsed.type)) continue;
          try {
            const data = JSON.parse(parsed.data);
            onEvent({ type: parsed.type, data });
          } catch {
            // ignore malformed JSON
          }
        }
      }
      // Stream closed cleanly — reconnect
      onError();
    })
    .catch(() => {
      if (!controller.signal.aborted) onError();
    });

  return () => controller.abort();
}

/**
 * Subscribe to SSE events for a single account.
 * Uses fetch() so we can send Authorization: Bearer header.
 * Includes exponential backoff on reconnect.
 */
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

    let closed = false;
    let delay = 1000;
    const maxDelay = 30000;
    const lastEventId = '';
    let cancelStream: (() => void) | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    function connect() {
      if (closed) return;
      // Use complete API URL
      const apiUrl = (import.meta.env.VITE_API_URL || 'http://localhost:8080/api/v1').replace(/\/+$/, '');
      const url = `${apiUrl}/accounts/${accountId}/events`;
      cancelStream = openStream(
        url, token, lastEventId,
        (e) => onEventRef.current(e),
        () => {
          // error / stream ended — reconnect with backoff
          if (!closed) {
            reconnectTimer = setTimeout(() => {
              delay = Math.min(delay * 2, maxDelay);
              connect();
            }, delay);
          }
        },
        () => { delay = 1000; }, // reset backoff on open
      );
    }

    connect();

    return () => {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      cancelStream?.();
    };
  }, [accountId]);
}

/**
 * Subscribe to SSE events for multiple accounts simultaneously.
 * Opens one fetch stream per account and forwards all events to the callback.
 */
export function useMultiAccountSSE(
  accountIds: number[],
  onEvent: (event: SSEEvent) => void,
) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  const idsKey = accountIds.join(',');

  useEffect(() => {
    const token = getToken();
    if (!token || accountIds.length === 0) return;

    const cleanups: (() => void)[] = [];

    for (const id of accountIds) {
      let closed = false;
      let delay = 1000;
      const maxDelay = 30000;
      const lastEventId = '';
      let cancelStream: (() => void) | null = null;
      let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

      function connect() {
        if (closed) return;
        // Use complete API URL
        const apiUrl = (import.meta.env.VITE_API_URL || 'http://localhost:8080/api/v1').replace(/\/+$/, '');
        const url = `${apiUrl}/accounts/${id}/events`;
        cancelStream = openStream(
          url, token, lastEventId,
          (e) => onEventRef.current(e),
          () => {
            if (!closed) {
              reconnectTimer = setTimeout(() => {
                delay = Math.min(delay * 2, maxDelay);
                connect();
              }, delay);
            }
          },
          () => { delay = 1000; },
        );
      }

      connect();

      cleanups.push(() => {
        closed = true;
        if (reconnectTimer) clearTimeout(reconnectTimer);
        cancelStream?.();
      });
    }

    return () => cleanups.forEach(fn => fn());
  }, [idsKey]); // eslint-disable-line react-hooks/exhaustive-deps
}
