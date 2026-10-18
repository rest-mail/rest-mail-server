'use strict';

// #160: a filter script runs in QuickJS compiled to WebAssembly, not in a Node
// `vm` context. Each canonical escape from the old sandbox is tried here and
// must fail: no route reaches the sidecar's process, its filesystem, or a
// child process, and nothing the script does to its own intrinsics reaches an
// object of the sidecar.

const { test, before, after } = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { startServer, request } = require('./helpers');

let srv;
let target;
let seen;

before(async () => {
  // A host the scripts may fetch from, recording what reaches it.
  target = http.createServer((req, res) => {
    seen.push({ method: req.method, headers: req.headers });
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok');
  });
  await new Promise((r) => target.listen(0, '127.0.0.1', r));
  srv = await startServer({ JS_FILTER_ALLOWED_HOSTS: '127.0.0.1' });
});

after(async () => {
  await srv.stop();
  await new Promise((r) => target.close(r));
});

function run(script, timeoutMs = 1000) {
  return request(srv.port, {
    body: { script, email: { from: 'a@b.c' }, timeout_ms: timeoutMs },
    timeoutMs: 8000,
  });
}

// marker returns a path that must not exist after a script tried to create it.
function marker(t) {
  const p = path.join(os.tmpdir(), `js-filter-escape-${process.pid}-${Date.now()}-${Math.random()}`);
  t.after(() => fs.rmSync(p, { force: true }));
  return p;
}

test('Object.constructor("return process")() does not reach the process', async (t) => {
  const file = marker(t);
  const escape = await run(`
    function filter(email) {
      const proc = Object.constructor("return process")();
      proc.getBuiltinModule("child_process").execSync("touch ${file}");
      return { type: "action", action: "continue" };
    }`);
  assert.strictEqual(escape.status, 500, 'the escape must fail');
  assert.ok(!fs.existsSync(file), 'the escape must not have run a command');

  const probe = await run(`
    function filter(email) {
      return Object.constructor("return [typeof process, typeof require, typeof module]")();
    }`);
  assert.strictEqual(probe.status, 200);
  assert.deepStrictEqual(probe.json.result, ['undefined', 'undefined', 'undefined']);
});

test('this.constructor.constructor does not reach the process', async (t) => {
  const file = marker(t);
  const escape = await run(`
    const proc = this.constructor.constructor("return process")();
    function filter(email) {
      proc.getBuiltinModule("fs").writeFileSync("${file}", "escaped");
      return { type: "action", action: "continue" };
    }`);
  assert.strictEqual(escape.status, 500, 'the escape must fail');
  assert.ok(!fs.existsSync(file), 'the escape must not have written a file');

  const probe = await run(`
    function filter(email) {
      const g = this.constructor.constructor("return this")();
      return [typeof g.process, typeof g.require, typeof g.Buffer];
    }`);
  assert.strictEqual(probe.status, 200);
  assert.deepStrictEqual(probe.json.result, ['undefined', 'undefined', 'undefined']);
});

test('prototype pollution does not reach the sidecar\'s objects', async () => {
  // fetch is carried out by the sidecar. Were the script's Object.prototype
  // the sidecar's, the options object it builds would inherit these, and the
  // request would go out as a DELETE carrying the header.
  seen = [];
  const polluted = await run(`
    async function filter(email) {
      Object.prototype.method = "DELETE";
      Object.prototype.headers = { "x-polluted": "yes" };
      Array.prototype.toJSON = function () { return "polluted"; };
      ({}).__proto__.polluted = true;
      await fetch("http://127.0.0.1:${target.address().port}/");
      console.log("fetched");
      return "done";
    }`);
  assert.strictEqual(polluted.status, 200, polluted.body);
  assert.strictEqual(seen.length, 1);
  assert.strictEqual(seen[0].method, 'GET', 'the sidecar\'s request must not inherit the script\'s prototype');
  assert.strictEqual(seen[0].headers['x-polluted'], undefined);
  // The response is built by the sidecar: its logs are still an array.
  assert.deepStrictEqual(polluted.json.logs, ['fetched']);
  assert.strictEqual(polluted.json.result, 'done');

  // Nor does it reach the next script.
  const next = await run(`
    function filter(email) {
      return [({}).polluted, ({}).method, typeof [].toJSON];
    }`);
  assert.strictEqual(next.status, 200);
  assert.deepStrictEqual(next.json.result, [null, null, 'undefined']);
});

test('import() loads nothing', async () => {
  for (const specifier of ['fs', 'node:child_process', 'std', 'os', './server.js']) {
    const res = await run(`
      async function filter(email) {
        const mod = await import(${JSON.stringify(specifier)});
        return Object.keys(mod);
      }`);
    assert.strictEqual(res.status, 500, `import(${specifier}) must fail`);
    assert.match(res.json.error, /could not load module/);
  }
});

test('the filesystem cannot be read', async () => {
  const secret = fs.readFileSync(__filename, 'utf8').slice(0, 40);
  const attempts = [
    // Through the old vm escape.
    `Object.constructor("return process")().getBuiltinModule("fs").readFileSync(${JSON.stringify(__filename)}, "utf8")`,
    // Node's module system and process are not there.
    `require("fs").readFileSync(${JSON.stringify(__filename)}, "utf8")`,
    `process.binding("fs")`,
    // Nor QuickJS's own std/os modules, which can open files.
    `std.loadFile(${JSON.stringify(__filename)})`,
    `os.open(${JSON.stringify(__filename)}, 0)`,
    // fetch speaks only http and https.
    `await (await fetch("file://${__filename}")).text()`,
  ];
  for (const attempt of attempts) {
    const res = await run(`async function filter(email) { return ${attempt}; }`);
    assert.strictEqual(res.status, 500, `${attempt} must fail`);
    assert.ok(!res.body.includes(secret), `${attempt} must not return the file`);
  }
});

test('no process can be spawned', async (t) => {
  const file = marker(t);
  const attempts = [
    `Object.constructor("return process")().getBuiltinModule("child_process").execSync("touch ${file}")`,
    `require("child_process").execSync("touch ${file}")`,
    `process.mainModule.require("child_process").execSync("touch ${file}")`,
    `(await import("child_process")).execSync("touch ${file}")`,
    `os.exec(["touch", "${file}"])`,
    `new Worker("data:,")`,
  ];
  for (const attempt of attempts) {
    const res = await run(`async function filter(email) { return ${attempt}; }`);
    assert.strictEqual(res.status, 500, `${attempt} must fail`);
    assert.ok(!fs.existsSync(file), `${attempt} must not have run a command`);
  }
});

test('fetch is not offered when no hosts are allowed', async (t) => {
  const plain = await startServer();
  t.after(() => plain.stop());
  const res = await request(plain.port, {
    body: { script: 'function filter(email) { return typeof fetch; }', email: {}, timeout_ms: 500 },
  });
  assert.strictEqual(res.status, 200);
  assert.strictEqual(res.json.result, 'undefined');
});
