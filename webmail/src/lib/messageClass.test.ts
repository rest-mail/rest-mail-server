import assert from 'node:assert/strict';
import { test } from 'node:test';
import { visibleMessages } from './messageClass.ts';

test('only list-shaped newsletters collapse, never people or other classes', () => {
  const messages = ['person', 'newsletter', 'notification', 'receipt', undefined].map((messageClass, id) => ({ id, class: messageClass }));
  assert.deepEqual(visibleMessages(messages, true, null).map(message => message.id), [0, 2, 3, 4]);
  assert.deepEqual(visibleMessages(messages, false, null), messages);
  assert.deepEqual(visibleMessages(messages, true, 1), messages);
  assert.equal(messages.length, 5);
});
