import { Call } from '@wailsio/runtime';
import type { SSEEvent } from '@webmail/lib/mailStream';
import { runtimeEvents, type RuntimeEvents } from './session';

export function createNativeMailStream(events: RuntimeEvents, start: (accountId: number, id: string) => Promise<void>, stop: (id: string) => Promise<void>) {
  return (accountId: number, onEvent: (event: SSEEvent) => void) => {
    const id = crypto.randomUUID();
    let closed = false;
    const unsubscribe = events.on('mail:stream', data => {
      const message = data as { subscription: string; event: SSEEvent };
      if (!closed && message.subscription === id) onEvent(message.event);
    });
    const started = start(accountId, id).catch(error => console.error('Mail stream unavailable', error));
    return () => {
      closed = true;
      unsubscribe();
      void started.then(() => stop(id)).catch(error => console.error('Mail stream cleanup failed', error));
    };
  };
}

export const openNativeMailStream = createNativeMailStream(runtimeEvents,
  (accountId, id) => Call.ByName('main.MailStreams.Start', accountId, id),
  id => Call.ByName('main.MailStreams.Stop', id));
