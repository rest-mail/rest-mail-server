const http = require('http');
const vm = require('vm');

const PORT = process.env.PORT || 3100;

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

  let body = '';
  req.on('data', chunk => { body += chunk; });
  req.on('end', () => {
    try {
      const { script, email, timeout_ms } = JSON.parse(body);

      if (!script || !email) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'script and email are required' }));
        return;
      }

      const timeout = timeout_ms || 500;

      // Build a restricted sandbox — no require, process, fs, net, etc.
      const logs = [];
      const sandbox = {
        JSON,
        Math,
        Date,
        String,
        Array,
        Object,
        RegExp,
        parseInt,
        parseFloat,
        isNaN,
        isFinite,
        encodeURIComponent,
        decodeURIComponent,
        console: {
          log: (...args) => { logs.push(args.map(String).join(' ')); },
          warn: (...args) => { logs.push(args.map(String).join(' ')); },
          error: (...args) => { logs.push(args.map(String).join(' ')); },
        },
      };

      const context = vm.createContext(sandbox);

      const wrappedScript = `
        ${script}
        if (typeof filter !== 'function') {
          throw new Error('script must define a filter(email) function');
        }
        filter(${JSON.stringify(email)});
      `;

      const result = vm.runInContext(wrappedScript, context, { timeout });

      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ result, logs }));
    } catch (err) {
      if (err.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT') {
        res.writeHead(408, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'execution timeout' }));
        return;
      }
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: err.message || 'script execution failed' }));
    }
  });
});

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
