import assert from 'node:assert/strict';
import { test } from 'node:test';
import { base64UrlToBytes, disableWebPush, enableWebPush, type WebPushDeps } from './webPush.ts';

const KEY = 'BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4';

function fakeDeps(over: Partial<WebPushDeps> = {}) {
  const calls: string[] = [];
  let stored: string | null = null;
  let subscribed = false;
  const subscription = {
    toJSON: () => ({ endpoint: 'https://push.example.com/s/1', keys: { p256dh: 'pk', auth: 'au' } }),
    unsubscribe: async () => { calls.push('unsubscribe'); subscribed = false; return true; },
  };
  const deps: WebPushDeps = {
    listDevices: async () => ({ platforms: ['apns', 'webpush'], web_push_key: KEY, devices: [] }),
    registerDevice: async body => { calls.push('register ' + JSON.stringify(body)); return { id: '12' }; },
    removeDevice: async id => { calls.push('remove ' + id); },
    requestPermission: async () => { calls.push('permission'); return 'granted'; },
    getSubscription: async () => (subscribed ? subscription : null),
    subscribe: async key => { calls.push('subscribe ' + key.length); subscribed = true; return subscription; },
    storage: { get: () => stored, set: id => { stored = id; } },
    ...over,
  };
  return { deps, calls, stored: () => stored };
}

test('the VAPID key decodes to its 65-byte point', () => {
  const bytes = base64UrlToBytes(KEY);
  assert.equal(bytes.length, 65);
  assert.equal(bytes[0], 4);
});

test('enabling asks permission, subscribes with the server key and registers the subscription', async () => {
  const f = fakeDeps();
  assert.equal(await enableWebPush(f.deps), 'enabled');
  assert.deepEqual(f.calls, [
    'permission',
    'subscribe 65',
    'register {"platform":"webpush","token":"https://push.example.com/s/1","keys":{"p256dh":"pk","auth":"au"}}',
  ]);
  assert.equal(f.stored(), '12');
});

test('a server without web push never asks for permission', async () => {
  const f = fakeDeps({ listDevices: async () => ({ platforms: ['apns'], devices: [] }) });
  assert.equal(await enableWebPush(f.deps), 'unsupported');
  const g = fakeDeps({ listDevices: async () => { throw new Error('404'); } });
  assert.equal(await enableWebPush(g.deps), 'unsupported');
  assert.deepEqual([...f.calls, ...g.calls], []);
});

test('a refused permission registers nothing', async () => {
  const f = fakeDeps({ requestPermission: async () => 'denied' });
  assert.equal(await enableWebPush(f.deps), 'denied');
  assert.equal(f.calls.some(c => c.startsWith('register') || c.startsWith('subscribe')), false);
  assert.equal(f.stored(), null);
});

test('disabling removes the registration and the subscription', async () => {
  const f = fakeDeps();
  await enableWebPush(f.deps);
  f.calls.length = 0;
  await disableWebPush(f.deps);
  assert.deepEqual(f.calls, ['remove 12', 'unsubscribe']);
  assert.equal(f.stored(), null);
});

test('disabling still unsubscribes when the server already removed the device', async () => {
  const f = fakeDeps({ removeDevice: async () => { throw new Error('404'); } });
  await enableWebPush(f.deps);
  f.calls.length = 0;
  await disableWebPush(f.deps);
  assert.deepEqual(f.calls, ['unsubscribe']);
});
