import assert from 'node:assert/strict';
import test from 'node:test';
import { openMailStream, type SSEEvent } from './mailStream';

test('initial connection and reconnect resync folders even when the server lost its event replay ring', () => {
  const handlers = new Map<string, EventListener>();
  const events: SSEEvent[] = [];
  let closed = false;
  const close = openMailStream('/api/v1/accounts/7/events', event => events.push(event), (url, options) => {
    assert.equal(url, '/api/v1/accounts/7/events');
    assert.equal(options.withCredentials, true);
    return {
      addEventListener: (type, listener) => { handlers.set(type, listener); },
      close: () => { closed = true; },
    };
  });
  handlers.get('open')?.(new Event('open'));
  handlers.get('folder_update')?.({ data: '{"mailbox_id":7,"thread_id":"thread","snoozed_until":null}' } as MessageEvent);
  handlers.get('open')?.(new Event('open'));
  assert.deepEqual(events.map(event => event.type), ['stream_ready', 'folder_update', 'stream_ready']);
  assert.equal(events[1].data.snoozed_until, null);
  handlers.get('folder_update')?.({ data: 'invalid JSON' } as MessageEvent);
  assert.equal(events.length, 3);
  close();
  assert.equal(closed, true);
});
