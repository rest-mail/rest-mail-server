import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { useState } from 'react';
import { handleUIRequest, registerUIBridge, type BridgeOptions } from './bridge';
import type { RuntimeEvents } from './session';

function options(): BridgeOptions { return { route: () => '/signin', navigate: vi.fn(), screenshot: async () => 'iVBORw0KGgo=' }; }
describe('MCP UI bridge', () => {
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
    expect(snapshot).toMatchObject({ route: '/signin', elements: [{ testId: 'signin-password', text: '' }, { testId: 'signin-submit', text: 'Sign in' }] });
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
});
