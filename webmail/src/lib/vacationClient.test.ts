import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { runInNewContext } from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../api/client.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
}).outputText;

test('disabling vacation accepts 204 and absent 404 without parsing an empty body', async () => {
  for (const status of [204, 404]) {
    const exports: { disableVacation?: (account: number) => Promise<void> } = {};
    runInNewContext(compiled, {
      exports,
      require: () => ({ API_BASE: '/api/v1' }),
      document: { cookie: 'restmail_csrf=vacation-token' },
      fetch: async (path: string, options: RequestInit) => {
        assert.equal(path, '/api/v1/accounts/7/vacation');
        assert.equal(options.method, 'DELETE');
        assert.equal(options.credentials, 'include');
        assert.equal((options.headers as Record<string, string>)['X-CSRF-Token'], 'vacation-token');
        return new Response(status === 204 ? null : '{"error":{"code":"not_found","message":"Vacation reply not found"}}', { status });
      },
    });
    await exports.disableVacation!(7);
  }
});

test('disabling vacation preserves authorization, server and network failures', async () => {
  for (const status of [401, 403, 500, 0]) {
    const exports: { disableVacation?: (account: number) => Promise<void> } = {};
    runInNewContext(compiled, {
      exports,
      require: () => ({ API_BASE: '/api/v1' }),
      document: { cookie: '' },
      fetch: async () => {
        if (status === 0) throw new Error('offline');
        return new Response('{"error":{"code":"refused","message":"Not allowed"}}', { status });
      },
    });
    await assert.rejects(exports.disableVacation!(7), status === 0 ? /offline/ : /Not allowed/);
  }
});
