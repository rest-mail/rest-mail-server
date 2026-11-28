'use strict';

const assert = require('node:assert/strict');
const { once } = require('node:events');
const http = require('node:http');
const { test } = require('node:test');
const { startServer, request } = require('./helpers');

const traceId = '0123456789abcdef0123456789abcdef';
const parentSpanId = '0123456789abcdef';
const headers = { traceparent: `00-${traceId}-${parentSpanId}-01`, tracestate: 'vendor=value' };
const body = {
  script: 'function filter(email) { return { type: "action", action: "continue" }; }',
  email: { subject: 'private message content' },
};

test('authenticated operations export child spans with the caller trace context', { timeout: 15000 }, async (t) => {
  const exports = [];
  const collector = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    exports.push({ path: req.url, data: Buffer.concat(chunks).toString() });
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{}');
  });
  collector.listen(0, '127.0.0.1');
  await once(collector, 'listening');
  t.after(() => new Promise((resolve) => collector.close(resolve)));

  const srv = await startServer({
    OTEL_EXPORTER_OTLP_ENDPOINT: `http://127.0.0.1:${collector.address().port}/collector`,
    OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: '',
    OTEL_EXPORTER_OTLP_HEADERS: '',
    OTEL_EXPORTER_OTLP_TRACES_HEADERS: '',
    OTEL_EXPORTER_OTLP_COMPRESSION: 'none',
    OTEL_EXPORTER_OTLP_TRACES_COMPRESSION: 'none',
    OTEL_SDK_DISABLED: 'false',
    OTEL_SERVICE_NAME: 'test-js-filter',
  });
  t.after(() => srv.stop());

  assert.equal((await request(srv.port, { headers, body })).status, 200);
  assert.equal((await request(srv.port, { headers, body: '{', pathName: '/validate' })).status, 400);
  assert.equal((await request(srv.port, { headers, body, token: null })).status, 401);

  const exited = once(srv.child, 'exit');
  srv.child.kill('SIGTERM');
  assert.deepEqual(await exited, [0, null], srv.stderr());

  assert.ok(exports.length > 0, 'the collector should receive spans before shutdown');
  const spans = exports.flatMap(({ path, data }) => {
    assert.equal(path, '/collector/v1/traces');
    assert.ok(!data.includes(body.email.subject), 'message content stays out of spans');
    return JSON.parse(data).resourceSpans.flatMap(({ resource, scopeSpans }) => {
      const service = resource.attributes.find(({ key }) => key === 'service.name');
      assert.equal(service?.value.stringValue, 'test-js-filter');
      return scopeSpans.flatMap(({ spans }) => spans);
    });
  });
  assert.deepEqual(spans.map(({ name }) => name).sort(), ['POST /execute', 'POST /validate']);
  for (const span of spans) {
    assert.equal(span.traceId, traceId);
    assert.equal(span.parentSpanId, parentSpanId);
    assert.notEqual(span.spanId, parentSpanId);
    assert.equal(span.traceState, headers.tracestate);
  }
});

test('the sidecar serves requests without an OTLP endpoint', async (t) => {
  const srv = await startServer({
    OTEL_EXPORTER_OTLP_ENDPOINT: '',
    OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: '',
    OTEL_SDK_DISABLED: 'false',
  });
  t.after(() => srv.stop());

  const response = await request(srv.port, { headers, body });
  assert.equal(response.status, 200);
  assert.equal(response.json.result.action, 'continue');
});
