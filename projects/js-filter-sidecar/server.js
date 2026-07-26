'use strict';

const http = require('http');
const vm = require('vm');
const path = require('path');
const { Worker } = require('worker_threads');

const PORT = process.env.PORT || 3100;
const WORKER_PATH = path.join(__dirname, 'worker.js');

function toPositiveInt(raw, fallback) {
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
}

// Cap the request body size so an oversized (or unbounded) payload is rejected
// with a definite error instead of being buffered without limit. The default is
// generous enough to hold the largest message the pipeline forwards (its JSON
// encoding, plus the script) and is overridable for tight deployments.
const MAX_BODY_BYTES = toPositiveInt(process.env.JS_FILTER_MAX_BODY_BYTES, 32 * 1024 * 1024);

// Default allowed hosts from environment (comma-separated). The allowlist is
// server-configured; request-supplied hosts are honoured too for backward
// compatibility with the existing /execute contract.
const DEFAULT_ALLOWED_HOSTS = (process.env.JS_FILTER_ALLOWED_HOSTS || '')
  .split(',')
  .map((h) => h.trim())
  .filter(Boolean);

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
// deadline. When the deadline is hit the worker is terminated (killing async
// busy-loops the vm timeout cannot), and the request gets a definite 408 timeout
// — never a silent success. Exactly one response is sent per request.
function runInWorker(workerData, wallTimeoutMs, res) {
  let settled = false;
  let worker;

  const finish = (fn) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    fn();
    if (worker) worker.terminate();
  };

  const timer = setTimeout(() => {
    finish(() => sendJSON(res, 408, { error: 'execution timeout' }));
  }, wallTimeoutMs);

  try {
    worker = new Worker(WORKER_PATH, { workerData });
  } catch (err) {
    finish(() => sendJSON(res, 500, { error: `failed to start worker: ${err && err.message}` }));
    return;
  }

  worker.once('message', (msg) => {
    finish(() => {
      if (msg && msg.ok) {
        sendJSON(res, 200, { result: msg.result, logs: msg.logs || [] });
      } else if (msg && msg.timeout) {
        // Uniform 408 body for both the synchronous (vm) and wall-clock
        // (terminate) timeout paths; the Go filter keys on the 408 status.
        sendJSON(res, 408, { error: 'execution timeout' });
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

  if (req.method === 'POST' && req.url === '/validate') {
    return handleValidate(req, res);
  }

  if (req.method !== 'POST' || req.url !== '/execute') {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not found' }));
    return;
  }

  return handleExecute(req, res);
});

// Execute handler: runs a filter script against an email in a dedicated Worker,
// with optional async fetch support. Request/response contract is unchanged:
//   request : { script, email, timeout_ms?, allowed_hosts? }
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

    const { script, email, timeout_ms, allowed_hosts } = parsed;
    if (!script || !email) {
      sendJSON(res, 400, { error: 'script and email are required' });
      return;
    }

    // Synchronous vm guard (inner) and wall-clock terminate deadline (outer). The
    // outer bound gives async work (e.g. fetch) headroom while still being hard.
    const syncTimeout = toPositiveInt(timeout_ms, 500);
    const wallTimeout = Math.max(syncTimeout * 3, 2000);

    const allowedHosts = new Set([...DEFAULT_ALLOWED_HOSTS, ...(Array.isArray(allowed_hosts) ? allowed_hosts : [])]);

    runInWorker(
      { script, email, syncTimeout, allowedHosts: [...allowedHosts] },
      wallTimeout,
      res,
    );
  });
}

// Validate endpoint: syntax check + optional dry-run. The dry-run calls filter()
// synchronously (its result is not awaited), so it cannot be hung by an async
// loop; the vm synchronous timeout bounds a runaway sync script. Body size is
// capped here too.
function handleValidate(req, res) {
  readBody(req, res, MAX_BODY_BYTES, (body) => {
    try {
      const { script, email } = JSON.parse(body);

      if (!script) {
        sendJSON(res, 400, { valid: false, error: 'script is required' });
        return;
      }

      // Step 1: Syntax check — try to compile the script
      const wrappedScript = `
        ${script}
        if (typeof filter !== 'function') {
          throw new Error('script must define a filter(email) function');
        }
      `;

      try {
        new vm.Script(wrappedScript);
      } catch (syntaxErr) {
        sendJSON(res, 200, { valid: false, error: syntaxErr.message, phase: 'syntax' });
        return;
      }

      // Step 2: If email is provided, do a dry-run
      if (email) {
        const logs = [];
        const sandbox = {
          JSON, Math, Date, String, Array, Object, RegExp,
          parseInt, parseFloat, isNaN, isFinite,
          encodeURIComponent, decodeURIComponent,
          console: {
            log: (...args) => { logs.push(args.map(String).join(' ')); },
            warn: (...args) => { logs.push(args.map(String).join(' ')); },
            error: (...args) => { logs.push(args.map(String).join(' ')); },
          },
        };

        const context = vm.createContext(sandbox);
        const dryRunScript = `
          ${script}
          if (typeof filter !== 'function') {
            throw new Error('script must define a filter(email) function');
          }
          filter(${JSON.stringify(email)});
        `;

        try {
          const result = vm.runInContext(dryRunScript, context, { timeout: 500 });
          sendJSON(res, 200, { valid: true, dry_run: { result, logs } });
        } catch (runErr) {
          sendJSON(res, 200, {
            valid: true,
            dry_run: { error: runErr.message, logs },
            phase: 'execution',
          });
        }
        return;
      }

      // Syntax-only validation passed
      sendJSON(res, 200, { valid: true });
    } catch (err) {
      sendJSON(res, 500, { valid: false, error: err.message });
    }
  });
}

server.listen(PORT, () => {
  const addr = server.address();
  const boundPort = addr && typeof addr === 'object' ? addr.port : PORT;
  console.log(`js-filter-sidecar listening on port ${boundPort}`);
});
