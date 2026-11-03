import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ClassLabel } from '../components/mail/signals/ClassLabel';
import { messageClassLabel, visibleMessages } from './messageClass.ts';

test('only list-shaped newsletters collapse, never people or other classes', () => {
  const messages = ['person', 'newsletter', 'notification', 'receipt', undefined].map((messageClass, id) => ({ id, class: messageClass }));
  assert.deepEqual(visibleMessages(messages, true, null).map(message => message.id), [0, 2, 3, 4]);
  assert.deepEqual(visibleMessages(messages, false, null), messages);
  assert.deepEqual(visibleMessages(messages, true, 1), messages);
  assert.equal(messages.length, 5);
});

test('a person stays visible whatever else is collapsed or selected', () => {
  const messages = [{ id: 1, class: 'person' }, { id: 2, class: 'newsletter' }, { id: 3, class: 'person' }];
  for (const selected of [null, 1, 2, 3, 99]) {
    const visible = visibleMessages(messages, true, selected).map(message => message.id);
    assert.ok(visible.includes(1) && visible.includes(3), `selected ${selected} hid a person: ${visible}`);
  }
});

test('a receipt a rule assigned never collapses, even on mail with list headers', () => {
  // The server reports one class, and a rule's receipt replaces newsletter on list-shaped mail.
  assert.deepEqual(visibleMessages([{ id: 1, class: 'receipt' }], true, null).map(message => message.id), [1]);
});

test('only mail that is not from a person gets a class label', () => {
  assert.equal(messageClassLabel('newsletter'), 'list');
  assert.equal(messageClassLabel('receipt'), 'receipt');
  assert.equal(messageClassLabel('notification'), 'automated');
  for (const messageClass of ['person', 'unknown', undefined]) {
    assert.equal(messageClassLabel(messageClass), null);
  }
});

test('the mail list labels a class without calling any sender safe', () => {
  assert.equal(renderToStaticMarkup(createElement(ClassLabel, { messageClass: 'person' })), '');
  assert.equal(renderToStaticMarkup(createElement(ClassLabel, {})), '');
  const receipt = renderToStaticMarkup(createElement(ClassLabel, { messageClass: 'receipt' }));
  assert.match(receipt, />receipt</);
  assert.match(receipt, /mail rules/);
  assert.match(renderToStaticMarkup(createElement(ClassLabel, { messageClass: 'newsletter' })), /Anyone can send those headers/);
});
