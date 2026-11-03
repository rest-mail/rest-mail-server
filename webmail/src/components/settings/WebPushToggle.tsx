import { useEffect, useState } from 'react';
import { listPushDevices, registerPushDevice, removePushDevice } from '@/api/client';
import { disableWebPush, enableWebPush, webPushOffered, type WebPushDeps } from '@/lib/webPush';

// The device id this browser registered with its session, so turning push
// off removes exactly that registration.
const STORAGE_KEY = 'restmail.webPushDevice';

function browserDeps(): WebPushDeps {
  // Relative to the page's <base>: the webmail may be served under a path,
  // and the worker's scope is the directory it is served from.
  const registration = () => navigator.serviceWorker.register(new URL('sw.js', document.baseURI).href)
    .then(() => navigator.serviceWorker.ready);
  return {
    listDevices: listPushDevices,
    registerDevice: registerPushDevice,
    removeDevice: removePushDevice,
    requestPermission: () => Notification.requestPermission(),
    getSubscription: async () => (await registration()).pushManager.getSubscription(),
    subscribe: async key => (await registration()).pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: key as BufferSource,
    }),
    storage: {
      get: () => { try { return localStorage.getItem(STORAGE_KEY); } catch { return null; } },
      set: id => {
        try {
          if (id) localStorage.setItem(STORAGE_KEY, id); else localStorage.removeItem(STORAGE_KEY);
        } catch { /* storage blocked: the registration still ends with the session */ }
      },
    },
  };
}

/** Background notifications through Web Push: shown only when the server offers it. */
export function WebPushToggle() {
  const supported = typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window;
  const [offered, setOffered] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!supported) return;
    let disposed = false;
    const deps = browserDeps();
    webPushOffered(deps).then(ok => {
      if (disposed) return;
      setOffered(ok);
      setEnabled(ok && !!deps.storage.get() && Notification.permission === 'granted');
    });
    return () => { disposed = true; };
  }, [supported]);

  if (!supported || !offered) return null;

  async function change(on: boolean) {
    setBusy(true);
    setError('');
    try {
      if (on) {
        const result = await enableWebPush(browserDeps());
        if (result === 'denied') setError('The browser did not allow notifications.');
        setEnabled(result === 'enabled');
      } else {
        await disableWebPush(browserDeps());
        setEnabled(false);
      }
    } catch {
      setError('Could not change background notifications.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-1">
      <label className="flex items-center gap-2 text-sm font-medium">
        <input type="checkbox" checked={enabled} disabled={busy} onChange={event => change(event.target.checked)} />
        Notify me when no tab is open
      </label>
      <p className="text-xs text-muted-foreground">
        A push says only that mail is waiting and how much is unread, never who sent it or what it says.
        Quiet hours hold it back.
      </p>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </div>
  );
}
