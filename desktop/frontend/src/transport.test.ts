import { describe, it, expect, vi } from 'vitest';
import { createDesktopTransport } from './transport';

describe('desktop proxy transport', () => {
  it('keeps API and restmail URLs relative and omits cookies and authorization', async () => {
    const send = vi.fn<typeof fetch>().mockResolvedValue(new Response('{}'));
    const transport = createDesktopTransport(send);
    await transport('/api/v1/messages', { headers: { Authorization: 'must-not-leave-js', 'X-CSRF-Token': 'cookie', 'Content-Type': 'application/json' }, credentials: 'include' });
    const [url, init] = send.mock.calls[0];
    expect(url).toBe('/api/v1/messages');
    expect(init?.credentials).toBe('omit');
    expect(init?.redirect).toBe('error');
    expect(new Headers(init?.headers).has('authorization')).toBe(false);
    expect(new Headers(init?.headers).has('x-csrf-token')).toBe(false);
    await transport('/restmail/health');
    expect(send.mock.calls[1][0]).toBe('/restmail/health');
    expect(localStorage.getItem('token')).toBeNull();
  });
  it('refuses absolute and protocol-relative URLs rather than contacting the server directly', async () => {
    const send = vi.fn<typeof fetch>();
    const transport = createDesktopTransport(send);
    for (const url of ['https://mail.test/api/v1/messages', '//mail.test/api/v1/messages', '/settings']) await expect(transport(url)).rejects.toThrow('relative API');
    expect(send).not.toHaveBeenCalled();
  });
});
