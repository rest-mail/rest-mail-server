import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { AppPasswordsPanel, type AppPasswordsPanelProps } from '../components/settings/AppPasswordsPanel.tsx';
import { appPasswordRefusal, appPasswordsReducer, initialAppPasswords } from './appPasswords.ts';

const iphone = {
  id: 1, name: 'iPhone Mail', kind: 'app_password', public_id: '7Kq2xT9bLm4P',
  created_at: '2026-01-01T00:00:00Z', last_used_at: '2026-02-03T10:00:00Z', expires_at: null,
};
const laptop = {
  id: 2, name: 'Thunderbird', kind: 'app_password', public_id: 'Ab3dEf6hJk9M',
  created_at: '2026-03-01T00:00:00Z', last_used_at: null, expires_at: null,
};
const secret = 'rm_Zz9yXw8vUt7s_' + 'A'.repeat(43);
const made = {
  id: 3, name: 'Work laptop', kind: 'app_password', public_id: 'Zz9yXw8vUt7s',
  created_at: '2026-04-01T00:00:00Z', last_used_at: null, expires_at: null, password: secret,
};

function render(over: Partial<AppPasswordsPanelProps> = {}) {
  return renderToStaticMarkup(createElement(AppPasswordsPanel, {
    items: [], loading: false, created: null, confirmingRevoke: null, busy: false, error: null,
    onCreate() {}, onDismissCreated() {}, onConfirmRevoke() {}, onRevoke() {}, onSignInAgain() {},
    ...over,
  }));
}

test('app passwords list their name, when made and when last used', () => {
  const html = render({ items: [iphone, laptop] });
  assert.match(html, /aria-label="App passwords"/);
  assert.match(html, /iPhone Mail/);
  assert.match(html, /Thunderbird/);
  assert.match(html, /Last used/);
  assert.match(html, /Never used/);
  assert.match(html, /aria-label="Revoke iPhone Mail"/);
  assert.doesNotMatch(html, /rm_/);
});

test('the section says two-factor accounts need an app password for a mail app', () => {
  const html = render();
  assert.match(html, /two-factor authentication, your own password will not work/);
  assert.match(html, /You have no app passwords/);
  assert.match(html, /autoComplete="current-password"|autocomplete="current-password"/);
  assert.match(html, /maxLength="100"|maxlength="100"/);
});

test('a new app password is shown once, with a copy button and a warning', () => {
  let state = appPasswordsReducer(initialAppPasswords, { type: 'loaded', items: [iphone] });
  state = appPasswordsReducer(state, { type: 'created', created: made });
  assert.deepEqual(state.items.map(i => i.id), [3, 1]);
  assert.equal('password' in state.items[0], false, 'the list never holds the secret');

  const shown = render({ items: state.items, created: state.created });
  assert.equal(shown.split(secret).length - 1, 1, 'the secret appears exactly once');
  assert.match(shown, /You won&#x27;t see it again/);
  assert.match(shown, />Copy</);

  state = appPasswordsReducer(state, { type: 'dismissed' });
  assert.equal(state.created, null);
  const after = render({ items: state.items, created: state.created });
  assert.doesNotMatch(after, new RegExp(secret));
  assert.match(after, /Work laptop/);
});

test('revoking asks to confirm, then removes the app password', () => {
  let state = appPasswordsReducer(initialAppPasswords, { type: 'loaded', items: [iphone, laptop] });
  assert.doesNotMatch(render({ items: state.items }), /Apps using it will stop working/);

  state = appPasswordsReducer(state, { type: 'confirmRevoke', id: 1 });
  const confirming = render({ items: state.items, confirmingRevoke: state.confirmingRevoke });
  assert.match(confirming, /Apps using it will stop working/);
  assert.match(confirming, />Cancel</);

  state = appPasswordsReducer(state, { type: 'revoked', id: 1 });
  assert.deepEqual(state.items.map(i => i.id), [2]);
  assert.equal(state.confirmingRevoke, null);
});

test('a stale or missing second factor offers a new sign-in', () => {
  for (const code of ['recent_two_factor_required', 'two_factor_required']) {
    const refusal = appPasswordRefusal({ code });
    assert.equal(refusal.signIn, true);
    const html = render({ error: refusal });
    assert.match(html, /role="alert"/);
    assert.match(html, /Sign in again/);
  }
  const wrong = appPasswordRefusal({ code: 'invalid_password' });
  assert.equal(wrong.signIn, false);
  assert.doesNotMatch(render({ error: wrong }), /Sign in again/);
});
