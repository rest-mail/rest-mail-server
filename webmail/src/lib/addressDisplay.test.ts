import assert from 'node:assert/strict';
import { test } from 'node:test';
import { displayAddress } from './addressDisplay.ts';

test('displays internationalized domains and preserves the local part', () => {
  for (const [input, expected] of [
    ['用户@xn--r8jz45g.jp', '用户@例え.jp'],
    ['münchen@xn--stdte-hra.de', 'münchen@städte.de'],
    ['User@Mail.XN--BCHER-KVA.example', 'User@Mail.bücher.example'],
    ['user@xn--fsqu00a.xn--0zwm56d', 'user@例子.测试'],
    ['"a@b"@xn--bcher-kva.de', '"a@b"@bücher.de'],
    ['xn--bcher-kva@xn--bcher-kva.de', 'xn--bcher-kva@bücher.de'],
  ]) {
    assert.equal(displayAddress(input), expected);
  }
});

test('leaves ASCII, Unicode, domain literals, and invalid addresses intact', () => {
  for (const input of [
    'User@Example.COM', '用户@例え.jp', 'user@[IPv6:2001:db8::1]', '',
    'xn--bcher-kva.de', '@xn--bcher-kva.de', 'user@',
    'user@xn--.example', 'user@xn--a.example',
    'user@xn--bcher-kva.de/path', 'user@xn--bcher-kva.de:443',
    'user@xn--bcher-kva.de?query', 'user@xn--bcher-kva.de#hash',
    'user@xn--bcher-kva.de\\path', 'user@xn--bcher-kva.de%2fpath',
    'user@xn--bcher-kva.de\n', 'Name <user@xn--bcher-kva.de>',
  ]) {
    assert.equal(displayAddress(input), input);
  }
});
