import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { QuietHoursPreference } from '../components/account/QuietHoursPreference.tsx';

test('quiet hours load disabled and explain delivery is unaffected', () => {
  const html = renderToStaticMarkup(createElement(QuietHoursPreference, {
    setting: { enabled: false, start: '', end: '', time_zone: '' }, saving: false, error: '', onChange() {}, onSave() {},
  }));
  assert.match(html, /Mail still arrives/);
  assert.match(html, /IANA time zone/);
  assert.doesNotMatch(html, /checked=""/);
});

test('quiet hours retain overnight times and the account time zone', () => {
  const html = renderToStaticMarkup(createElement(QuietHoursPreference, {
    setting: { enabled: true, start: '22:00', end: '08:00', time_zone: 'Europe/Berlin' }, saving: true, error: 'Failed to save', onChange() {}, onSave() {},
  }));
  assert.match(html, /value="22:00"/);
  assert.match(html, /value="08:00"/);
  assert.match(html, /value="Europe\/Berlin"/);
  assert.match(html, /disabled=""/);
  assert.match(html, /role="alert"/);
});

test('quiet hours cannot be edited before loading', () => {
  const html = renderToStaticMarkup(createElement(QuietHoursPreference, {
    saving: false, error: '', onChange() {}, onSave() {},
  }));
  assert.match(html, /Loading quiet hours/);
  assert.match(html, /disabled=""/);
});
