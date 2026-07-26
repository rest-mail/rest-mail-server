'use strict';

// Test helpers: spawn the sidecar as a child process (so per-test env vars such
// as body/fetch caps and the allowed-hosts list take effect at import time),
// wait for it to report its listening port, and make HTTP requests against it.

const { spawn } = require('child_process');
const http = require('http');
const path = require('path');

const SERVER = path.join(__dirname, '..', 'server.js');

// startServer launches server.js on an OS-assigned port (PORT=0) with the given
// extra environment, resolving with the actual bound port once the server logs
// it. Letting the OS pick and reading back the real port avoids the TOCTOU race
// of pre-grabbing a port, which matters when test files run in parallel.
function startServer(env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [SERVER], {
      env: { ...process.env, PORT: '0', ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    let out = '';
    let stderr = '';
    const onData = (d) => {
      out += d.toString();
      const m = out.match(/listening on port (\d+)/);
      if (m) {
        child.stdout.off('data', onData);
        resolve({
          port: Number(m[1]),
          child,
          stderr: () => stderr,
          stop: () =>
            new Promise((res) => {
              if (child.exitCode !== null || child.signalCode !== null) return res();
              child.once('exit', () => res());
              child.kill('SIGKILL');
            }),
        });
      }
    };
    child.stdout.on('data', onData);
    child.stderr.on('data', (d) => { stderr += d.toString(); });
    child.once('error', reject);
    setTimeout(
      () => reject(new Error('server did not start in time; output:\n' + out + '\nstderr:\n' + stderr)),
      8000,
    );
  });
}

// request makes an HTTP request and resolves with { status, body, json }.
// A client-side timeout rejects with an Error tagged code 'CLIENT_TIMEOUT' so a
// hung server is observable rather than hanging the test forever.
function request(port, opts = {}) {
  const { method = 'POST', pathName = '/execute', body, timeoutMs = 5000, headers = {} } = opts;
  return new Promise((resolve, reject) => {
    const data = body == null ? null : typeof body === 'string' ? body : JSON.stringify(body);
    const reqHeaders = { ...headers };
    if (data != null && reqHeaders['Content-Type'] === undefined && !opts.chunked) {
      reqHeaders['Content-Type'] = 'application/json';
    }
    if (data != null && !opts.chunked) {
      reqHeaders['Content-Length'] = Buffer.byteLength(data);
    }

    const req = http.request(
      { host: '127.0.0.1', port, method, path: pathName, headers: reqHeaders },
      (res) => {
        let buf = '';
        res.on('data', (c) => { buf += c; });
        res.on('end', () => {
          let json = null;
          try { json = JSON.parse(buf); } catch { /* leave null */ }
          resolve({ status: res.statusCode, body: buf, json });
        });
      },
    );
    req.setTimeout(timeoutMs, () => {
      const e = new Error('client timeout after ' + timeoutMs + 'ms');
      e.code = 'CLIENT_TIMEOUT';
      req.destroy(e);
    });
    req.once('error', reject);

    if (data != null) {
      if (opts.chunked) {
        // Force chunked transfer-encoding (no Content-Length) to exercise the
        // streaming byte-counter rather than the Content-Length fast path.
        const half = Math.ceil(data.length / 2);
        req.write(data.slice(0, half));
        req.write(data.slice(half));
      } else {
        req.write(data);
      }
    }
    req.end();
  });
}

module.exports = { startServer, request };
