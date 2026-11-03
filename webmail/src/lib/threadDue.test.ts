import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Children, createElement, isValidElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { dueInstant, dueLocalInput, dueToSave, initialDueInput, isDueSuggestion, threadDueChange } from './threadDue';
import { DueBadge } from '../components/mail/signals/ThreadSignals';
import { ThreadDueControl, ThreadDueForm } from '../components/mail/ThreadDueControl';
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
  const props = { message: message(1), value: '', pending: false, error: '', onValue: () => {}, onSave: () => {}, onClear: () => {}, onAccept: () => {} };
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
    onSave: () => saves++, onClear: () => clears++, onAccept: () => {}, onValue: next => { value = next; },
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

const suggestion = { ...message(1), due, due_source: 'parsed' as const };
const noForm = { value: '', pending: false, error: '', onValue: () => {}, onSave: () => {}, onClear: () => {}, onAccept: () => {} };

test('a parsed date is a suggestion, and only a parsed date is', () => {
  assert.equal(isDueSuggestion('parsed'), true);
  for (const source of ['user', 'rule', null, undefined]) assert.equal(isDueSuggestion(source), false);
});

test('a parsed date reads as a suggestion and names its source', () => {
  const later = renderToStaticMarkup(createElement(DueBadge, { due: '2099-12-01T10:00:00Z', source: 'parsed' }));
  assert.match(later, /suggested/);
  assert.match(later, /parsed from the message text/);
  assert.match(later, /not a deadline until you accept it/);
  const set = renderToStaticMarkup(createElement(DueBadge, { due: '2099-12-01T10:00:00Z', source: 'user' }));
  assert.doesNotMatch(set, /suggested/);
  assert.match(set, /set by you/);
});

test('a suggestion whose date has passed does not warn like a missed deadline', () => {
  const past = '2020-01-01T10:00:00Z';
  const suggested = renderToStaticMarkup(createElement(DueBadge, { due: past, source: 'parsed' }));
  assert.doesNotMatch(suggested, /past due/);
  assert.doesNotMatch(suggested, /--color-danger/);
  assert.match(suggested, /suggested/);
  assert.match(suggested, /ago/);
  assert.match(renderToStaticMarkup(createElement(DueBadge, { due: past, source: 'user' })), /past due/);
});

test('only a suggestion offers accept and dismiss, and it does not offer clear', () => {
  const render = (props: Partial<Parameters<typeof ThreadDueForm>[0]>) =>
    renderToStaticMarkup(createElement(ThreadDueForm, { message: suggestion, ...noForm, ...props }));
  assert.match(render({}), /Accept suggestion/);
  assert.match(render({}), /Dismiss suggestion/);
  assert.doesNotMatch(render({}), /Clear due date/);
  for (const source of ['user', 'rule'] as const) {
    const held = render({ message: { ...suggestion, due_source: source } });
    assert.doesNotMatch(held, /Accept suggestion|Dismiss suggestion/);
    assert.match(held, /Clear due date/);
  }
  assert.match(render({ pending: true }), /<button[^>]*disabled[^>]*>Accept suggestion/);
});

test('the picker never starts on a suggestion, so setting a date cannot accept one by accident', () => {
  assert.equal(initialDueInput(suggestion), '');
  assert.equal(initialDueInput({ ...suggestion, due_source: 'user' }), dueLocalInput(due));
  assert.equal(initialDueInput({ ...suggestion, due_source: 'rule' }), dueLocalInput(due));
  const rendered = renderToStaticMarkup(createElement(ThreadDueControl, { message: suggestion, setThreadDue: async () => {} }));
  assert.match(rendered, /type="datetime-local"[^>]*value=""/);
  assert.match(rendered, /<button type="submit" disabled/);
});

test('accept sends the suggested instant itself, dismiss clears, and set sends the picker', () => {
  const precise = { ...suggestion, due: '2026-12-01T10:00:37.250Z' };
  assert.equal(dueToSave('accept', precise, 'ignored'), precise.due);
  assert.equal(dueToSave('clear', precise, 'ignored'), null);
  assert.equal(dueToSave('set', precise, '2026-12-02T10:00:00Z'), '2026-12-02T10:00:00.000Z');
  assert.throws(() => dueToSave('accept', message(1), ''), /suggestion/);
});

test('accepting a suggestion makes it the user\'s without moving a row, and only the accept button does it', () => {
  const rows: MessageSummary[] = [
    { ...message(2), folder: 'INBOX', due: null, due_source: null },
    { ...message(1), folder: 'INBOX', due, due_source: 'parsed' },
    { ...message(5), folder: 'Archive', due, due_source: 'parsed' },
    { ...message(3, 1, 'other'), folder: 'INBOX', due: null, due_source: null },
  ];
  const state = { activeAccountId: 1, messages: rows, searchResults: rows, selectedMessage: suggestion };
  const accepted = threadDueChange(state, 1, 'thread', due, 'user');
  assert.deepEqual(accepted.messages?.map(row => [row.id, row.folder]), rows.map(row => [row.id, row.folder]));
  assert.deepEqual(accepted.searchResults?.map(row => row.id), rows.map(row => row.id));
  assert.equal(accepted.selectedMessage?.due_source, 'user');
  assert.equal(accepted.messages?.[1].due_source, 'user');
  assert.equal(accepted.messages?.[2].due_source, 'user');
  assert.equal(accepted.messages?.[2].folder, 'Archive');
  assert.equal(accepted.messages?.[3], rows[3]);

  let accepts = 0;
  let clears = 0;
  const rendered = ThreadDueForm({ message: suggestion, ...noForm, onAccept: () => accepts++, onClear: () => clears++ });
  assert.ok(rendered);
  const form = Children.toArray(rendered.props.children).find(child => isValidElement(child) && child.type === 'form');
  assert.ok(isValidElement<{ children: React.ReactNode }>(form));
  const buttons = Children.toArray(form.props.children);
  const named = (name: string) => buttons.find(child => isValidElement<{ children: React.ReactNode }>(child) && child.props.children === name);
  const accept = named('Accept suggestion');
  const dismiss = named('Dismiss suggestion');
  assert.ok(isValidElement<{ onClick: () => void }>(accept));
  assert.ok(isValidElement<{ onClick: () => void }>(dismiss));
  accept.props.onClick();
  assert.deepEqual([accepts, clears], [1, 0]);
  dismiss.props.onClick();
  assert.deepEqual([accepts, clears], [1, 1]);
});
