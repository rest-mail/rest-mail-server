import { describe, expect, it } from 'vitest';
import { mailtoCompose } from './mailto';

describe('mailtoCompose', () => {
  it('fills the recipients and subject', () => {
    expect(mailtoCompose({ to: 'a@example.com', cc: 'b@example.com', bcc: 'c@example.com', subject: 'Hi', body: '', inReplyTo: '' }))
      .toEqual({ to: 'a@example.com', cc: 'b@example.com', bcc: 'c@example.com', subject: 'Hi' });
  });

  it('escapes the body for the HTML editor and keeps the plain text', () => {
    const state = mailtoCompose({ to: 'a@example.com', cc: '', bcc: '', subject: '', body: '<img src=x onerror=alert(1)>\n\nBye & "thanks"', inReplyTo: '<id@example.com>' });
    expect(state.bodyText).toBe('<img src=x onerror=alert(1)>\n\nBye & "thanks"');
    expect(state.bodyHtml).toBe('<p>&lt;img src=x onerror=alert(1)&gt;</p><p><br></p><p>Bye &amp; &quot;thanks&quot;</p>');
    expect(state.inReplyTo).toBe('<id@example.com>');
  });
});
