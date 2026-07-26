'use strict';

const { test } = require('node:test');
const assert = require('node:assert');
const { startServer, request } = require('./helpers');

// A healthy script that returns a normal filter verdict.
const healthyScript =
  'function filter(email) { return { type: "action", action: "continue", log: { detail: "ok" } }; }';

// An ASYNC busy-loop: vm.runInContext({timeout}) bounds only SYNCHRONOUS work, so
// this escapes it and, on the single-threaded pre-fix server, saturates the
// microtask queue and starves the event loop for every other request.
const asyncBusyLoop =
  'async function filter(email) { while (true) { await 0; } }';

// Core regression for #160 / #201: an async busy-loop must be HARD-terminated at
// the wall-clock timeout (worker.terminate()) and, crucially, must NOT hang the
// process — a concurrent /health and a concurrent healthy /execute must still
// respond promptly while the busy-loop is running.
//
// RED (pre-fix): the busy loop runs on the main thread and starves the event
// loop, so the concurrent /health request times out. GREEN (post-fix): the loop
// runs in a Worker, the main thread stays responsive, and the loop is killed.
test('async busy-loop is isolated and hard-terminated; other requests stay responsive', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  // Kick off the async busy-loop. Give it a short script timeout so the
  // wall-clock terminate fires quickly. Do NOT await it yet.
  const busy = request(srv.port, {
    pathName: '/execute',
    body: { script: asyncBusyLoop, email: { from: 'a@b.c' }, timeout_ms: 300 },
    timeoutMs: 9000,
  }).catch((e) => ({ error: e }));

  // Let the busy loop get going.
  await new Promise((r) => setTimeout(r, 200));

  // The event loop must still service other work. On the pre-fix server these
  // time out; here they must return promptly.
  const health = await request(srv.port, {
    method: 'GET',
    pathName: '/health',
    timeoutMs: 1500,
  });
  assert.strictEqual(health.status, 200, '/health must respond while a busy-loop runs');
  assert.strictEqual(health.json && health.json.status, 'ok');

  const concurrent = await request(srv.port, {
    pathName: '/execute',
    body: { script: healthyScript, email: { from: 'x@y.z' }, timeout_ms: 300 },
    timeoutMs: 1500,
  });
  assert.strictEqual(concurrent.status, 200, 'a concurrent healthy /execute must respond');
  assert.strictEqual(concurrent.json.result.action, 'continue');

  // The busy-loop request itself must resolve with a definite timeout error
  // (HTTP 408) that the Go filter maps to its failure action — never a silent
  // success.
  const busyResult = await busy;
  assert.ok(!busyResult.error, 'busy-loop request should have returned an HTTP response, not hung');
  assert.strictEqual(busyResult.status, 408, 'async busy-loop must return an HTTP 408 timeout');
  assert.match(busyResult.json.error, /timeout/i);
});

test('a script that throws returns a definite 500 error (fail-closed on the Go side)', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    pathName: '/execute',
    body: {
      script: 'function filter(email) { throw new Error("kaboom"); }',
      email: { from: 'a@b.c' },
      timeout_ms: 300,
    },
    timeoutMs: 4000,
  });
  assert.strictEqual(res.status, 500);
  assert.match(res.json.error, /kaboom/);
});

test('a synchronous infinite loop is bounded and returns a timeout', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    pathName: '/execute',
    body: {
      script: 'function filter(email) { while (true) {} }',
      email: { from: 'a@b.c' },
      timeout_ms: 200,
    },
    timeoutMs: 6000,
  });
  assert.strictEqual(res.status, 408, 'a sync infinite loop must time out, not hang');
  assert.match(res.json.error, /timeout/i);
});

test('healthy script returns the expected {result, logs} contract', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    pathName: '/execute',
    body: {
      script:
        'function filter(email) { console.log("hi"); return { type: "action", action: "reject", log: { detail: "spam" } }; }',
      email: { from: 'a@b.c' },
      timeout_ms: 500,
    },
    timeoutMs: 4000,
  });
  assert.strictEqual(res.status, 200);
  assert.strictEqual(res.json.result.type, 'action');
  assert.strictEqual(res.json.result.action, 'reject');
  assert.deepStrictEqual(res.json.logs, ['hi']);
});
