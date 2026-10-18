'use strict';

const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const path = require('path');
const { Worker } = require('worker_threads');

const PORT = process.env.PORT || 3100;
const WORKER_PATH = path.join(__dirname, 'worker.js');

// The API authenticates to the sidecar with a shared token (Authorization:
// Bearer). Without one the sidecar would run any script anyone on the network
// sent it, so it refuses to start.
const TOKEN = process.env.JS_FILTER_TOKEN || '';
if (TOKEN.length < 16) {
  console.error('JS_FILTER_TOKEN must be set, at least 16 characters: the API sends it to authenticate');
  process.exit(1);
}
const TOKEN_DIGEST = sha256(TOKEN);

function toPositiveInt(raw, fallback) {
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
}

// Cap the request body size so an oversized (or unbounded) payload is rejected
// with a definite error instead of being buffered without limit. The default is
// generous enough to hold the largest message the pipeline forwards (its JSON
// encoding, plus the script) and is overridable for tight deployments.
const MAX_BODY_BYTES = toPositiveInt(process.env.JS_FILTER_MAX_BODY_BYTES, 32 * 1024 * 1024);

// The hosts a script may fetch from (comma-separated). The allowlist is set by
// whoever runs the sidecar; a request cannot add to it.
const DEFAULT_ALLOWED_HOSTS = (process.env.JS_FILTER_ALLOWED_HOSTS || '')
  .split(',')
  .map((h) => h.trim())
  .filter(Boolean);

// What one run of a script may use inside QuickJS: its heap (the email it is
// given included), and its stack. The CPU budget is the request's timeout_ms.
const MEMORY_LIMIT_BYTES = toPositiveInt(process.env.JS_FILTER_MEMORY_LIMIT_BYTES, 64 * 1024 * 1024);
const STACK_LIMIT_BYTES = 256 * 1024;

// QuickJS is compiled once; each worker instantiates its own copy, so no two
// scripts ever share an engine.
const WASM_MODULE = new WebAssembly.Module(
  fs.readFileSync(require.resolve('@jitl/quickjs-wasmfile-release-sync/wasm')),
);

function sha256(s) {
  return crypto.createHash('sha256').update(s).digest();
}

// authorized checks the caller's bearer token in constant time.
function authorized(req) {
  const m = /^Bearer (.+)$/.exec(req.headers.authorization || '');
  return Boolean(m) && crypto.timingSafeEqual(sha256(m[1]), TOKEN_DIGEST);
}

function sendJSON(res, status, obj) {
  if (res.headersSent || res.writableEnded) return;
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(obj));
}

// readBody buffers the request body up to maxBytes. It rejects oversized bodies
// with HTTP 413 — both via the Content-Length fast path and by counting bytes on
// the wire (for chunked/streamed requests) — and never accumulates past the cap.
function readBody(req, res, maxBytes, onBody) {
  const declared = Number(req.headers['content-length']);
  if (Number.isFinite(declared) && declared > maxBytes) {
    sendTooLarge(res, maxBytes);
    return;
  }

  const chunks = [];
  let size = 0;
  let aborted = false;

  req.on('data', (chunk) => {
    if (aborted) return;
    size += chunk.length;
    if (size > maxBytes) {
      aborted = true;
      sendTooLarge(res, maxBytes);
      return;
    }
    chunks.push(chunk);
  });
  req.on('end', () => {
    if (aborted) return;
    onBody(Buffer.concat(chunks).toString('utf8'));
  });
  req.on('error', () => {
    if (aborted) return;
    aborted = true;
    sendJSON(res, 400, { error: 'error reading request body' });
  });
}

function sendTooLarge(res, maxBytes) {
  if (res.headersSent || res.writableEnded) return;
  // Connection: close lets Node tear down the socket after the response flushes,
  // aborting the rest of an oversized upload instead of draining it.
  res.writeHead(413, { 'Content-Type': 'application/json', Connection: 'close' });
  res.end(JSON.stringify({ error: `request body too large (limit ${maxBytes} bytes)` }));
}

// runInWorker executes one script in a dedicated Worker with a hard wall-clock
// deadline. When the deadline is hit the worker is terminated (whatever the
// script is doing, waiting on fetch included), and the request gets a definite
// 408 timeout — never a silent success. Exactly one response is sent per
// request.
function runInWorker(job, wallTimeoutMs, res) {
  let settled = false;
  let worker;

  const finish = (fn) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    fn();
    if (worker) worker.terminate();
  };

  // /validate answers 200 with the outcome of its dry run, a timeout included.
  const timeout = () =>
    job.mode === 'validate'
      ? sendJSON(res, 200, { valid: true, dry_run: { error: 'execution timeout', logs: [] }, phase: 'execution' })
      : sendJSON(res, 408, { error: 'execution timeout' });

  const timer = setTimeout(() => finish(timeout), wallTimeoutMs);

  try {
    worker = new Worker(WORKER_PATH, {
      workerData: {
        ...job,
        wasmModule: WASM_MODULE,
        memoryLimitBytes: MEMORY_LIMIT_BYTES,
        stackLimitBytes: STACK_LIMIT_BYTES,
      },
    });
  } catch (err) {
    finish(() => sendJSON(res, 500, { error: `failed to start worker: ${err && err.message}` }));
    return;
  }

  worker.once('message', (msg) => {
    finish(() => {
      if (msg && msg.ok && msg.body) {
        sendJSON(res, 200, msg.body);
      } else if (msg && msg.ok) {
        sendJSON(res, 200, { result: msg.result, logs: msg.logs || [] });
      } else if (msg && msg.timeout) {
        // Uniform 408 body for both the CPU budget (QuickJS interrupt) and
        // wall-clock (terminate) timeout paths; the Go filter keys on the 408
        // status.
        timeout();
      } else {
        sendJSON(res, 500, { error: (msg && msg.error) || 'script execution failed' });
      }
    });
  });

  worker.once('error', (err) => {
    finish(() => sendJSON(res, 500, { error: (err && err.message) || 'worker error' }));
  });

  worker.once('exit', (code) => {
    // Only meaningful if the worker died before posting a message and outside our
    // own terminate() path (which sets settled first).
    finish(() => sendJSON(res, 500, { error: `worker exited unexpectedly (code ${code})` }));
  });
}

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ status: 'ok' }));
    return;
  }

  if (req.method !== 'POST' || (req.url !== '/execute' && req.url !== '/validate')) {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not found' }));
    return;
  }

  if (!authorized(req)) {
    res.writeHead(401, { 'Content-Type': 'application/json', 'WWW-Authenticate': 'Bearer' });
    res.end(JSON.stringify({ error: 'unauthorized' }));
    return;
  }

  if (req.url === '/validate') {
    return handleValidate(req, res);
  }
  return handleExecute(req, res);
});

// Execute handler: runs a filter script against an email in a dedicated Worker,
// with optional async fetch support. Request/response contract (every request
// carries Authorization: Bearer <JS_FILTER_TOKEN>):
//   request : { script, email, timeout_ms? }
//   success : 200 { result, logs }
//   timeout : 408 { error }
//   failure : 5xx/413/400 { error }
function handleExecute(req, res) {
  readBody(req, res, MAX_BODY_BYTES, (body) => {
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON body' });
      return;
    }

    const { script, email, timeout_ms } = parsed;
    if (!script || !email) {
      sendJSON(res, 400, { error: 'script and email are required' });
      return;
    }

    // CPU budget inside QuickJS (inner) and wall-clock terminate deadline
    // (outer). The outer bound gives async work (e.g. fetch) headroom while
    // still being hard.
    const cpuBudget = toPositiveInt(timeout_ms, 500);
    const wallTimeout = Math.max(cpuBudget * 3, 2000);

    runInWorker(
      { mode: 'execute', script, email, cpuBudgetMs: cpuBudget, allowedHosts: DEFAULT_ALLOWED_HOSTS },
      wallTimeout,
      res,
    );
  });
}

// Validate endpoint: syntax check + optional dry-run, in the same isolated
// Worker as /execute. The dry run gets 500ms of CPU and no fetch.
//   request : { script, email? }
//   success : 200 { valid: true } | 200 { valid: false, error, phase: 'syntax' }
//             | 200 { valid: true, dry_run: { result, logs } }
//             | 200 { valid: true, dry_run: { error, logs }, phase: 'execution' }
function handleValidate(req, res) {
  readBody(req, res, MAX_BODY_BYTES, (body) => {
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch {
      sendJSON(res, 400, { valid: false, error: 'invalid JSON body' });
      return;
    }

    const { script, email } = parsed;
    if (!script) {
      sendJSON(res, 400, { valid: false, error: 'script is required' });
      return;
    }

    runInWorker({ mode: 'validate', script, email, cpuBudgetMs: 500, allowedHosts: [] }, 2000, res);
  });
}

server.listen(PORT, () => {
  const addr = server.address();
  const boundPort = addr && typeof addr === 'object' ? addr.port : PORT;
  console.log(`js-filter-sidecar listening on port ${boundPort}`);
});
