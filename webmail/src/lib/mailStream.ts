export interface SSEEvent {
  type: string;
  data: Record<string, unknown>;
}

interface MailEventSource {
  addEventListener(type: string, listener: EventListener): void;
  close(): void;
}

const EVENT_TYPES = ['new_message', 'folder_update', 'message_updated', 'message_deleted', 'message_sent'];

export function openMailStream(
  url: string,
  onEvent: (event: SSEEvent) => void,
  createSource: (url: string, options: EventSourceInit) => MailEventSource = (url, options) => new EventSource(url, options),
): () => void {
  const source = createSource(url, { withCredentials: true });
  source.addEventListener('open', () => onEvent({ type: 'stream_ready', data: {} }));
  for (const type of EVENT_TYPES) {
    source.addEventListener(type, event => {
      let data: Record<string, unknown>;
      try {
        data = JSON.parse((event as MessageEvent).data);
      } catch {
        return;
      }
      onEvent({ type, data });
    });
  }
  return () => source.close();
}
