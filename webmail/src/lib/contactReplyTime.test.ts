import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Children, createElement, isValidElement, type ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ContactReplyTime, ContactReplyTimePreference } from '../components/contacts/ContactReplyTime.tsx';

test('reply-time label is absent while off or history is unknown', () => {
  for (const seconds of [undefined, null, 0, -1, Number.NaN]) {
    assert.equal(renderToStaticMarkup(createElement(ContactReplyTime, { seconds })), '');
  }
});

test('known reply time renders beside the contact with local-history context', () => {
  for (const [seconds, text] of [[7200, '2 hours'], [60, '1 minute'], [86400, '1 day'], [10, 'less than a minute']] as const) {
    const html = renderToStaticMarkup(createElement(ContactReplyTime, { seconds }));
    assert.ok(html.includes(`Usually replies in about ${text}`));
    assert.ok(html.includes('this account'));
  }
});

test('account setting is opt-in and shows loading, saved and error states', () => {
  const render = (enabled?: boolean, saving = false, error = '') => renderToStaticMarkup(createElement(ContactReplyTimePreference, {
    enabled, saving, error, onChange: () => {},
  }));
  assert.ok(render().includes('disabled=""'));
  assert.ok(!render(false).includes('checked=""'));
  assert.ok(render(true).includes('checked=""'));
  assert.ok(render(true, true).includes('disabled=""'));
  assert.ok(render(false, false, 'Could not save').includes('role="alert"'));
  assert.ok(render(false).includes('Nothing leaves the server'));
});

test('account checkbox forwards both opt-in and opt-out', () => {
  const changes: boolean[] = [];
  const view = ContactReplyTimePreference({ enabled: false, saving: false, error: '', onChange: value => changes.push(value) });
  const visit = (node: ReactNode) => {
    if (!isValidElement<{ children?: ReactNode; onChange: (event: { target: { checked: boolean } }) => void }>(node)) return;
    if (node.type === 'input') {
      node.props.onChange({ target: { checked: true } });
      node.props.onChange({ target: { checked: false } });
    }
    Children.forEach(node.props.children, visit);
  };
  visit(view);
  assert.deepEqual(changes, [true, false]);
});
