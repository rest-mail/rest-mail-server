import DOMPurify from 'dompurify';
import type { MessageSummary } from '@/types';

export function messagePreview(message: Pick<MessageSummary, 'body_text' | 'body_html' | 'has_attachments'>): string {
  const plain = message.body_text?.replace(/\s+/g, ' ').trim();
  if (plain) return plain;
  const html = message.body_html?.replace(/<\/(?:p|div|h[1-6]|li|tr)>|<br\s*\/?>/gi, ' ') || '';
  const fragment = DOMPurify.sanitize(html, { ALLOWED_TAGS: [], ALLOWED_ATTR: [], RETURN_DOM_FRAGMENT: true });
  return fragment.textContent?.replace(/\s+/g, ' ').trim() || (message.has_attachments ? 'Includes attachments' : '');
}
