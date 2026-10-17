'use strict';

// #160: only the API may run a script. /execute and /validate take the shared
// JS_FILTER_TOKEN as a bearer token; the sidecar does not start without one.

const { test, before, after } = require('node:test');
const assert = require('node:assert');
const { spawn } = require('child_process');
const { startServer, request, SERVER } = require('./helpers');

const script = 'function filter(email) { return { type: "action", action: "continue" }; }';

let srv;

before(async () => {
  srv = await startServer();
});

after(() => srv.stop());

for (const pathName of ['/execute', '/validate']) {
  test(`${pathName} refuses a request without the token`, async () => {
    const res = await request(srv.port, { pathName, token: null, body: { script, email: {} } });
    assert.strictEqual(res.status, 401);
    assert.strictEqual(res.json.error, 'unauthorized');
  });

  test(`${pathName} refuses a wrong token`, async () => {
    const res = await request(srv.port, { pathName, token: 'not-the-token-0123456789', body: { script, email: {} } });
    assert.strictEqual(res.status, 401);
  });

  test(`${pathName} refuses another scheme`, async () => {
    const res = await request(srv.port, {
      pathName,
      token: null,
      headers: { Authorization: `Basic ${Buffer.from('api:x').toString('base64')}` },
      body: { script, email: {} },
    });
    assert.strictEqual(res.status, 401);
  });

  test(`${pathName} accepts the token`, async () => {
    const res = await request(srv.port, { pathName, body: { script, email: {} } });
    assert.strictEqual(res.status, 200);
  });
}

test('/health needs no token, for probes', async () => {
  const res = await request(srv.port, { method: 'GET', pathName: '/health', token: null });
  assert.strictEqual(res.status, 200);
});

for (const [name, value] of [['unset', undefined], ['short', 'too-short']]) {
  test(`the sidecar does not start with JS_FILTER_TOKEN ${name}`, async () => {
    const env = { ...process.env, PORT: '0' };
    delete env.JS_FILTER_TOKEN;
    if (value !== undefined) env.JS_FILTER_TOKEN = value;
    const child = spawn(process.execPath, [SERVER], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '';
    child.stdout.on('data', (d) => { out += d; });
    child.stderr.on('data', (d) => { out += d; });
    const code = await new Promise((resolve) => child.once('exit', resolve));
    assert.notStrictEqual(code, 0);
    assert.doesNotMatch(out, /listening/);
    assert.match(out, /JS_FILTER_TOKEN/);
  });
}
