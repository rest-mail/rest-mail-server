import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { noWaitingView, waitingPage } from './threadAwaiting';
import { ThreadSignals, WaitingBadge } from '../components/mail/signals/ThreadSignals';
import { ThreadDueForm } from '../components/mail/ThreadDueControl';
import type { AwaitingThread, MessageDetail, MessageSummary } from '../types';

const since = (ms: number) => new Date(Date.now() - ms).toISOString();
const days = 24 * 60 * 60 * 1000;

const thread = (id: number, folder = 'Sent'): AwaitingThread => ({
  thread_id: `<thread-${id}@example.test>`,
  awaiting_since: '2026-11-01T10:00:00Z',
  last_message: { id, mailbox_id: 1, thread_id: `<thread-${id}@example.test>`, folder } as MessageSummary,
});

test('the chip says how long a thread has waited', () => {
  const chip = renderToStaticMarkup(createElement(WaitingBadge, { since: since(2 * days) }));
  assert.match(chip, /waiting (<!-- -->)?2d/);
  assert.match(chip, /waiting on a reply/);
});

test('a thread with no waiting state, or an unreadable one, shows no chip', () => {
  assert.equal(renderToStaticMarkup(createElement(WaitingBadge, {})), '');
  assert.equal(renderToStaticMarkup(createElement(WaitingBadge, { since: null })), '');
  assert.equal(renderToStaticMarkup(createElement(WaitingBadge, { since: 'not a date' })), '');
  assert.equal(renderToStaticMarkup(createElement(ThreadSignals, {})), '');
});

test('the reader shows the chip beside the due date when the thread is waiting', () => {
  const form = (message: Partial<MessageDetail>) => renderToStaticMarkup(createElement(ThreadDueForm, {
    message: { mailbox_id: 1, thread_id: 'thread', ...message } as MessageDetail,
    value: '', pending: false, error: '', onValue: () => {}, onSave: () => {}, onClear: () => {}, onAccept: () => {},
  }));
  assert.match(form({ awaiting_since: since(3 * 60 * 60 * 1000) }), /waiting (<!-- -->)?3h/);
  assert.doesNotMatch(form({}), /waiting/);
  assert.doesNotMatch(form({ awaiting_since: null }), /waiting/);
});

const view = { activeAccountId: 1, ...noWaitingView };

test('a page of waiting threads becomes rows carrying their wait, across folders', () => {
  const page = waitingPage(view, 1, { data: [thread(2), thread(1, 'Archive')], pagination: { has_more: true, cursor: 'next' } }, false);
  assert.deepEqual(page.waitingMessages?.map(row => [row.id, row.folder, row.awaiting_since]), [
    [2, 'Sent', '2026-11-01T10:00:00Z'],
    [1, 'Archive', '2026-11-01T10:00:00Z'],
  ]);
  assert.equal(page.waitingHasMore, true);
  assert.equal(page.waitingCursor, 'next');
});

test('the next page is added after the rows already shown, and the last has no cursor', () => {
  const first = { ...view, ...waitingPage(view, 1, { data: [thread(2)], pagination: { has_more: true, cursor: 'next' } }, false) };
  const next = waitingPage(first, 1, { data: [thread(1)], pagination: { has_more: false } }, true);
  assert.deepEqual(next.waitingMessages?.map(row => row.id), [2, 1]);
  assert.equal(next.waitingHasMore, false);
  assert.equal(next.waitingCursor, null);
});

test('nothing waiting is an empty list, not the folder', () => {
  const page = waitingPage(view, 1, { data: [] }, false);
  assert.deepEqual(page.waitingMessages, []);
  assert.equal(page.waitingHasMore, false);
});

test('an answer for an account no longer shown changes nothing', () => {
  assert.deepEqual(waitingPage(view, 2, { data: [thread(1)] }, false), {});
});
