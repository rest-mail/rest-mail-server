'use strict';

// Worker entry point for a single filter execution.
//
// Each /execute request runs its user script here, in a dedicated
// worker_threads Worker, NOT on the main HTTP thread. This is the core of the
// #160 / #201 hardening: vm.runInContext({timeout}) bounds only SYNCHRONOUS
// script execution, so an async busy-loop (e.g. `while (true) { await 0; }`) or
// an unresolved/looping promise escapes it and would peg a single-threaded
// server, hanging every other tenant's /execute and /health. By running in a
// Worker the main thread stays responsive, and the parent enforces a hard
// wall-clock deadline with worker.terminate() — which kills the thread outright,
// async loops included.
//
// The vm sandbox is still used inside the worker to keep the script off the
// worker's own globals (require/process/etc.); the Worker adds killability on
// top of that. Node's `vm` is not a security boundary on its own — see #160.

const vm = require('vm');
const http = require('http');
const https = require('https');
const { URL } = require('url');
const { parentPort, workerData } = require('worker_threads');

// Cap the number of bytes a single fetch response body may accumulate, so a
// script cannot exhaust memory by pulling a huge download into the sandbox.
const MAX_FETCH_BYTES = toPositiveInt(process.env.JS_FILTER_MAX_FETCH_BYTES, 8 * 1024 * 1024);

function toPositiveInt(raw, fallback) {
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
}

// makeFetch creates a fetch-like function restricted to allowed hosts, with a
// bounded response body.
function makeFetch(allowedHostSet) {
  return (url, options = {}) => {
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      return Promise.reject(new Error(`fetch: invalid URL: ${url}`));
    }

    if (!allowedHostSet.has(parsed.hostname)) {
      return Promise.reject(
        new Error(`fetch blocked: ${parsed.hostname} is not in the allowed hosts list`),
      );
    }

    return new Promise((resolve, reject) => {
      const mod = parsed.protocol === 'https:' ? https : http;
      const reqOpts = {
        hostname: parsed.hostname,
        port: parsed.port || (parsed.protocol === 'https:' ? 443 : 80),
        path: parsed.pathname + parsed.search,
        method: (options.method || 'GET').toUpperCase(),
        headers: options.headers || {},
        timeout: 5000,
      };
      const req = mod.request(reqOpts, (res) => {
        const chunks = [];
        let size = 0;
        let done = false;
        res.on('data', (chunk) => {
          if (done) return;
          size += chunk.length;
          if (size > MAX_FETCH_BYTES) {
            done = true;
            res.destroy();
            req.destroy();
            reject(new Error(`fetch response exceeds limit of ${MAX_FETCH_BYTES} bytes`));
            return;
          }
          chunks.push(chunk);
        });
        res.on('end', () => {
          if (done) return;
          done = true;
          const data = Buffer.concat(chunks).toString('utf8');
          resolve({
            status: res.statusCode,
            headers: res.headers,
            text: () => Promise.resolve(data),
            json: () => Promise.resolve(JSON.parse(data)),
          });
        });
        res.on('error', (err) => {
          if (done) return;
          done = true;
          reject(err);
        });
      });
      req.on('error', reject);
      req.on('timeout', () => {
        req.destroy();
        reject(new Error('fetch timeout'));
      });
      if (options.body) {
        req.write(typeof options.body === 'string' ? options.body : JSON.stringify(options.body));
      }
      req.end();
    });
  };
}

// buildSandbox creates a restricted VM sandbox with optional fetch support.
function buildSandbox(logs, allowedHosts) {
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

  if (allowedHosts && allowedHosts.size > 0) {
    sandbox.fetch = makeFetch(allowedHosts);
  }

  return sandbox;
}

// post sends one message to the parent, converting a non-cloneable result into a
// clean error so the parent never sees a raw structured-clone throw.
function post(msg) {
  try {
    parentPort.postMessage(msg);
  } catch (err) {
    parentPort.postMessage({
      ok: false,
      timeout: false,
      error: `script result is not serializable: ${(err && err.message) || err}`,
      logs: (msg && msg.logs) || [],
    });
  }
}

async function run() {
  const { script, email, syncTimeout, allowedHosts } = workerData;
  const logs = [];
  try {
    const sandbox = buildSandbox(logs, new Set(allowedHosts || []));
    const context = vm.createContext(sandbox);

    // Wrap the script in an async IIFE so filter() may use await fetch(). The vm
    // timeout still guards synchronous runaway; the parent's wall-clock
    // terminate() guards everything else (async loops, hung promises).
    const wrappedScript = `
      (async () => {
        ${script}
        if (typeof filter !== 'function') {
          throw new Error('script must define a filter(email) function');
        }
        return await filter(${JSON.stringify(email)});
      })();
    `;

    const resultOrPromise = vm.runInContext(wrappedScript, context, { timeout: syncTimeout });
    const result = await Promise.resolve(resultOrPromise);
    post({ ok: true, result, logs });
  } catch (err) {
    const isTimeout = err && err.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT';
    post({
      ok: false,
      timeout: Boolean(isTimeout),
      error: (err && err.message) || 'script execution failed',
      logs,
    });
  }
}

run();
