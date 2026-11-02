import assert from 'node:assert/strict';
import test from 'node:test';
import { handleThreadSnoozeEvent, snoozeDeadline, threadSnoozeChange } from './threadSnooze';
import { threadDueChange } from './threadDue';
import type { MessageDetail } from '../types';

const message: MessageDetail = {
  id: 1, mailbox_id: 2, thread_id: 'thread', class: 'person', folder: 'INBOX', message_id: 'one',
  sender: 'sender@example.test', sender_name: '', recipients_to: [], subject: 'one',
  size_bytes: 0, has_attachments: false, is_read: false, is_flagged: false,
  is_starred: false, is_draft: false, received_at: '', body_text: '', body_html: '',
  headers: null, in_reply_to: '', references: '',
};
const until = '2099-01-01T10:00:00Z';
const state = { activeAccountId: 2, messages: [message, { ...message, id: 3 }], selectedMessageId: 1, selectedMessage: message, searchResults: null };

test('snoozing removes the entire thread and reader without a page refresh', () => {
  const changed = threadSnoozeChange(state, 2, 'thread', until);
  assert.deepEqual(changed.messages, []);
  assert.equal(changed.selectedMessageId, null);
  assert.equal(changed.selectedMessage, null);
  assert.deepEqual(threadSnoozeChange(state, 9, 'thread', until), {});
});

test('search retains snoozed results and permits cancelling from the reader', () => {
  const changed = threadSnoozeChange({ ...state, searchResults: [message] }, 2, 'thread', until);
  assert.equal(changed.searchResults?.[0].snoozed_until, until);
  assert.equal(changed.selectedMessage?.snoozed_until, until);
  assert.equal(changed.selectedMessageId, 1);
  const cancelled = threadSnoozeChange({ ...state, ...changed }, 2, 'thread', null);
  assert.equal(cancelled.searchResults?.[0].snoozed_until, null);
  assert.equal(cancelled.selectedMessage?.snoozed_until, null);
});

test('return event clears snooze metadata before the live folder reload', () => {
  const changed = threadSnoozeChange({ ...state, messages: [], selectedMessage: { ...message, snoozed_until: until } }, 2, 'thread', null);
  assert.equal(changed.selectedMessage?.snoozed_until, null);
  assert.deepEqual(changed.messages, []);
});

test('due edits and snooze events preserve each other in search and the reader', () => {
  const due = '2099-01-02T10:00:00Z';
  let device: Parameters<typeof threadSnoozeChange>[0] = { ...state, searchResults: [message] };
  const apply = (mailboxId: number, threadId: string, deadline: string | null) => {
    device = { ...device, ...threadSnoozeChange(device, mailboxId, threadId, deadline) };
  };
  device = { ...device, ...threadDueChange(device, 2, 'thread', due, 'user') };
  handleThreadSnoozeEvent({ mailbox_id: 2, thread_id: 'thread', snoozed_until: until }, apply, () => {});
  assert.equal(device.searchResults?.[0].due, due);
  assert.equal(device.selectedMessage?.due_source, 'user');
  assert.equal(device.selectedMessage?.snoozed_until, until);
  device = { ...device, ...threadDueChange(device, 2, 'thread', null, null) };
  assert.equal(device.searchResults?.[0].snoozed_until, until);
  assert.equal(device.selectedMessage?.snoozed_until, until);
  device = { ...device, ...threadDueChange(device, 2, 'thread', due, 'user') };
  handleThreadSnoozeEvent({ mailbox_id: 2, thread_id: 'thread', snoozed_until: null }, apply, () => {});
  assert.equal(device.searchResults?.[0].due, due);
  assert.equal(device.selectedMessage?.due_source, 'user');
  assert.equal(device.selectedMessage?.snoozed_until, null);
});

test('a second device removes and reloads the returning thread from folder_update events', () => {
  let device: Parameters<typeof threadSnoozeChange>[0] = { ...state };
  let serverMessages = [...state.messages];
  let reloads = 0;
  const apply = (mailboxId: number, threadId: string, deadline: string | null) => {
    device = { ...device, ...threadSnoozeChange(device, mailboxId, threadId, deadline) };
  };
  const refresh = () => {
    reloads++;
    device = { ...device, messages: serverMessages };
  };
  serverMessages = [];
  assert.equal(handleThreadSnoozeEvent({ mailbox_id: 2, thread_id: 'thread', snoozed_until: until }, apply, refresh), true);
  assert.deepEqual(device.messages, []);
  assert.equal(device.selectedMessageId, null);
  serverMessages = [...state.messages];
  assert.equal(handleThreadSnoozeEvent({ mailbox_id: 2, thread_id: 'thread', snoozed_until: null }, apply, refresh), true);
  assert.deepEqual(device.messages, state.messages);
  assert.equal(reloads, 2);
  assert.equal(handleThreadSnoozeEvent({ folder: 'INBOX' }, apply, refresh), false);
  assert.equal(reloads, 2);
});

test('local picker time becomes an absolute instant and refuses invalid or past times', () => {
  const now = Date.parse('2026-01-01T00:00:00Z');
  assert.equal(snoozeDeadline('2099-01-01T10:00:00Z', now), '2099-01-01T10:00:00.000Z');
  for (const invalid of ['', 'not a date', '2020-01-01T00:00:00Z', '2026-01-01T00:00:00Z']) {
    assert.throws(() => snoozeDeadline(invalid, now), /future/);
  }
});
