import type { RuntimeEvents } from './session';

export interface UIRequest { id: string; action: string; args?: Record<string, unknown> }
export interface BridgeOptions {
  route: () => string;
  navigate: (route: string) => void | Promise<void>;
  screenshot: () => Promise<string>;
}
const interactive = 'button, input, select, textarea, a[href], [contenteditable="true"], [role="button"], [role="menuitem"], [role="checkbox"], [tabindex="0"]';
const slug = (text: string) => text.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 80);

export function tagInteractiveElements(root: Document = document) {
  const counts = new Map<string, number>();
  const used = new Set([...root.querySelectorAll<HTMLElement>('[data-testid]')].map(element => element.dataset.testid));
  for (const element of root.querySelectorAll<HTMLElement>(interactive)) {
    if (element.dataset.testid) continue;
    const label = element.getAttribute('aria-label') || element.getAttribute('title') || element.getAttribute('name') || element.getAttribute('placeholder') || element.textContent || element.tagName;
    const base = `control-${slug(label) || element.tagName.toLowerCase()}`;
    let count = (counts.get(base) ?? 0) + 1;
    while (used.has(count === 1 ? base : `${base}-${count}`)) count++;
    counts.set(base, count);
    element.dataset.testid = count === 1 ? base : `${base}-${count}`;
    used.add(element.dataset.testid);
  }
}

function target(testId: unknown) {
  if (typeof testId !== 'string') throw new Error('testId is required');
  const matches = [...document.querySelectorAll<HTMLElement>('[data-testid]')].filter(element => element.dataset.testid === testId);
  if (matches.length !== 1) throw new Error(`Expected one element: ${testId}`);
  const element = matches[0];
  if (element.matches(':disabled, [aria-disabled="true"]')) throw new Error(`Element disabled: ${testId}`);
  return element;
}

export async function handleUIRequest(request: UIRequest, options: BridgeOptions): Promise<unknown> {
  tagInteractiveElements();
  const args = request.args ?? {};
  switch (request.action) {
    case 'snapshot': return {
      route: options.route(), title: document.title,
      elements: [...document.querySelectorAll<HTMLElement>('[data-testid]')].map(element => ({
        testId: element.dataset.testid, role: element.getAttribute('role') || element.tagName.toLowerCase(),
        text: element.getAttribute('aria-label') || element.textContent?.trim().slice(0, 250) || element.getAttribute('placeholder') || '',
        enabled: !element.matches(':disabled, [aria-disabled="true"]'),
      })),
    };
    case 'click': target(args.testId).click(); return {};
    case 'type': {
      const element = target(args.testId);
      if (typeof args.text !== 'string') throw new Error('text is required');
      element.focus();
      if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
        const prototype = element instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
        Object.getOwnPropertyDescriptor(prototype, 'value')!.set!.call(element, args.text);
        element.dispatchEvent(new Event('input', { bubbles: true }));
        element.dispatchEvent(new Event('change', { bubbles: true }));
      } else if (element.isContentEditable || element.getAttribute('contenteditable') === 'true') {
        document.execCommand('selectAll');
        document.execCommand('insertText', false, args.text);
      } else throw new Error('Element is not a text field');
      return {};
    }
    case 'navigate':
      if (typeof args.route !== 'string') throw new Error('route is required');
      await options.navigate(args.route); return { route: options.route() };
    case 'readText': {
      const element = target(args.testId);
      return { text: element instanceof HTMLInputElement && element.type !== 'password' ? element.value : element.textContent?.trim() || '' };
    }
    case 'screenshot': return { data: await options.screenshot() };
    default: throw new Error(`Unsupported UI action: ${request.action}`);
  }
}

export function registerUIBridge(events: RuntimeEvents, options: BridgeOptions) {
  const stop = events.on('mcp:ui:request', async (data) => {
    const request = data as UIRequest;
    try {
      const result = await handleUIRequest(request, options);
      events.emit('mcp:ui:response', { id: request.id, ok: true, result });
    } catch (error) {
      events.emit('mcp:ui:response', { id: request.id, ok: false, error: error instanceof Error ? error.message : 'UI request failed' });
    }
  });
  const observer = new MutationObserver(() => tagInteractiveElements());
  observer.observe(document.documentElement, { childList: true, subtree: true });
  tagInteractiveElements();
  return () => { stop(); observer.disconnect(); };
}
