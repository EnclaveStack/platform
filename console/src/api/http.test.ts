import { describe, expect, it, vi } from 'vitest';
import { createHttpApi } from './http';
import { ApiError } from './types';

type Call = { url: string; init: RequestInit };

function fakeFetch(responses: (() => Response)[]) {
  const calls: Call[] = [];
  const fn = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const next = responses.shift();
    if (!next) throw new TypeError('network down');
    return next();
  });
  return { fn: fn as unknown as typeof fetch, calls };
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) => () =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const noSleep = () => Promise.resolve();

describe('http adapter (L3-002)', () => {
  it('sends the CSRF header on writes and an Idempotency-Key on creates', async () => {
    const f = fakeFetch([json(202, { id: 'db_1' })]);
    await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).createResource('database', 'orders', { engine: 'postgres', version: '16' });
    const h = f.calls[0]!.init.headers as Record<string, string>;
    expect(h['X-Enclave-CSRF']).toBe('1');
    expect(h['Idempotency-Key']).toMatch(/^es-[0-9a-f]{32}$/);
    expect(JSON.parse(String(f.calls[0]!.init.body))).toEqual({ kind: 'database', name: 'orders', spec: { engine: 'postgres', version: '16' } });
  });

  it('posts a restore to the action route, once: restores are not retried', async () => {
    const f = fakeFetch([json(503, { error: { code: 'ENGINE_UNAVAILABLE', message: 'x' } })]);
    const api = createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep });
    await expect(api.restore('bak_1', { runId: 'run_1', mode: 'replace', confirmName: 'shop' })).rejects.toMatchObject({ code: 'ENGINE_UNAVAILABLE' });
    expect(f.calls).toHaveLength(1);
    expect(f.calls[0]!.url).toBe('/api/v1/resources/bak_1/actions/restore');
    expect(f.calls[0]!.init.method).toBe('POST');
    expect(JSON.parse(String(f.calls[0]!.init.body))).toEqual({ runId: 'run_1', mode: 'replace', confirmName: 'shop' });
  });

  it('asks for the build log with build=true', async () => {
    const f = fakeFetch([json(200, { items: [] })]);
    await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).getLogs('app_1', 500, true);
    expect(f.calls[0]!.url).toBe('/api/v1/resources/app_1/logs?tail=500&build=true');
  });

  it('sends If-Match on updates', async () => {
    const f = fakeFetch([json(202, { id: 'x' })]);
    await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).updateResource('x', { image: 'b' }, 3);
    expect((f.calls[0]!.init.headers as Record<string, string>)['If-Match']).toBe('"3"');
    expect(JSON.parse(String(f.calls[0]!.init.body))).toEqual({ spec: { image: 'b' } });
  });

  it('maps the L0-003 envelope, field details and Retry-After', async () => {
    const f = fakeFetch([json(422, { error: { code: 'VALIDATION_FAILED', message: 'Check the highlighted fields.', requestId: 'req_1', details: [{ field: 'name', code: 'TOO_SHORT', message: 'Too short' }] } })]);
    const err = await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).createResource('cache', 'x', { engine: 'redis', version: '7' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const ae = err as ApiError;
    expect([ae.code, ae.status, ae.requestId, ae.fieldMessage('name')]).toEqual(['VALIDATION_FAILED', 422, 'req_1', 'Too short']);
  });

  it('retries idempotent calls on 503 and honours Retry-After, then succeeds', async () => {
    const sleeps: number[] = [];
    const f = fakeFetch([json(503, { error: { code: 'ENGINE_UNAVAILABLE', message: 'x' } }, { 'Retry-After': '2' }), json(200, { name: 'm' })]);
    const m = await createHttpApi('/api/v1', { fetch: f.fn, sleep: async (ms) => void sleeps.push(ms) }).getMachine();
    expect(m.name).toBe('m');
    expect(sleeps).toEqual([2000]);
  });

  it('does not retry non-idempotent actions', async () => {
    const f = fakeFetch([json(503, { error: { code: 'INTERNAL', message: 'x' } })]);
    await expect(createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).action('x', 'restart')).rejects.toBeInstanceOf(ApiError);
    expect(f.calls).toHaveLength(1);
  });

  it('reports an unreachable server as NETWORK after retries', async () => {
    const f = fakeFetch([]);
    const err = (await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).getMachine().catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe('NETWORK');
    expect(f.calls).toHaveLength(3);
  });

  it('treats 401 on GET /session as signed out', async () => {
    const f = fakeFetch([json(401, { error: { code: 'UNAUTHENTICATED', message: 'Sign in.' } })]);
    expect(await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).getSession()).toBeNull();
  });

  it('follows cursors when listing', async () => {
    const f = fakeFetch([json(200, { items: [{ id: 'a' }], nextCursor: 'c1' }), json(200, { items: [{ id: 'b' }], nextCursor: null })]);
    const items = await createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep }).listResources('app');
    expect(items.map((i) => i.id)).toEqual(['a', 'b']);
    expect(f.calls[1]!.url).toContain('cursor=c1');
  });

  it('reads the root certificate as text and patches machine settings', async () => {
    const pem = '-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n';
    const f = fakeFetch([() => new Response(pem, { status: 200, headers: { 'Content-Type': 'application/x-pem-file' } }), json(200, { publicAddress: '203.0.113.7' })]);
    const api = createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep });
    await expect(api.getRootCA()).resolves.toBe(pem);
    await api.updateMachine({ publicAddress: '203.0.113.7', acmeEmail: null });
    expect(f.calls[1]!.url).toBe('/api/v1/machine');
    expect(f.calls[1]!.init.method).toBe('PATCH');
    expect(JSON.parse(String(f.calls[1]!.init.body))).toEqual({ publicAddress: '203.0.113.7', acmeEmail: null });
  });
});

describe('http adapter, v0.7 (L3-002 §10)', () => {
  it('lists and creates in a project, and asks for metrics with RFC 3339 times', async () => {
    const f = fakeFetch([json(200, { items: [], nextCursor: null }), json(202, { id: 'sec_1' }), json(200, { from: '', to: '', stepSeconds: 30, items: [] })]);
    const api = createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep });
    await api.listResources('secret', 'shop');
    await api.createResource('secret', 'key', { value: 'v' }, 'shop');
    await api.getMetrics('app_1', ['cpu', 'memory'], 1_800_000_000, 1_800_003_600);
    expect(f.calls[0]!.url).toBe('/api/v1/resources?limit=200&kind=secret&project=shop');
    expect(JSON.parse(String(f.calls[1]!.init.body))).toEqual({ kind: 'secret', name: 'key', project: 'shop', spec: { value: 'v' } });
    expect(f.calls[2]!.url).toBe('/api/v1/resources/app_1/metrics?metric=cpu&metric=memory&from=2027-01-15T08%3A00%3A00Z&to=2027-01-15T09%3A00%3A00Z');
  });

  it('reaches people, projects, tests and passwords at their routes', async () => {
    const f = fakeFetch([json(200, { items: [] }), json(200, { user: {}, url: 'u', expiresAt: '' }), json(200, { ok: true, items: [] }), () => new Response(null, { status: 204 })]);
    const api = createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep });
    await api.listProjects();
    await api.resetUser('usr_1');
    await api.test('chan_1');
    await api.changePassword('old-password', 'new-password!');
    expect(f.calls.map((c) => `${String(c.init.method)} ${c.url}`)).toEqual([
      'GET /api/v1/projects', 'POST /api/v1/users/usr_1/reset', 'POST /api/v1/resources/chan_1/actions/test', 'PUT /api/v1/session/password',
    ]);
    expect((f.calls[3]!.init.headers as Record<string, string>)['X-Enclave-CSRF']).toBe('1');
  });
});

describe('http adapter, v0.8 (L3-002 §11)', () => {
  it('calls the machine and catalogue endpoints', async () => {
    const f = fakeFetch([json(200, { items: [] }), json(201, { machine: {}, joinCode: 'c', joinCommand: 'x', expiresAt: '' }), () => new Response(null, { status: 204 }),
      json(202, { moving: [], pinned: [] }), json(200, { items: [] }), json(200, {}), json(201, {})]);
    const api = createHttpApi('/api/v1', { fetch: f.fn, sleep: noSleep });
    await api.listMachines();
    await api.addMachine('rack-1', { disk: 'ssd' });
    await api.removeMachine('rack-1', true);
    await api.drainMachine('rack-1');
    await api.listCatalogue();
    await api.getCatalogueEntry('wordpress', '1.0.0');
    await api.addTemplate('name: x');
    expect(f.calls.map((c) => `${c.init.method ?? ''} ${c.url}`)).toEqual(['GET /api/v1/machines', 'POST /api/v1/machines', 'DELETE /api/v1/machines/rack-1?force=true',
      'POST /api/v1/machines/rack-1/drain', 'GET /api/v1/catalogue', 'GET /api/v1/catalogue/wordpress?version=1.0.0', 'POST /api/v1/catalogue']);
    expect(JSON.parse(String(f.calls[1]!.init.body))).toEqual({ name: 'rack-1', labels: { disk: 'ssd' } });
    expect(JSON.parse(String(f.calls[6]!.init.body))).toEqual({ body: 'name: x' });
  });
});
