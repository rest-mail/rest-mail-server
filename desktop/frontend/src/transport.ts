export function createDesktopTransport(send: typeof fetch = globalThis.fetch): typeof fetch {
  return async (input, options: RequestInit = {}) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    if (!/^\/(api|restmail)\//.test(path) || path.startsWith('//')) {
      throw new Error('Desktop requests must use a relative API proxy URL');
    }
    const headers = new Headers(input instanceof Request ? input.headers : undefined);
    new Headers(options.headers).forEach((value, key) => headers.set(key, value));
    headers.delete('authorization');
    headers.delete('x-csrf-token');
    return send(path, { ...options, headers, credentials: 'omit', redirect: 'error' });
  };
}
