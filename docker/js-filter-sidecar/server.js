const http = require('http');
const https = require('https');
const vm = require('vm');
const { URL } = require('url');

const PORT = process.env.PORT || 3100;

// Default allowed hosts from environment (comma-separated)
const DEFAULT_ALLOWED_HOSTS = (process.env.JS_FILTER_ALLOWED_HOSTS || '')
  .split(',')
  .map(h => h.trim())
  .filter(Boolean);

// makeFetch creates a fetch-like function restricted to allowed hosts.
function makeFetch(allowedHostSet) {
  return (url, options = {}) => {
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      return Promise.reject(new Error(`fetch: invalid URL: ${url}`));
    }

    if (!allowedHostSet.has(parsed.hostname)) {
      return Promise.reject(new Error(`fetch blocked: ${parsed.hostname} is not in the allowed hosts list`));
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
        let data = '';
        res.on('data', chunk => { data += chunk; });
        res.on('end', () => {
          resolve({
            status: res.statusCode,
            headers: res.headers,
            text: () => Promise.resolve(data),
            json: () => Promise.resolve(JSON.parse(data)),
          });
        });
      });
      req.on('error', reject);
      req.on('timeout', () => { req.destroy(); reject(new Error('fetch timeout')); });
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

  // Merge default + request-level allowed hosts
  const allHosts = new Set([...DEFAULT_ALLOWED_HOSTS, ...(allowedHosts || [])]);
  if (allHosts.size > 0) {
    sandbox.fetch = makeFetch(allHosts);
  }

  return sandbox;
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

// Execute handler: runs a filter script against an email, with optional async fetch support.
function handleExecute(req, res) {
  let body = '';
  req.on('data', chunk => { body += chunk; });
  req.on('end', async () => {
    try {
      const { script, email, timeout_ms, allowed_hosts } = JSON.parse(body);

      if (!script || !email) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'script and email are required' }));
        return;
      }

      const timeout = timeout_ms || 500;
      const logs = [];
      const sandbox = buildSandbox(logs, allowed_hosts);
      const context = vm.createContext(sandbox);

      // Wrap script in an async IIFE so filter() can use await fetch()
      const wrappedScript = `
        (async () => {
          ${script}
          if (typeof filter !== 'function') {
            throw new Error('script must define a filter(email) function');
          }
          return await filter(${JSON.stringify(email)});
        })();
      `;

      const resultOrPromise = vm.runInContext(wrappedScript, context, { timeout });

      // Await the result with an overall timeout (covers async operations like fetch)
      const overallTimeout = Math.max(timeout * 3, 2000);
      const result = await Promise.race([
        Promise.resolve(resultOrPromise),
        new Promise((_, reject) =>
          setTimeout(() => reject(new Error('execution timeout')), overallTimeout)
        ),
      ]);

      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ result, logs }));
    } catch (err) {
      if (err.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT' || err.message === 'execution timeout') {
        res.writeHead(408, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'execution timeout' }));
        return;
      }
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: err.message || 'script execution failed' }));
    }
  });
}

// Validate endpoint: syntax check + optional dry-run
function handleValidate(req, res) {
  let body = '';
  req.on('data', chunk => { body += chunk; });
  req.on('end', () => {
    try {
      const { script, email } = JSON.parse(body);

      if (!script) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ valid: false, error: 'script is required' }));
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
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          valid: false,
          error: syntaxErr.message,
          phase: 'syntax',
        }));
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
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({
            valid: true,
            dry_run: { result, logs },
          }));
        } catch (runErr) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({
            valid: true,
            dry_run: {
              error: runErr.message,
              logs,
            },
            phase: 'execution',
          }));
        }
        return;
      }

      // Syntax-only validation passed
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ valid: true }));
    } catch (err) {
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ valid: false, error: err.message }));
    }
  });
}

server.listen(PORT, () => {
  console.log(`js-filter-sidecar listening on port ${PORT}`);
});
