// REST Mail webmail service worker (#1572): Web Push notifications.
//
// A push carries only {"restmail":{"v":1,"device","account","unread"}} (plus
// "arrived" for a quiet-hours summary): never a sender, a subject or any text
// of the mail, so the notification says only that mail is waiting.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));

self.addEventListener('push', event => {
  event.waitUntil((async () => {
    let signal = {};
    try {
      signal = (event.data && event.data.json().restmail) || {};
    } catch {
      signal = {};
    }
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    // An open, visible tab shows new mail itself.
    if (windows.some(w => w.visibilityState === 'visible')) return;
    const unread = Number(signal.unread) || 0;
    const body = unread > 0 ? `${unread} unread in your inbox` : 'New mail in your inbox';
    await self.registration.showNotification('REST Mail', {
      body,
      icon: new URL('apple-touch-icon.png', self.registration.scope).href,
      tag: `restmail-${signal.account ?? 'mail'}`,
      renotify: true,
      data: { account: signal.account },
    });
  })());
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    // The webmail is wherever this worker is served: its scope.
    for (const w of windows) {
      if (w.url.startsWith(self.registration.scope) && 'focus' in w) return w.focus();
    }
    return self.clients.openWindow(self.registration.scope);
  })());
});
