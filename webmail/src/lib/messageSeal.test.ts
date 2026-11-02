import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { TrustReceipt } from '../components/mail/signals/TrustReceipt.tsx';
import { trustFromStoredResults } from './messageTrust.ts';

for (const [state, label] of [
  ['intact', 'Message unchanged since this server accepted it'],
  ['mismatch', 'Message changed since this server accepted it'],
  ['not_sealed', 'Not sealed'],
  ['checking', 'Checking receipt seal'],
  ['unavailable', 'Receipt seal could not be checked'],
] as const) {
  test(`trust receipt renders ${state} independently of sender authentication`, () => {
    const output = renderToStaticMarkup(createElement(TrustReceipt, {
      sealState: state,
      sealedHash: state === 'not_sealed' ? undefined : 'a'.repeat(64),
      defaultOpen: true,
      headers: { 'Authentication-Results': 'mx.test; spf=pass; dkim=pass; arc=pass; dmarc=pass' },
    }));
    assert.ok(output.includes(label), output);
    for (const method of ['spf', 'dkim', 'arc', 'dmarc']) assert.ok(output.includes(`>${method}<`), output);
    assert.ok(output.includes('does not identify the author or establish that the content is safe'), output);
    assert.ok(!output.includes('headers unchanged since receipt'), output);
    if (state !== 'mismatch') assert.ok(!output.includes('Message changed since'), output);
  });
}

test('an old unsealed message still has a trust receipt without authentication results', () => {
  const output = renderToStaticMarkup(createElement(TrustReceipt, { sealState: 'not_sealed', defaultOpen: true }));
  assert.ok(output.includes('Not sealed'), output);
  assert.ok(!output.includes('Message changed since'), output);
});

test('a digest alone is never presented as proof of integrity', () => {
  const output = renderToStaticMarkup(createElement(TrustReceipt, { sealedHash: 'a'.repeat(64), defaultOpen: true }));
  assert.ok(!output.includes('unchanged'), output);
});

test('the viewer uses recorded server verdicts instead of sender-supplied authentication headers', () => {
  const verdict = trustFromStoredResults([
    { method: 'spf', result: 'pass', domain: 'example.test', property: 'smtp.mailfrom' },
    { method: 'dkim', result: 'pass', domain: 'example.test', property: 'header.d' },
    { method: 'arc', result: 'none' },
    { method: 'dmarc', result: 'pass', domain: 'example.test', property: 'header.from' },
  ]);
  const output = renderToStaticMarkup(createElement(TrustReceipt, { verdict, sealState: 'not_sealed', defaultOpen: true }));
  for (const method of ['spf', 'dkim', 'arc', 'dmarc']) assert.ok(output.includes(`>${method}<`));
  const unrecorded = renderToStaticMarkup(createElement(TrustReceipt, {
    headers: { 'Authentication-Results': 'attacker.test; dkim=pass' },
    verdict: trustFromStoredResults(undefined), sealState: 'not_sealed', defaultOpen: true,
  }));
  assert.ok(!unrecorded.includes('>dkim<'));
  assert.ok(unrecorded.includes('Not sealed'));
});
