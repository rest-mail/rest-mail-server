import type { ComposeState } from '@webmail/stores/uiStore';

// Mailto is a new message the Go side read from a mailto: link (RFC 6068).
export interface Mailto {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
  inReplyTo: string;
}

const escapeHtml = (text: string) => text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

// mailtoCompose turns a mailto: link into the initial state of a compose tab.
// The body is plain text, so it is escaped before it reaches the HTML editor.
export function mailtoCompose(link: Mailto): ComposeState {
  const state: ComposeState = { to: link.to, cc: link.cc, bcc: link.bcc, subject: link.subject };
  if (link.inReplyTo) state.inReplyTo = link.inReplyTo;
  if (link.body) {
    state.bodyText = link.body;
    state.bodyHtml = link.body.split('\n').map(line => `<p>${line ? escapeHtml(line) : '<br>'}</p>`).join('');
  }
  return state;
}
