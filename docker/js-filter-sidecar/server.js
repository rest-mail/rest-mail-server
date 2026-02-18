const http = require('http');
const vm = require('vm');

const PORT = process.env.PORT || 3100;

const server = http.createServer((req, res) => {
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

server.listen(PORT, () => {
  console.log(`js-filter-sidecar listening on port ${PORT}`);
});
