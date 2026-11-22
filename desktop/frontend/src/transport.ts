import type { IndexResponse } from './localMail';

export function createDesktopTransport(send: typeof fetch = globalThis.fetch, report: (answer: IndexResponse) => void = answer => { window.dispatchEvent(new CustomEvent('desktop:index-response', { detail: answer })); }): typeof fetch {
  let searchSerial = 0;
  let remote: AbortController | null = null;
  return async (input, options: RequestInit = {}) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    if (!/^\/(api|restmail)\//.test(path) || path.startsWith('//')) {
      throw new Error('Desktop requests must use a relative API proxy URL');
    }
    const headers = new Headers(input instanceof Request ? input.headers : undefined);
    new Headers(options.headers).forEach((value, key) => headers.set(key, value));
    headers.delete('authorization');
    headers.delete('x-csrf-token');
    const init: RequestInit = { ...options, headers, credentials: 'omit', redirect: 'error' };
    const search = /^\/api\/v1\/accounts\/\d+\/search\?/.test(path);
    const serial = search ? ++searchSerial : searchSerial;
    if (search) { remote?.abort(); remote = new AbortController(); }
    const response = await send(path, init);
    const source = response.headers.get('X-Restmail-Source');
    if (source) {
      const result = await response.clone().json().catch(() => undefined) as IndexResponse['result'];
      if (!search || serial === searchSerial) report({ path, source, offline: response.headers.get('X-Restmail-Offline') === 'true', result });
      if (search && source === 'local' && result?.complete === false) {
        const controller = remote!;
        window.setTimeout(() => {
          if (serial !== searchSerial || controller.signal.aborted || options.signal?.aborted) return;
          void send(`${path}&desktop_remote=1`, { ...init, signal: options.signal ? AbortSignal.any([options.signal, controller.signal]) : controller.signal }).then(async supplement => {
            if (!supplement.ok || serial !== searchSerial || controller.signal.aborted) return;
            const result = await supplement.json() as IndexResponse['result'];
            if (serial === searchSerial && !controller.signal.aborted) report({ path, source: supplement.headers.get('X-Restmail-Source') ?? 'local', offline: supplement.headers.get('X-Restmail-Offline') === 'true', result });
          }).catch(() => undefined);
        }, 0);
      }
    }
    return response;
  };
}
