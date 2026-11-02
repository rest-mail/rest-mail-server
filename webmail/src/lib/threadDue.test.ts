import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Children, createElement, isValidElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { dueInstant, dueLocalInput, threadDueChange } from './threadDue';
import { ThreadDueForm } from '../components/mail/ThreadDueControl';
import type { MessageDetail, MessageSummary } from '../types';

const due = '2026-12-01T10:00:00Z';
const message = (id: number, account = 1, thread = 'thread') => ({
  id, mailbox_id: account, thread_id: thread, folder: 'INBOX', received_at: '2026-01-01T00:00:00Z',
}) as MessageDetail;

test('setting and clearing updates only this account/thread, never order, folder, or selection', () => {
  const messages: MessageSummary[] = [message(2), message(1), message(3, 2), message(4, 1, 'other')];
  const state = { activeAccountId: 1, messages, searchResults: messages, selectedMessage: message(1) };
  const changed = threadDueChange(state, 1, 'thread', due, 'user');
  assert.deepEqual(changed.messages?.map(row => [row.id, row.folder]), messages.map(row => [row.id, row.folder]));
  assert.equal(changed.messages?.[0].due_source, 'user');
  assert.equal(changed.messages?.[2], messages[2]);
  assert.equal(changed.messages?.[3], messages[3]);
  assert.equal(changed.selectedMessage?.id, 1);
  assert.equal(changed.searchResults?.[1].due, due);
  assert.deepEqual(threadDueChange(state, 2, 'thread', due, 'user'), {});
  const cleared = threadDueChange({ ...state, ...changed }, 1, 'thread', null, null);
  assert.equal(cleared.selectedMessage?.due, null);
  assert.equal(cleared.selectedMessage?.due_source, null);
  assert.equal(cleared.messages?.length, messages.length);
});

test('local date picker round-trips an absolute instant and permits past due dates', () => {
  assert.equal(dueInstant(dueLocalInput(due)), dueInstant(due));
  assert.equal(dueInstant('2020-01-01T10:00:00Z'), '2020-01-01T10:00:00.000Z');
  assert.throws(() => dueInstant(''), /valid/);
  assert.throws(() => dueInstant('bad'), /valid/);
});

test('due form renders empty, saved with source, clearing, busy, and error states', () => {
  const props = { message: message(1), value: '', pending: false, error: '', onValue: () => {}, onSave: () => {}, onClear: () => {} };
  const render = (overrides: Partial<typeof props> = {}) => renderToStaticMarkup(createElement(ThreadDueForm, { ...props, ...overrides }));
  assert.match(render(), /Set due date/);
  assert.doesNotMatch(render(), /Clear due date|set by you/);
  const saved = { ...message(1), due, due_source: 'user' as const };
  assert.match(render({ message: saved }), /set by you/);
  assert.match(render({ message: saved }), /Clear due date/);
  assert.match(render({ message: saved }), /datetime="2026-12-01T10:00:00Z"/i);
  assert.doesNotMatch(render({ message: { ...saved, due: null, due_source: null } }), /Clear due date|set by you/);
  assert.match(render({ pending: true }), /Saving/);
  assert.match(render({ error: 'Could not save due date' }), /role="alert"[^>]*>Could not save due date/);
  assert.equal(render({ message: { ...message(1), thread_id: '' } }), '');
});

test('due form sends save, clear and picker events to their controls', () => {
  let saves = 0;
  let clears = 0;
  let prevented = false;
  let value = '';
  const rendered = ThreadDueForm({
    message: { ...message(1), due, due_source: 'user' }, value: dueLocalInput(due), pending: false, error: '',
    onSave: () => saves++, onClear: () => clears++, onValue: next => { value = next; },
  });
  assert.ok(rendered);
  const form = Children.toArray(rendered.props.children).find(child => isValidElement(child) && child.type === 'form');
  assert.ok(isValidElement<{ onSubmit: (event: { preventDefault: () => void }) => void; children: React.ReactNode }>(form));
  form.props.onSubmit({ preventDefault: () => { prevented = true; } });
  const children = Children.toArray(form.props.children);
  const clear = children.find(child => isValidElement(child) && child.props.children === 'Clear due date');
  assert.ok(isValidElement<{ onClick: () => void }>(clear));
  clear.props.onClick();
  const label = children.find(child => isValidElement(child) && child.type === 'label');
  assert.ok(isValidElement<{ children: React.ReactNode }>(label));
  const input = Children.toArray(label.props.children).find(child => isValidElement(child) && child.type === 'input');
  assert.ok(isValidElement<{ onChange: (event: { target: { value: string } }) => void }>(input));
  input.props.onChange({ target: { value: '2026-12-02T10:00' } });
  assert.equal(saves, 1);
  assert.equal(clears, 1);
  assert.equal(prevented, true);
  assert.equal(value, '2026-12-02T10:00');
});
