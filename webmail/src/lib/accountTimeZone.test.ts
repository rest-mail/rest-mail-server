import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ApiError, apiErrorFromBody } from '../api/apiError.ts';
import { AccountTimeZonePreference } from '../components/account/AccountTimeZonePreference.tsx';
import { timeZoneRefusal, timeZoneToStore } from './accountTimeZone.ts';

function render(props: Partial<Parameters<typeof AccountTimeZonePreference>[0]>) {
  return renderToStaticMarkup(createElement(AccountTimeZonePreference, {
    draft: '', saving: false, error: '', onDraftChange() {}, onSave() {}, onClear() {}, ...props,
  }));
}

const clearButton = /<button[^>]*>Clear time zone<\/button>/;

test('an account with no zone is told IMAP uses UTC, and has nothing to clear', () => {
  const html = render({ saved: null });
  assert.match(html, /No time zone is set, so IMAP uses UTC/);
  assert.match(html, /IANA time zone/);
  assert.match(html, /placeholder="Europe\/Berlin"/);
  assert.match(html.match(clearButton)?.[0] ?? '', /disabled=""/);
});

test('an account with a zone shows it, and can clear it', () => {
  const html = render({ saved: 'Asia/Tokyo', draft: 'Asia/Tokyo' });
  assert.match(html, /IMAP uses Asia\/Tokyo/);
  assert.match(html, /value="Asia\/Tokyo"/);
  assert.doesNotMatch(html, /No time zone is set/);
  assert.doesNotMatch(html.match(clearButton)?.[0] ?? 'missing', /disabled=""/);
});

test('a name not yet saved does not change what the field says IMAP uses', () => {
  const html = render({ saved: null, draft: 'Mars/Olympus', error: 'Could not save the time zone: must be an IANA time zone name.' });
  assert.match(html, /No time zone is set, so IMAP uses UTC/);
  assert.match(html, /value="Mars\/Olympus"/);
  assert.match(html, /role="alert"[^>]*>Could not save the time zone: must be an IANA time zone name\./);
});

test('the time zone cannot be edited before it loads, or while it saves', () => {
  const loading = render({});
  assert.match(loading, /Loading the time zone/);
  assert.doesNotMatch(loading, /IMAP uses/);
  assert.match(loading, /<input[^>]*disabled=""/);
  const saving = render({ saved: 'Asia/Tokyo', draft: 'Asia/Tokyo', saving: true });
  assert.match(saving, /Saving\.\.\./);
  assert.match(saving, /<input[^>]*disabled=""/);
  assert.match(saving.match(clearButton)?.[0] ?? '', /disabled=""/);
});

test('a failed load says so instead of loading forever', () => {
  const html = render({ error: 'Could not load the time zone.' });
  assert.match(html, /role="alert"[^>]*>Could not load the time zone\./);
  assert.doesNotMatch(html, /Loading the time zone/);
  assert.match(html, /<input[^>]*disabled=""/);
});

test('a blank field clears the zone, because the API refuses an empty name', () => {
  assert.equal(timeZoneToStore(''), null);
  assert.equal(timeZoneToStore('   '), null);
  assert.equal(timeZoneToStore(' Europe/Berlin '), 'Europe/Berlin');
});

test("the API's refusal of an unknown name is what the person reads", () => {
  // The body the API sends for PUT /accounts/{id}/time-zone with an unknown name.
  const refusal = apiErrorFromBody(422, JSON.stringify({ error: {
    code: 'validation_failed',
    message: 'Request body failed validation',
    details: { fields: { time_zone: 'must be an IANA time zone name' } },
  } }));
  assert.equal(timeZoneRefusal(refusal), 'Could not save the time zone: must be an IANA time zone name.');
});

test('any other failure to save does not claim the name was wrong', () => {
  const fallback = 'Could not save the time zone.';
  assert.equal(timeZoneRefusal(new ApiError(403, 'forbidden', 'Forbidden')), fallback);
  assert.equal(timeZoneRefusal(apiErrorFromBody(502, '<html>Bad Gateway</html>')), fallback);
  assert.equal(timeZoneRefusal(new TypeError('Failed to fetch')), fallback);
});

test('an API error keeps its code and message, and a body that is not an error envelope stays readable', () => {
  const refused = apiErrorFromBody(403, JSON.stringify({ error: { code: 'forbidden', message: 'No access' } }));
  assert.ok(refused instanceof ApiError);
  assert.equal(refused.status, 403);
  assert.equal(refused.code, 'forbidden');
  assert.equal(refused.message, 'No access');
  assert.deepEqual(refused.fields, {});
  const unparsed = apiErrorFromBody(502, 'Bad Gateway');
  assert.equal(unparsed.code, 'unknown');
  assert.equal(unparsed.message, 'API error 502: Bad Gateway');
  assert.equal(apiErrorFromBody(500, 'null').message, 'API error 500: null');
});
