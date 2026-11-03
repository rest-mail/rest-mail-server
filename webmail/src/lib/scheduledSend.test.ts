import assert from 'node:assert/strict';
import test from 'node:test';
import {
  CANCELLED_OUTCOME, cancelPrompt, scheduleTime, scheduledRow, scheduledSendRequest, sendTimeLabel,
  toLocalInput, withoutCancelled, type ScheduledMessage,
} from './scheduledSend';

// The person's own zone is the process zone here, as it is the browser's in the
// webmail. Europe/London moves its clocks on 2026-03-29 and 2026-10-25 at 01:00 UTC.
process.env.TZ = 'Europe/London';

test('a local time is sent as the UTC instant it names, in winter and in summer', () => {
  assert.deepEqual(scheduleTime('2026-12-01T09:30'), { utc: '2026-12-01T09:30:00.000Z', local: '2026-12-01T09:30', adjusted: false });
  assert.deepEqual(scheduleTime('2026-07-01T09:30'), { utc: '2026-07-01T08:30:00.000Z', local: '2026-07-01T09:30', adjusted: false });
});

test('a time skipped when the clocks go forward is moved on by the hour, and says so', () => {
  assert.deepEqual(scheduleTime('2026-03-29T01:30'), { utc: '2026-03-29T01:30:00.000Z', local: '2026-03-29T02:30', adjusted: true });
  // Either side of the gap is exact.
  assert.equal(scheduleTime('2026-03-29T00:59').utc, '2026-03-29T00:59:00.000Z');
  assert.equal(scheduleTime('2026-03-29T02:00').utc, '2026-03-29T01:00:00.000Z');
});

test('a time repeated when the clocks go back is the first of the two', () => {
  assert.deepEqual(scheduleTime('2026-10-25T01:30'), { utc: '2026-10-25T00:30:00.000Z', local: '2026-10-25T01:30', adjusted: false });
  assert.equal(scheduleTime('2026-10-25T02:00').utc, '2026-10-25T02:00:00.000Z');
});

test('a value that is not a time is refused before any request', () => {
  for (const value of ['', 'tomorrow', '2026-02-30T09:00', '2026-13-01T09:00', '2026-11-03T24:00', '2026-11-03T09:60']) {
    assert.throws(() => scheduleTime(value), /Choose a date and time to send/, value);
  }
});

test('the picker default round-trips through the local input form', () => {
  const at = new Date('2026-07-01T08:30:00Z');
  assert.equal(toLocalInput(at), '2026-07-01T09:30');
  assert.equal(scheduleTime(toLocalInput(at)).utc, at.toISOString());
});

test('the schedule request is the send request with scheduled_at in UTC', () => {
  const message = { from: 'me@example.test', to: ['you@example.test'], subject: 'Hi', body_text: 'Hello' };
  const request = scheduledSendRequest(message, scheduleTime('2026-07-01T09:30'));
  assert.deepEqual(request, { ...message, scheduled_at: '2026-07-01T08:30:00.000Z' });
  assert.equal('scheduled_at' in message, false);
});

const scheduled = (over: Partial<ScheduledMessage> = {}): ScheduledMessage => ({
  id: 1, account_id: 2, attempts: 0, created_at: '2026-11-03T10:00:00Z', updated_at: '2026-11-03T10:00:00Z',
  from: 'me@example.test', to: ['you@example.test', 'them@example.test'], subject: 'Report',
  scheduled_at: '2026-11-03T14:30:00Z', status: 'pending', ...over,
});

test('a listed message shows its send time in the viewer zone, naming the zone', () => {
  assert.match(sendTimeLabel('2026-11-03T14:30:00Z', 'en-US', 'America/New_York'), /Nov 3, 2026.*9:30\sAM EST/);
  assert.match(sendTimeLabel('2026-07-01T08:30:00Z', 'en-GB'), /1 Jul 2026.*09:30 BST/);
  const row = scheduledRow(scheduled(), 'en-US', 'Asia/Tokyo');
  assert.match(row.when, /Nov 3, 2026.*11:30\sPM GMT\+9/);
  assert.deepEqual({ ...row, when: '' }, {
    id: 1, when: '', subject: 'Report', to: ['you@example.test', 'them@example.test'], from: 'me@example.test',
    status: 'Waiting', cancellable: true, error: undefined,
  });
});

test('only a waiting or held message offers Cancel; one being sent cannot be called back', () => {
  assert.equal(scheduledRow(scheduled({ status: 'held', last_error: 'daily limit' })).cancellable, true);
  assert.equal(scheduledRow(scheduled({ status: 'held', last_error: 'daily limit' })).error, 'daily limit');
  assert.equal(scheduledRow(scheduled({ status: 'sending' })).cancellable, false);
  assert.equal(scheduledRow(scheduled({ status: 'sending' })).status, 'Sending now');
  assert.deepEqual(scheduledRow(scheduled({ subject: '', to: null })).to, []);
  assert.equal(scheduledRow(scheduled({ subject: '' })).subject, '(no subject)');
});

test('cancelling asks first, says the message is gone, and drops only that row', () => {
  assert.equal(cancelPrompt(scheduled()), `Cancel "Report"? ${CANCELLED_OUTCOME}`);
  assert.match(CANCELLED_OUTCOME, /not be sent.*not kept as a draft/);
  const list = [scheduled(), scheduled({ id: 2 }), scheduled({ id: 3 })];
  assert.deepEqual(withoutCancelled(list, 2).map(m => m.id), [1, 3]);
});
