import assert from 'node:assert/strict';
import test from 'node:test';
import { unsubscribeChoice } from './unsubscribe';

test('one-click is chosen over mailto and links, and names the host it posts to', () => {
  const choice = unsubscribeChoice({
    one_click: 'https://news.example/u?t=1',
    links: ['https://news.example/page'],
    mailto: [{ to: 'leave@news.example', subject: 'unsubscribe', body: '' }],
  });
  assert.equal(choice?.method, 'one_click');
  assert.equal(choice?.target, 'https://news.example/u?t=1');
  assert.match(choice?.detail ?? '', /news\.example/);
});

test('mailto is chosen without one-click, and says it sends from the account', () => {
  const choice = unsubscribeChoice({ links: ['https://news.example/page'], mailto: [{ to: 'leave@news.example', subject: 'remove me', body: '' }] });
  assert.deepEqual(choice && { method: choice.method, target: choice.target }, { method: 'mailto', target: 'leave@news.example' });
  assert.ok(choice?.method === 'mailto' && choice.index === 0);
  assert.match(choice?.detail ?? '', /from your account to leave@news\.example with the subject "remove me"/);
});

test('a link alone is opened, not posted', () => {
  const choice = unsubscribeChoice({ links: ['https://news.example/page'], mailto: [] });
  assert.equal(choice?.method, 'link');
  assert.equal(choice?.target, 'https://news.example/page');
});

test('no targets offer no action', () => {
  assert.equal(unsubscribeChoice({ links: [], mailto: [] }), null);
  assert.equal(unsubscribeChoice(null), null);
});
