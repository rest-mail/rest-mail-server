'use strict';

const { test } = require('node:test');
const assert = require('node:assert');
const { startServer, request } = require('./helpers');

// A healthy script that returns a normal filter verdict.
const healthyScript =
  'function filter(email) { return { type: "action", action: "continue", log: { detail: "ok" } }; }';

// An ASYNC busy-loop: on a single-threaded server this saturates the microtask
// queue and starves the event loop for every other request (#201). Each script
// runs in its own Worker, under a CPU budget the QuickJS interrupt enforces and
// a wall-clock deadline the parent enforces with terminate().
const asyncBusyLoop =
  'async function filter(email) { while (true) { await 0; } }';

// Regression for #201: an async busy-loop must be stopped at its timeout and,
// crucially, must NOT hang the process — a concurrent /health and a concurrent
// healthy /execute must still respond promptly while the busy-loop is running.
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

test('a filter that awaits a promise nothing settles fails at once', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    body: { script: 'async function filter(email) { await new Promise(() => {}); }', email: {}, timeout_ms: 300 },
  });
  assert.strictEqual(res.status, 500);
  assert.match(res.json.error, /never returned/);
});

// /validate runs in the same isolate as /execute.
test('/validate checks syntax without running the script, and dry-runs it with an email', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());
  const validate = (body) => request(srv.port, { pathName: '/validate', body });

  const syntax = await validate({ script: 'function filter(email) { return ' });
  assert.strictEqual(syntax.status, 200);
  assert.strictEqual(syntax.json.valid, false);
  assert.strictEqual(syntax.json.phase, 'syntax');

  // Compiled, not run: the loop would time out if it ran.
  const compiled = await validate({ script: 'while (true) {} function filter(email) { return 1; }' });
  assert.deepStrictEqual(compiled.json, { valid: true });

  const dry = await validate({
    script: 'async function filter(email) { console.log(email.subject); return { type: "action", action: "reject" }; }',
    email: { subject: 'hello' },
  });
  assert.deepStrictEqual(dry.json, { valid: true, dry_run: { result: { type: 'action', action: 'reject' }, logs: ['hello'] } });

  const failing = await validate({ script: 'function filter(email) { throw new Error("nope"); }', email: {} });
  assert.strictEqual(failing.json.valid, true);
  assert.strictEqual(failing.json.phase, 'execution');
  assert.match(failing.json.dry_run.error, /nope/);

  const looping = await validate({ script: 'function filter(email) { while (true) {} }', email: {} });
  assert.strictEqual(looping.json.phase, 'execution');
  assert.match(looping.json.dry_run.error, /timeout/);
});
