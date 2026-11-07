// Web Push in the webmail (#1572). The browser subscribes with its push
// service using the server's VAPID key, and registers the subscription with
// this session at /api/v1/devices, like the phone app registers its APNs
// token (#363). The push carries only an account id and its unread count;
// the service worker (public/sw.js) turns it into a notification.

export interface PushDeviceList {
  platforms: string[];
  web_push_key?: string;
  devices: { id: string; platform: string }[];
}

export interface WebPushSubscriptionJSON {
  endpoint?: string;
  keys?: Record<string, string>;
}

/** What enabling and disabling need of the API, the browser and storage. */
export interface WebPushDeps {
  listDevices(): Promise<PushDeviceList>;
  registerDevice(body: { platform: 'webpush'; token: string; keys: { p256dh: string; auth: string } }): Promise<{ id: string }>;
  removeDevice(id: string): Promise<void>;
  /** Asks the person; called only when they turn notifications on. */
  requestPermission(): Promise<NotificationPermission>;
  getSubscription(): Promise<{ toJSON(): WebPushSubscriptionJSON; unsubscribe(): Promise<boolean> } | null>;
  subscribe(applicationServerKey: Uint8Array): Promise<{ toJSON(): WebPushSubscriptionJSON; unsubscribe(): Promise<boolean> }>;
  storage: { get(): string | null; set(id: string | null): void };
}

export type EnableResult = 'enabled' | 'unsupported' | 'denied';

/** The base64url VAPID key as the bytes PushManager.subscribe wants. */
export function base64UrlToBytes(value: string): Uint8Array {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (value.length % 4)) % 4);
  const raw = atob(padded);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** Whether the server offers Web Push to this session. */
export async function webPushOffered(deps: Pick<WebPushDeps, 'listDevices'>): Promise<boolean> {
  try {
    const list = await deps.listDevices();
    return list.platforms.includes('webpush') && !!list.web_push_key;
  } catch {
    return false; // 404: push is not configured on this server
  }
}

/** Turns notifications on: permission, subscription, registration. */
export async function enableWebPush(deps: WebPushDeps): Promise<EnableResult> {
  let list: PushDeviceList;
  try {
    list = await deps.listDevices();
  } catch {
    return 'unsupported';
  }
  if (!list.platforms.includes('webpush') || !list.web_push_key) return 'unsupported';
  if ((await deps.requestPermission()) !== 'granted') return 'denied';

  const subscription = (await deps.getSubscription()) ?? (await deps.subscribe(base64UrlToBytes(list.web_push_key)));
  const json = subscription.toJSON();
  if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) {
    throw new Error('The browser gave an incomplete push subscription');
  }
  const device = await deps.registerDevice({
    platform: 'webpush',
    token: json.endpoint,
    keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
  });
  deps.storage.set(device.id);
  return 'enabled';
}

/** Turns notifications off: removes the registration and the subscription. */
export async function disableWebPush(deps: WebPushDeps): Promise<void> {
  const id = deps.storage.get();
  if (id) {
    try {
      await deps.removeDevice(id);
    } catch {
      // Already gone: the session ended, or the push service said so.
    }
  }
  deps.storage.set(null);
  const subscription = await deps.getSubscription();
  if (subscription) await subscription.unsubscribe();
}
