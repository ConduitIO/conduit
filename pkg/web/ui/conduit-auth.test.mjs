// Behavior tests for conduit-auth.js. Run by TestAuthScript_Behavior (node --test).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const src = readFileSync(new URL('./conduit-auth.js', import.meta.url), 'utf8');

class FakeStorage {
  constructor() { this.m = new Map(); this.throws = false; }
  getItem(k) { if (this.throws) throw new Error('blocked'); return this.m.has(k) ? this.m.get(k) : null; }
  setItem(k, v) { if (this.throws) throw new Error('blocked'); this.m.set(k, String(v)); }
  removeItem(k) { if (this.throws) throw new Error('blocked'); this.m.delete(k); }
}

class FakeWS {
  static instances = [];
  static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
  constructor(url, protocols) {
    this.url = url; this.protocols = protocols; this.l = {};
    FakeWS.instances.push(this);
  }
  addEventListener(t, f) { (this.l[t] ||= []).push(f); }
  fire(t) { (this.l[t] || []).forEach((f) => f()); }
}

// setup builds a page-like global with a scripted server: `serve(req)` returns a
// status given the Request the script sent.
function setup({ token = '', serve = () => 200, prompt = async () => '' } = {}) {
  const storage = new FakeStorage();
  if (token) storage.setItem('conduit.api.token', token);
  const seen = [];
  const win = {
    location: new URL('http://127.0.0.1:8080/pipelines'),
    sessionStorage: storage,
    localStorage: { getItem() { throw new Error('localStorage must not be used'); }, setItem() { throw new Error('localStorage must not be used'); }, removeItem() { throw new Error('localStorage must not be used'); } },
    WebSocket: FakeWS,
    fetch: async (req) => {
      const r = req instanceof Request ? req : new Request(new URL(req, win.location).href);
      const body = r.method === 'GET' || r.method === 'HEAD' ? '' : await r.clone().text();
      seen.push({ url: r.url, method: r.method, auth: r.headers.get('authorization'), body });
      return new Response('x', { status: serve(r, seen.length) });
    },
  };
  win.window = win;
  const prompts = { n: 0 };
  Object.defineProperty(win, 'document', { get() { throw new Error('document.cookie must not be touched'); } });
  // Browsers resolve a relative Request URL against the document; node's does not.
  class PageRequest extends Request {
    constructor(input, init) {
      super(typeof input === 'string' ? new URL(input, win.location).href : input, init);
    }
  }
  const ctx = vm.createContext({ ...win, window: undefined, URL, Request: PageRequest, Response, TextEncoder, btoa, Promise, Array, String, console });
  ctx.window = ctx; ctx.window.window = ctx;
  Object.assign(ctx, { location: win.location, sessionStorage: storage, WebSocket: FakeWS, fetch: win.fetch });
  vm.runInContext(src, ctx);
  ctx.__conduitAuth.promptForToken = async () => { prompts.n++; return prompt(prompts.n); };
  FakeWS.instances.length = 0;
  return { ctx, storage, seen, prompts };
}

test('API fetch carries the stored token as a Bearer header', async () => {
  const { ctx, seen } = setup({ token: 'tok-123456789012345' });
  const r = await ctx.fetch('/v1/pipelines');
  assert.equal(r.status, 200);
  assert.equal(seen[0].auth, 'Bearer tok-123456789012345');
});

test('non-API and cross-origin requests never get the token', async () => {
  const { ctx, seen } = setup({ token: 'tok-123456789012345' });
  await ctx.fetch('/assets/app.js');
  await ctx.fetch('http://evil.example/v1/pipelines');
  await ctx.fetch('http://127.0.0.1:9999/v1/pipelines');
  await ctx.fetch('/metrics');
  assert.deepEqual(seen.map((s) => s.auth), [null, null, null, null]);
});

test('401 with no token prompts, stores it in sessionStorage, retries and succeeds', async () => {
  const { ctx, storage, seen, prompts } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => 'good-token-good-token',
  });
  const r = await ctx.fetch('/v1/pipelines');
  assert.equal(r.status, 200);
  assert.equal(prompts.n, 1);
  assert.equal(storage.getItem('conduit.api.token'), 'good-token-good-token');
  assert.equal(seen.length, 2);
  assert.equal(seen[0].auth, null);
  assert.equal(seen[1].auth, 'Bearer good-token-good-token');
});

test('401 with a stale token clears it, prompts again, and replaces it', async () => {
  const { ctx, storage, prompts } = setup({
    token: 'stale-token-stale-token',
    serve: (r) => (r.headers.get('authorization') === 'Bearer fresh-token-fresh-token' ? 200 : 401),
    prompt: async () => 'fresh-token-fresh-token',
  });
  const r = await ctx.fetch('/v1/pipelines');
  assert.equal(r.status, 200);
  assert.equal(prompts.n, 1);
  assert.equal(storage.getItem('conduit.api.token'), 'fresh-token-fresh-token');
});

test('a wrong token typed at the prompt is cleared, not kept', async () => {
  const { ctx, storage } = setup({ serve: () => 401, prompt: async () => 'wrong-token-wrong-token' });
  const r = await ctx.fetch('/v1/pipelines');
  assert.equal(r.status, 401);
  assert.equal(storage.getItem('conduit.api.token'), null);
});

test('cancelling the prompt returns the 401 and stores nothing', async () => {
  const { ctx, storage, prompts } = setup({ serve: () => 401, prompt: async () => '' });
  const r = await ctx.fetch('/v1/pipelines');
  assert.equal(r.status, 401);
  assert.equal(prompts.n, 1);
  assert.equal(storage.getItem('conduit.api.token'), null);
});

test('a POST body survives the retry', async () => {
  const { ctx, seen } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => 'good-token-good-token',
  });
  await ctx.fetch('/v1/pipelines', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"config":{"name":"p"}}' });
  assert.equal(seen.length, 2);
  assert.equal(seen[1].body, '{"config":{"name":"p"}}');
  assert.equal(seen[1].method, 'POST');
});

test('concurrent 401s share one prompt', async () => {
  const { ctx, prompts } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => { await new Promise((r) => setTimeout(r, 20)); return 'good-token-good-token'; },
  });
  const rs = await Promise.all([ctx.fetch('/v1/pipelines'), ctx.fetch('/v1/connectors'), ctx.fetch('/v1/processors')]);
  assert.deepEqual(rs.map((r) => r.status), [200, 200, 200]);
  assert.equal(prompts.n, 1);
});

test('the token is never put in a URL, localStorage or cookie', async () => {
  const { ctx, seen } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => 'good-token-good-token',
  });
  await ctx.fetch('/v1/pipelines'); // setup throws if localStorage or document is touched
  new ctx.WebSocket('ws://127.0.0.1:8080/v1/connectors/x/inspect');
  for (const s of seen) assert.ok(!s.url.includes('good-token'));
  for (const w of ctx.WebSocket === undefined ? [] : FakeWS.instances) assert.ok(!w.url.includes('good-token'));
});

test('works when sessionStorage is blocked (token kept in memory)', async () => {
  const { ctx, storage, prompts } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => 'good-token-good-token',
  });
  storage.throws = true;
  assert.equal((await ctx.fetch('/v1/pipelines')).status, 200);
  assert.equal((await ctx.fetch('/v1/connectors')).status, 200);
  assert.equal(prompts.n, 1);
});

test('WebSocket offers conduit plus the base64url token carrier, never a query token', () => {
  const { ctx } = setup({ token: 'tok é+/=123456789' });
  new ctx.WebSocket('ws://127.0.0.1:8080/v1/connectors/x/inspect');
  const ws = FakeWS.instances[0];
  assert.equal(ws.url, 'ws://127.0.0.1:8080/v1/connectors/x/inspect');
  assert.equal(ws.protocols[0], 'conduit');
  const carrier = ws.protocols[1];
  assert.ok(carrier.startsWith('conduit.token.'));
  assert.match(carrier, /^[A-Za-z0-9._-]+$/); // valid subprotocol token characters
  const b64 = carrier.slice('conduit.token.'.length).replace(/-/g, '+').replace(/_/g, '/');
  assert.equal(Buffer.from(b64, 'base64').toString('utf8'), 'tok é+/=123456789');
});

test('WebSocket without a stored token or to a non-API URL carries no carrier', () => {
  const { ctx } = setup();
  new ctx.WebSocket('ws://127.0.0.1:8080/v1/connectors/x/inspect');
  assert.equal(FakeWS.instances[0].protocols, undefined);
  const { ctx: c2 } = setup({ token: 'tok-123456789012345' });
  new c2.WebSocket('ws://evil.example/v1/connectors/x/inspect');
  new c2.WebSocket('ws://127.0.0.1:8080/other', 'chat');
  assert.equal(FakeWS.instances[0].protocols, undefined);
  assert.equal(FakeWS.instances[1].protocols, 'chat');
});

test('a refused WebSocket upgrade triggers a probe that prompts for the token', async () => {
  const { ctx, prompts, seen } = setup({
    serve: (r) => (r.headers.get('authorization') === 'Bearer good-token-good-token' ? 200 : 401),
    prompt: async () => 'good-token-good-token',
  });
  new ctx.WebSocket('ws://127.0.0.1:8080/v1/connectors/x/inspect');
  FakeWS.instances[0].fire('close'); // closed without ever opening
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(prompts.n, 1);
  assert.ok(seen.some((s) => s.url.endsWith('/v1/plugins')));
});

test('WebSocket constants and prototype are preserved', () => {
  const { ctx } = setup();
  assert.equal(ctx.WebSocket.OPEN, 1);
  assert.ok(new ctx.WebSocket('ws://127.0.0.1:8080/x') instanceof FakeWS);
});
