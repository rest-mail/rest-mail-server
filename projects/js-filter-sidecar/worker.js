'use strict';

// Worker entry point for a single filter execution or validation.
//
// The isolation boundary (#160) is QuickJS compiled to WebAssembly, not Node's
// `vm` module (which Node documents as not a security boundary: anything handed
// into a vm context brings the host realm with it, so `Object.constructor` was
// the host Function and `Object.constructor("return process")()` reached
// child_process). Here the script runs in a separate JavaScript engine whose
// whole heap is the linear memory of a WebAssembly instance. It has no
// reference to any object of this process: it gets its own intrinsics, no
// require, no process, no module loader, and the WebAssembly module imports no
// filesystem or network calls. The only ways out are the functions installed
// below, and they pass strings only: a log line, and (when the operator
// configured allowed hosts) a fetch request and its response.
//
// Limits: the QuickJS runtime has a memory limit and a stack limit, and an
// interrupt handler stops the script once it has run for its CPU budget (it is
// uncatchable, so a try/catch in the script cannot swallow it). The parent
// still runs this in a dedicated Worker and terminate()s it at a wall-clock
// deadline, which also covers time spent waiting on fetch. A worker runs one
// script and is thrown away, so nothing in QuickJS is freed explicitly.

const http = require('http');
const https = require('https');
const { URL } = require('url');
const { parentPort, workerData } = require('worker_threads');
const { newQuickJSWASMModuleFromVariant, newVariant } = require('quickjs-emscripten-core');
const RELEASE_SYNC = require('@jitl/quickjs-wasmfile-release-sync').default;

// Cap the number of bytes a single fetch response body may accumulate, so a
// script cannot exhaust memory by pulling a huge download into the sandbox.
const MAX_FETCH_BYTES = toPositiveInt(process.env.JS_FILTER_MAX_FETCH_BYTES, 8 * 1024 * 1024);

// Cap what a script may log: the lines live in this process, outside the
// QuickJS memory limit, so an unbounded console.log loop would grow them freely.
const MAX_LOG_BYTES = 64 * 1024;

function toPositiveInt(raw, fallback) {
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
}

// makeFetch creates a fetch-like function restricted to allowed hosts, with a
// bounded response body. It runs on the host side of the boundary; the script
// reaches it only through the string bridge in PRELUDE.
function makeFetch(allowedHostSet) {
  return (url, options = {}) => {
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      return Promise.reject(new Error(`fetch: invalid URL: ${url}`));
    }

    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return Promise.reject(new Error(`fetch blocked: only http and https URLs are allowed`));
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
          resolve({
            status: res.statusCode,
            headers: res.headers,
            body: Buffer.concat(chunks).toString('utf8'),
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

// PRELUDE runs inside QuickJS before the script. It builds the script's API —
// console and, when allowed, fetch — on top of the two host functions, then
// removes those from the global object so the script holds them only through
// these closures. Everything crossing the boundary is a string.
const PRELUDE = `
(() => {
  const log = globalThis.__restmailLog;
  const hostFetch = globalThis.__restmailFetch;
  delete globalThis.__restmailLog;
  delete globalThis.__restmailFetch;
  const line = (...args) => { log(args.map(String).join(' ')); };
  globalThis.console = { log: line, warn: line, error: line };
  if (typeof hostFetch === 'function') {
    globalThis.fetch = async (url, options) => {
      const res = JSON.parse(await hostFetch(String(url), JSON.stringify(options || {})));
      return {
        status: res.status,
        headers: res.headers,
        text: async () => res.body,
        json: async () => JSON.parse(res.body),
      };
    };
  }
})();
`;

// wrap turns the script into one async function body, as the sidecar always
// has: the script may use top-level await, and its filter() may be async. The
// verdict leaves QuickJS as a JSON string.
function wrap(script, call) {
  return `(async () => {
${script}
if (typeof filter !== 'function') {
  throw new Error('script must define a filter(email) function');
}
${call ? 'return JSON.stringify((await filter(JSON.parse(globalThis.__restmailEmail))) ?? null);' : ''}
})`;
}

async function newSandbox({ wasmModule, memoryLimitBytes, stackLimitBytes, cpuBudgetMs, allowedHosts }) {
  const QuickJS = await newQuickJSWASMModuleFromVariant(newVariant(RELEASE_SYNC, { wasmModule }));
  const runtime = QuickJS.newRuntime();
  runtime.setMemoryLimit(memoryLimitBytes);
  // Without an explicit limit, deep recursion overflows the host stack the
  // WebAssembly runs on, killing the worker instead of failing the script.
  runtime.setMaxStackSize(stackLimitBytes);

  // CPU budget: the time the script actually spends executing, summed across
  // its first run and every later job (async continuations, fetch callbacks).
  // Waiting on fetch does not count; the parent's wall-clock deadline covers it.
  const box = { runtime, interrupted: false, used: 0, sliceStart: 0, inflight: 0 };
  runtime.setInterruptHandler(() => {
    if (!box.interrupted && box.used + (Date.now() - box.sliceStart) > cpuBudgetMs) {
      box.interrupted = true;
    }
    return box.interrupted;
  });
  box.run = (fn) => {
    box.sliceStart = Date.now();
    try {
      return fn();
    } finally {
      box.used += Date.now() - box.sliceStart;
    }
  };

  const vm = runtime.newContext();
  box.vm = vm;
  box.logs = [];
  let logBytes = 0;
  vm.newFunction('__restmailLog', (lineHandle) => {
    const line = vm.getString(lineHandle);
    if (logBytes >= MAX_LOG_BYTES) return;
    logBytes += line.length;
    box.logs.push(logBytes > MAX_LOG_BYTES ? '[further log output dropped: limit reached]' : line);
  }).consume((fn) => vm.setProp(vm.global, '__restmailLog', fn));

  if (allowedHosts && allowedHosts.length > 0) {
    const fetch = makeFetch(new Set(allowedHosts));
    vm.newFunction('__restmailFetch', (urlHandle, optionsHandle) => {
      const url = vm.getString(urlHandle);
      let options = {};
      try {
        options = JSON.parse(vm.getString(optionsHandle));
      } catch {
        // An options value JSON cannot express is treated as no options.
      }
      const deferred = vm.newPromise();
      box.inflight++;
      fetch(url, options).then(
        (res) => {
          box.inflight--;
          vm.newString(JSON.stringify(res)).consume((s) => deferred.resolve(s));
        },
        (err) => {
          box.inflight--;
          vm.newError((err && err.message) || String(err)).consume((e) => deferred.reject(e));
        },
      );
      // A settled promise only runs the script's continuation once the
      // pending jobs are drained.
      deferred.settled.then(() => box.drain());
      return deferred.handle;
    }).consume((fn) => vm.setProp(vm.global, '__restmailFetch', fn));
  }

  // drain runs the jobs a settled promise queued, then lets the run check
  // whether the script has finished.
  box.drain = () => {
    box.run(() => runtime.executePendingJobs());
    if (box.check) box.check();
  };
  box.run(() => vm.unwrapResult(vm.evalCode(PRELUDE, 'prelude.js')));
  return box;
}

// errorMessage reads the message of what the script threw. Reading it can run
// script code (a getter), so it runs on the budget, and anything odd gets a
// generic message.
function errorMessage(box, handle) {
  const { vm } = box;
  try {
    return box.run(() => {
      if (vm.typeof(handle) === 'string') return vm.getString(handle);
      if (vm.typeof(handle) === 'object') {
        const message = vm.getProp(handle, 'message');
        if (vm.typeof(message) === 'string') return vm.getString(message);
      }
      return 'script execution failed';
    });
  } catch {
    return 'script execution failed';
  }
}

function failure(box, errorHandle) {
  if (box.interrupted) {
    return { ok: false, timeout: true, error: 'execution timeout', logs: box.logs };
  }
  return { ok: false, timeout: false, error: errorMessage(box, errorHandle), logs: box.logs };
}

// execute runs filter(email) and resolves with the parent message. After each
// drain it looks at the script's promise: settled, it reports the verdict;
// still pending with no fetch in flight, nothing can ever settle it; and once
// the budget is spent every job is interrupted, so the run ends there.
function execute(box, script, email) {
  const { vm } = box;
  vm.newString(JSON.stringify(email)).consume((s) => vm.setProp(vm.global, '__restmailEmail', s));

  const fn = box.run(() => vm.evalCode(wrap(script, true), 'filter.js'));
  if (fn.error) return failure(box, fn.error);
  const call = box.run(() => vm.callFunction(fn.value, vm.undefined));
  if (call.error) return failure(box, call.error);

  return new Promise((resolve) => {
    box.check = () => {
      if (box.interrupted) return resolve(failure(box));
      const state = vm.getPromiseState(call.value);
      if (state.type === 'rejected') return resolve(failure(box, state.error));
      if (state.type === 'fulfilled') {
        // The script can replace its own JSON, so what comes back is checked.
        let result = null;
        try {
          if (vm.typeof(state.value) === 'string') result = JSON.parse(vm.getString(state.value));
        } catch {
          return resolve({ ok: false, timeout: false, error: 'the verdict is not JSON', logs: box.logs });
        }
        return resolve({ ok: true, result, logs: box.logs });
      }
      if (box.inflight === 0) {
        resolve({ ok: false, timeout: false, error: 'filter() never returned: it awaits a promise nothing settles', logs: box.logs });
      }
    };
    box.drain();
  });
}

// validate compiles the script without running it (a syntax check), and when
// an email is given, runs it as a dry run, with no fetch whatever the
// sidecar's allowed hosts. Response bodies are those /validate always gave.
async function validate(box, script, email) {
  const { vm } = box;
  const compiled = box.run(() => vm.evalCode(wrap(script, false), 'filter.js', { compileOnly: true }));
  if (compiled.error) {
    return { ok: true, body: { valid: false, error: errorMessage(box, compiled.error), phase: 'syntax' } };
  }
  if (email == null) {
    return { ok: true, body: { valid: true } };
  }
  const run = await execute(box, script, email);
  if (run.ok) {
    return { ok: true, body: { valid: true, dry_run: { result: run.result, logs: run.logs } } };
  }
  return {
    ok: true,
    body: { valid: true, dry_run: { error: run.error, logs: run.logs }, phase: 'execution' },
  };
}

async function main() {
  const { mode, script, email, allowedHosts } = workerData;
  let box;
  try {
    box = await newSandbox({ ...workerData, allowedHosts: mode === 'validate' ? [] : allowedHosts });
    parentPort.postMessage(mode === 'validate' ? await validate(box, script, email) : await execute(box, script, email));
  } catch (err) {
    parentPort.postMessage({
      ok: false,
      timeout: Boolean(box && box.interrupted),
      error: (box && box.interrupted && 'execution timeout') || (err && err.message) || 'script execution failed',
      logs: (box && box.logs) || [],
    });
  }
}

main();
