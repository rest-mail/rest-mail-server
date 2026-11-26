import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatListDate } from './listDate.ts';

const now = new Date(2026, 10, 22, 9, 30); // Sun 22 Nov 2026, 09:30 local
const at = (y: number, m: number, d: number, h = 12) => new Date(y, m, d, h);

test('desktop shows native mail-client dates', () => {
  assert.equal(formatListDate(at(2026, 10, 22, 8), now, true, 'en-GB'), '08:00');
  assert.equal(formatListDate(at(2026, 10, 21), now, true, 'en-GB'), 'Yesterday');
  assert.equal(formatListDate(at(2026, 10, 18), now, true, 'en-GB'), 'Wed');
  assert.equal(formatListDate(at(2026, 10, 10), now, true, 'en-GB'), '10 Nov');
  assert.equal(formatListDate(at(2025, 0, 3), now, true, 'en-GB'), '3 Jan 2025');
});

test('desktop never shows the terminal-style underscore', () => {
  for (const day of [at(2026, 10, 20), at(2026, 5, 1), at(2024, 11, 31)]) {
    assert.doesNotMatch(formatListDate(day, now, true, 'en-GB'), /_/);
  }
});

test('webmail keeps its terminal style', () => {
  assert.equal(formatListDate(at(2026, 10, 21), now, false, 'en-GB'), 'yesterday');
  assert.equal(formatListDate(at(2026, 10, 20), now, false, 'en-US'), 'nov_20');
});
