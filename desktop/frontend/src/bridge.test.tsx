import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { useState } from 'react';
import { handleUIRequest, prepareScreenshotDocument, registerUIBridge, type BridgeOptions } from './bridge';
import type { RuntimeEvents } from './session';

function options(): BridgeOptions { return { route: () => '/signin', navigate: vi.fn(), screenshot: async () => 'iVBORw0KGgo=' }; }
describe('MCP UI bridge', () => {
  it('keeps closed disclosures closed in snapshots and screenshot clones without mutating the live UI', async () => {
    render(<details><summary data-testid="tools">More tools</summary><button data-testid="inside">Scheduled</button></details>);
    const snapshot = await handleUIRequest({ id: 'snap', action: 'snapshot' }, options()) as { elements: { testId: string }[] };
    expect(snapshot.elements.map(element => element.testId)).toEqual(['tools']);
    await expect(handleUIRequest({ id: 'hidden', action: 'click', args: { testId: 'inside' } }, options())).rejects.toThrow('Expected one element');
    const clone = document.implementation.createHTMLDocument(); clone.body.innerHTML = document.body.innerHTML;
    prepareScreenshotDocument(clone);
    expect(clone.querySelector('button')?.style.display).toBe('none');
    expect(clone.querySelector('summary')?.textContent).toContain('▸');
    expect(document.querySelector('button')?.style.display).toBe('');
    expect(clone.querySelector('style')?.textContent).toContain('animation: none');
  });
  it('exposes tabs and excludes inactive compose panels from targets and snapshots', async () => {
    const visible = vi.fn(); const hidden = vi.fn();
    render(<><div hidden><button data-testid="compose-send" onClick={hidden}>Hidden send</button></div><button data-testid="compose-send" onClick={visible}>Visible send</button></>);
    const config = { ...options(), tabs: () => [{ id: 'mail', active: false }, { id: 'compose-2', active: true }] };
    expect(await handleUIRequest({ id: 'tabs', action: 'tabs' }, config)).toEqual(config.tabs());
    const snapshot = await handleUIRequest({ id: 'snapshot', action: 'snapshot' }, config) as { elements: { text: string }[] };
    expect(snapshot.elements.map(element => element.text)).toEqual(['Visible send']);
    await handleUIRequest({ id: 'send', action: 'click', args: { testId: 'compose-send' } }, config);
    expect(visible).toHaveBeenCalledOnce(); expect(hidden).not.toHaveBeenCalled();
  });
  it('keeps generated IDs unique across repeated controls and snapshots', async () => {
    const click = vi.fn();
    render(<><button data-testid="control-inbox">Explicit Inbox</button><button onClick={click}>Inbox</button><button>Inbox</button></>);
    const request = { id: 'unique', action: 'snapshot' };
    const snapshot = await handleUIRequest(request, options()) as { elements: { testId: string }[] };
    const ids = snapshot.elements.map(element => element.testId);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).toEqual(['control-inbox', 'control-inbox-2', 'control-inbox-3']);
    expect(await handleUIRequest(request, options())).toEqual(snapshot);
    await handleUIRequest({ id: 'click', action: 'click', args: { testId: 'control-inbox-2' } }, options());
    expect(click).toHaveBeenCalledOnce();
  });
  it('snapshots stable test IDs on the sign-in screen before authentication', async () => {
    render(<><input data-testid="signin-password" type="password" defaultValue="secret" /><button data-testid="signin-submit">Sign in</button></>);
    const snapshot = await handleUIRequest({ id: '1', action: 'snapshot' }, options());
    expect(snapshot).toMatchObject({ route: '/signin', elements: [{ testId: 'signin-password', text: '[redacted]' }, { testId: 'signin-submit', text: 'Sign in' }] });
    expect(JSON.stringify(snapshot)).not.toContain('secret');
  });
  it('clicks controls and types through React controlled fields', async () => {
    const click = vi.fn();
    function Form() { const [value, setValue] = useState(''); return <><input aria-label="Email" data-testid="email" value={value} onChange={event => setValue(event.target.value)} /><span>{value}</span><button data-testid="send" onClick={click}>Send</button></>; }
    render(<Form />);
    await handleUIRequest({ id: '1', action: 'type', args: { testId: 'email', text: 'chris@test.invalid' } }, options());
    expect((screen.getByLabelText('Email') as HTMLInputElement).value).toBe('chris@test.invalid');
    await handleUIRequest({ id: '2', action: 'click', args: { testId: 'send' } }, options());
    expect(click).toHaveBeenCalledOnce();
    expect(await handleUIRequest({ id: '3', action: 'readText', args: { testId: 'email' } }, options())).toEqual({ text: 'chris@test.invalid' });
  });
  it('redacts enrollment secrets and recovery codes, including ancestor text and readText', async () => {
    render(<section data-testid="setup-card"><div data-ui-sensitive="true" data-testid="setup-secrets"><code data-testid="setup-key">private-enrollment-key</code><pre>private-recovery-code</pre></div></section>);
    const snapshot = JSON.stringify(await handleUIRequest({ id: '1', action: 'snapshot' }, options()));
    expect(snapshot).not.toContain('private-enrollment-key');
    expect(snapshot).not.toContain('private-recovery-code');
    for (const testId of ['setup-card', 'setup-secrets', 'setup-key']) expect(await handleUIRequest({ id: '2', action: 'readText', args: { testId } }, options())).toEqual({ text: '[redacted]' });
  });
  it('routes navigation and returns PNG data for screenshots', async () => {
    const config = options();
    await handleUIRequest({ id: '1', action: 'navigate', args: { route: '/compose' } }, config);
    expect(config.navigate).toHaveBeenCalledWith('/compose');
    expect(await handleUIRequest({ id: '2', action: 'screenshot' }, config)).toEqual({ data: 'iVBORw0KGgo=' });
  });
  it('registers launch listeners and correlates success and error responses', async () => {
    let listener: (data: unknown) => unknown = () => {};
    const emit = vi.fn(); const stop = vi.fn();
    const events: RuntimeEvents = { on: vi.fn((_name, handler) => { listener = handler; return stop; }), emit };
    const close = registerUIBridge(events, options());
    expect(events.on).toHaveBeenCalledWith('mcp:ui:request', expect.any(Function));
    await listener({ id: '1', action: 'snapshot' });
    expect(emit).toHaveBeenCalledWith('mcp:ui:response', expect.objectContaining({ id: '1', ok: true }));
    await listener({ id: '2', action: 'click', args: { testId: 'missing' } });
    expect(emit).toHaveBeenCalledWith('mcp:ui:response', expect.objectContaining({ id: '2', ok: false, error: 'Expected one element: missing' }));
    close(); expect(stop).toHaveBeenCalledOnce();
  });
  it('returns the renderer failure reason instead of an opaque screenshot error', async () => {
    let listener: (data: unknown) => unknown = () => {};
    const emit = vi.fn();
    const close = registerUIBridge({ on: (_name, handler) => { listener = handler; return () => {}; }, emit }, { ...options(), screenshot: async () => { throw new Error('Canvas export blocked by origin policy'); } });
    await listener({ id: 'capture', action: 'screenshot' });
    expect(emit).toHaveBeenCalledWith('mcp:ui:response', { id: 'capture', ok: false, error: 'Canvas export blocked by origin policy' });
    close();
  });
  it('responds only from the window targeted by MCP', async () => {
    let listener: (data: unknown) => unknown = () => {};
    const emit = vi.fn();
    const close = registerUIBridge({ on: (_name, handler) => { listener = handler; return () => {}; }, emit }, { ...options(), windowName: 'main' });
    await listener({ id: 'other', window: 'other', action: 'snapshot' });
    expect(emit).not.toHaveBeenCalled();
    await listener({ id: 'main', window: 'main', action: 'snapshot' });
    expect(emit).toHaveBeenCalledWith('mcp:ui:response', expect.objectContaining({ id: 'main', ok: true }));
    close();
  });
});
