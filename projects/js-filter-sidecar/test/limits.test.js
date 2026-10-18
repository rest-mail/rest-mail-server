'use strict';

const { test } = require('node:test');
const assert = require('node:assert');
const http = require('http');
const { startServer, request } = require('./helpers');

// Body cap: an oversized request body must be rejected with a definite error
// (HTTP 413) instead of being buffered unbounded.
test('over-limit request body is rejected via Content-Length fast path', async (t) => {
  const srv = await startServer({ JS_FILTER_MAX_BODY_BYTES: '2048' });
  t.after(() => srv.stop());

  const big = 'x'.repeat(8000);
  const res = await request(srv.port, {
    pathName: '/execute',
    body: {
      script: 'function filter(email){ return { type:"action", action:"continue" }; }',
      email: { from: 'a@b.c', junk: big },
      timeout_ms: 200,
    },
    timeoutMs: 4000,
  });
  assert.strictEqual(res.status, 413, 'oversized body must be rejected with 413');
  assert.match(res.json.error, /too large|limit|exceed/i);
});

test('over-limit request body is rejected on the streaming path (chunked, no Content-Length)', async (t) => {
  const srv = await startServer({ JS_FILTER_MAX_BODY_BYTES: '2048' });
  t.after(() => srv.stop());

  const big = 'x'.repeat(8000);
  const res = await request(srv.port, {
    pathName: '/execute',
    chunked: true,
    headers: { 'Content-Type': 'application/json' },
    body: {
      script: 'function filter(email){ return { type:"action", action:"continue" }; }',
      email: { from: 'a@b.c', junk: big },
      timeout_ms: 200,
    },
    timeoutMs: 4000,
  });
  assert.strictEqual(res.status, 413, 'oversized chunked body must be rejected with 413');
  assert.match(res.json.error, /too large|limit|exceed/i);
});

test('a within-limit body is accepted normally', async (t) => {
  const srv = await startServer({ JS_FILTER_MAX_BODY_BYTES: '65536' });
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    pathName: '/execute',
    body: {
      script: 'function filter(email){ return { type:"action", action:"continue" }; }',
      email: { from: 'a@b.c' },
      timeout_ms: 300,
    },
    timeoutMs: 4000,
  });
  assert.strictEqual(res.status, 200);
  assert.strictEqual(res.json.result.action, 'continue');
});

// Fetch response cap: a script that pulls a response larger than the configured
// cap must have the download bounded (fetch rejects), surfacing as a definite
// error rather than exhausting memory.
test('over-limit fetch response is bounded and surfaces as an error', async (t) => {
  // A target the sandbox is allowed to reach, serving more bytes than the cap.
  const target = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('y'.repeat(50000));
  });
  await new Promise((r) => target.listen(0, '127.0.0.1', r));
  const targetPort = target.address().port;
  t.after(() => new Promise((r) => target.close(r)));

  const srv = await startServer({
    JS_FILTER_ALLOWED_HOSTS: '127.0.0.1',
    JS_FILTER_MAX_FETCH_BYTES: '1024',
  });
  t.after(() => srv.stop());

  const script =
    'async function filter(email){' +
    '  const r = await fetch("http://127.0.0.1:' + targetPort + '/");' +
    '  const txt = await r.text();' +
    '  return { type:"action", action:"continue", log:{ detail: "len " + txt.length } };' +
    '}';

  const res = await request(srv.port, {
    pathName: '/execute',
    body: { script, email: { from: 'a@b.c' }, timeout_ms: 1000 },
    timeoutMs: 6000,
  });
  assert.strictEqual(res.status, 500, 'an over-cap fetch must fail, not deliver the full body');
  assert.match(res.json.error, /limit|exceed|too large/i);
});

test('a within-cap fetch response is delivered to the script', async (t) => {
  const target = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('z'.repeat(100));
  });
  await new Promise((r) => target.listen(0, '127.0.0.1', r));
  const targetPort = target.address().port;
  t.after(() => new Promise((r) => target.close(r)));

  const srv = await startServer({
    JS_FILTER_ALLOWED_HOSTS: '127.0.0.1',
    JS_FILTER_MAX_FETCH_BYTES: '1048576',
  });
  t.after(() => srv.stop());

  const script =
    'async function filter(email){' +
    '  const r = await fetch("http://127.0.0.1:' + targetPort + '/");' +
    '  const txt = await r.text();' +
    '  return { type:"action", action:"continue", log:{ detail: "len " + txt.length } };' +
    '}';

  const res = await request(srv.port, {
    pathName: '/execute',
    body: { script, email: { from: 'a@b.c' }, timeout_ms: 1000 },
    timeoutMs: 6000,
  });
  assert.strictEqual(res.status, 200);
  assert.strictEqual(res.json.result.log.detail, 'len 100');
});

// Memory: a script that allocates without end is stopped by the QuickJS memory
// limit (or, failing that, the wall-clock deadline), and the sidecar carries on.
test('a script that exhausts its memory fails, and the sidecar carries on', async (t) => {
  const srv = await startServer({ JS_FILTER_MEMORY_LIMIT_BYTES: String(8 * 1024 * 1024) });
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    body: {
      script: 'function filter(email){ const a = []; for (let i = 0; ; i++) a.push(new Array(1000).fill(i)); }',
      email: { from: 'a@b.c' },
      timeout_ms: 1000,
    },
    timeoutMs: 8000,
  });
  assert.ok(res.status === 500 || res.status === 408, `want a failure, got ${res.status} ${res.body}`);
  if (res.status === 500) assert.match(res.json.error, /out of memory/);

  const after = await request(srv.port, {
    body: { script: 'function filter(email){ return { type:"action", action:"continue" }; }', email: {}, timeout_ms: 500 },
  });
  assert.strictEqual(after.status, 200);
});

// Stack: unbounded recursion is a script error, not a crash of its worker.
test('unbounded recursion fails with a stack overflow', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    body: { script: 'function filter(email){ function f(){ return f() + 1; } return f(); }', email: {}, timeout_ms: 1000 },
  });
  assert.strictEqual(res.status, 500);
  assert.match(res.json.error, /stack overflow/);
});

// CPU: the budget is uncatchable, so a try/catch cannot outlast it.
test('a script cannot catch its own timeout', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    body: {
      script: 'function filter(email){ for (;;) { try { for (;;) {} } catch (e) {} } }',
      email: {},
      timeout_ms: 200,
    },
    timeoutMs: 6000,
  });
  assert.strictEqual(res.status, 408);
});

// Logs live outside the script's memory limit, so they are capped.
test('log output is capped', async (t) => {
  const srv = await startServer();
  t.after(() => srv.stop());

  const res = await request(srv.port, {
    body: {
      script: 'function filter(email){ for (let i = 0; i < 1000; i++) console.log("x".repeat(1000)); return "done"; }',
      email: {},
      timeout_ms: 2000,
    },
  });
  assert.strictEqual(res.status, 200);
  assert.ok(res.json.logs.length < 100, `got ${res.json.logs.length} lines`);
  assert.match(res.json.logs[res.json.logs.length - 1], /dropped/);
});
