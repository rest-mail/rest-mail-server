import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { DeliveryStatus } from '../components/mail/DeliveryStatus.tsx';
import { deliverySummary, type RecipientStatus } from './deliveryStatus.ts';

const bounced: RecipientStatus = {
  address: 'nobody@example.com', delivery: 'remote', state: 'failed', attempts: 1,
  category: 'bad_mailbox', reason: "The recipient's mailbox does not exist.", last_attempt_at: '2026-11-03T12:00:00Z',
};
const delivered: RecipientStatus = { address: 'bob@example.com', delivery: 'remote', state: 'delivered', attempts: 1 };

test('a partly failed send shows each recipient with the readable reason', () => {
  const output = renderToStaticMarkup(createElement(DeliveryStatus, { recipients: [bounced, delivered] }));
  assert.ok(output.includes('Delivered to 1 of 2 recipients; 1 not delivered'), output);
  assert.ok(output.includes('nobody@example.com'), output);
  assert.ok(output.includes('The recipient&#x27;s mailbox does not exist.'), output);
  assert.ok(output.includes('bob@example.com'), output);
  assert.ok(output.includes('>Delivered<'), output);
  assert.ok(!/delivery failed/i.test(output), output);
});

test('a send delivered to everyone says so, without the per-recipient list', () => {
  const carol: RecipientStatus = { address: 'carol@example.com', delivery: 'local', state: 'delivered', attempts: 0 };
  const output = renderToStaticMarkup(createElement(DeliveryStatus, { recipients: [delivered, carol] }));
  assert.ok(output.includes('Delivered to all 2 recipients'), output);
  assert.ok(!output.includes('bob@example.com'), output);
  assert.equal(deliverySummary([delivered]), 'Delivered to its recipient');
});

test('a message with no recipients to report on shows nothing', () => {
  assert.equal(renderToStaticMarkup(createElement(DeliveryStatus, { recipients: [] })), '');
  assert.equal(deliverySummary([]), null);
});

test('copies still being sent are counted apart from failures', () => {
  const retrying: RecipientStatus = { address: 'later@example.com', delivery: 'remote', state: 'retrying', attempts: 2, category: 'greylisted', reason: 'x' };
  assert.equal(deliverySummary([retrying, delivered]), 'Delivered to 1 of 2 recipients; 1 still being sent');
});
