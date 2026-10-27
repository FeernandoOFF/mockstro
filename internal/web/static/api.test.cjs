const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

// Exercise the API code shipped to the browser without a browser dependency.
function client(fetch) {
  const html = readFileSync(`${__dirname}/index.html`, 'utf8');
  const start = html.indexOf('let workspaceRequests =');
  const end = html.indexOf('const fmtTime =', start);
  const context = vm.createContext({ fetch, TOKEN: 'test', seenInstance: '',
    reloadOnce() {}, setTimeout: (fn) => setTimeout(fn, 0) });
  vm.runInContext(html.slice(start, end), context);
  return (path, body) => context.api(path, body);
}
const response = (status = 200, data = {}) => ({ ok: status < 400, status,
  headers: { get: () => null }, json: async () => data });

test('workspace actions wait for Git while session polling stays live', async () => {
  let finish;
  const calls = [];
  const api = client(async (path) => {
    calls.push(path);
    if (path === '/api/git/fetch') await new Promise((resolve) => { finish = resolve; });
    return response();
  });
  const git = api('/api/git/fetch', {});
  const serve = api('/api/serve', { ref: 'flow' });
  await api('/api/state');
  assert.deepEqual(calls, ['/api/state', '/api/git/fetch']);
  finish();
  await Promise.all([git, serve]);
  assert.deepEqual(calls, ['/api/state', '/api/git/fetch', '/api/serve']);
});

test('an admission refusal from another tab is retried before the next action', async () => {
  const calls = [];
  const api = client(async (path) => {
    calls.push(path);
    return calls.length === 1 ? response(409, { retryable: true, error: 'busy' }) : response();
  });
  await Promise.all([api('/api/git'), api('/api/stop', {})]);
  assert.deepEqual(calls, ['/api/git', '/api/git', '/api/stop']);
});

test('other conflicts are never replayed and do not poison the queue', async () => {
  const calls = [];
  const api = client(async (path) => {
    calls.push(path);
    return path === '/api/git/switch' ? response(409, { error: 'dirty checkout' }) : response();
  });
  const refused = api('/api/git/switch', { branch: 'main' });
  const next = api('/api/git');
  await assert.rejects(refused, /dirty checkout/);
  await next;
  assert.deepEqual(calls, ['/api/git/switch', '/api/git']);
});
