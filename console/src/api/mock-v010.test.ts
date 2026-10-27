import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN, previewName, type MockOptions } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, AppSpec, BackupSpec, DatabaseSpec, FunctionSpec, RunDetail, TopicSpec } from './types';

// The mock holds v0.10 to the server's contract (L3-002 §18): functions, previews, database options,
// volume backups and the standby.

async function signedIn(opts: MockOptions = {}): Promise<EnclaveApi> {
  const api = createMockApi({ latencyMs: 0, stepMs: 50, storage: null, ...opts });
  await api.signIn(DEMO_ADMIN.email, DEMO_ADMIN.password);
  return api;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const fails = async (p: Promise<unknown>) => {
  try {
    await p;
    return null;
  } catch (e) {
    return e as ApiError;
  }
};

describe('mock api, v0.10 functions (FN-*)', () => {
  it('runs on invoke with the event, and says busy past its concurrency', async () => {
    const api = await signedIn();
    const fn = await api.createResource('function', 'thumbs', { image: 'alpine:3', concurrency: 1 } as FunctionSpec);
    await vi.advanceTimersByTimeAsync(1000);
    const run = await api.invoke(fn.id, { key: 'a.png' });
    expect(run).toMatchObject({ trigger: 'invoke', status: 'running', event: { source: 'invoke', body: { key: 'a.png' } } });
    const busy = await fails(api.invoke(fn.id, {}));
    expect(busy?.code).toBe('FUNCTION_BUSY');
    expect(busy?.retryAfterSeconds).toBe(5);
    await vi.advanceTimersByTimeAsync(100);
    const done = (await api.getRun(fn.id, run.id)) as RunDetail;
    expect(done.status).toBe('succeeded');
    expect(done.outputTail).toContain('a.png');
    expect((await fails(api.invoke(fn.id, 'x'.repeat(RULES_INVOKE_MAX))))?.code).toBe('PAYLOAD_TOO_LARGE');
  });

  it('checks triggers and is fed by queues and topics', async () => {
    const api = await signedIn();
    const q = await api.createResource('queue', 'jobs', { visibilityTimeoutSeconds: 30 });
    await vi.advanceTimersByTimeAsync(1000);
    // A queue trigger needs the queue's visibility timeout past the function's timeout.
    expect((await fails(api.createResource('function', 'slow', { image: 'alpine:3', timeoutSeconds: 60, triggers: [{ queue: 'jobs' }] } as FunctionSpec)))?.code).toBe('VALIDATION_FAILED');
    expect((await fails(api.createResource('function', 'lost', { image: 'alpine:3', triggers: [{ bucket: 'nowhere' }] } as FunctionSpec)))?.code).toBe('VALIDATION_FAILED');
    await api.updateResource(q.id, { visibilityTimeoutSeconds: 120 }, (await api.getResource(q.id)).generation);
    const fn = await api.createResource('function', 'worker', { image: 'alpine:3', timeoutSeconds: 60, triggers: [{ queue: 'jobs' }] } as FunctionSpec);
    await vi.advanceTimersByTimeAsync(1000);
    await api.sendMessage(q.id, { body: 'hello' });
    expect((await api.listRuns(fn.id))[0]).toMatchObject({ trigger: 'queue' });

    const t = await api.createResource('topic', 'orders', { subscriptions: [{ function: 'worker' }] } as TopicSpec);
    await vi.advanceTimersByTimeAsync(1000);
    await vi.advanceTimersByTimeAsync(100);
    await api.sendMessage(t.id, { body: 'placed' });
    expect((await api.listRuns(fn.id))[0]).toMatchObject({ trigger: 'topic' });
    expect((await api.getDeliveries(t.id)).items).toEqual([expect.objectContaining({ target: 'function/worker' })]);
    expect((await fails(api.deleteResource(fn.id)))?.code).toBe('RESOURCE_IN_USE');
  });
});

const RULES_INVOKE_MAX = 256 * 1024 + 1;

describe('mock api, v0.10 previews (PRV-*)', () => {
  const gitApp = (previews: AppSpec['previews']): AppSpec => ({ source: { git: 'https://github.com/acme/web.git', ref: 'main' }, port: 8080, previews });

  it('makes one preview per ref up to the limit, refuses edits and deletes on its own', async () => {
    const api = await signedIn();
    const app = await api.createResource('app', 'web', gitApp({ enabled: true, maxActive: 1, domain: { suffix: 'preview.localhost' } }));
    await vi.advanceTimersByTimeAsync(2000);
    const p = await api.createPreview(app.id, 'feature/login');
    expect(p.name).toBe(previewName('web', 'feature/login'));
    expect(p.outputs).toMatchObject({ previewOf: 'web', previewRef: 'feature/login', url: `https://${p.name}.preview.localhost` });
    expect((p.spec as AppSpec).previews).toBeUndefined();
    // The same ref redeploys the same preview; another is past the limit.
    expect((await api.createPreview(app.id, 'feature/login')).id).toBe(p.id);
    expect((await fails(api.createPreview(app.id, 'feature/other')))?.code).toBe('PREVIEW_LIMIT');
    expect((await api.listPreviews(app.id)).map((x) => x.name)).toEqual([p.name]);
    await vi.advanceTimersByTimeAsync(2000);
    const ready = await api.getResource(p.id);
    expect((await fails(api.updateResource(p.id, { ...ready.spec, replicas: 2 }, ready.generation)))?.code).toBe('IMMUTABLE_FIELD');
    await api.deleteResource(p.id, ready.generation);
    await vi.advanceTimersByTimeAsync(2000);
    expect(await api.listPreviews(app.id)).toEqual([]);
  });

  it('needs previews turned on, and a webhook secret that exists', async () => {
    const api = await signedIn();
    const off = await api.createResource('app', 'site', gitApp(undefined));
    expect((await fails(api.createPreview(off.id, 'main')))?.code).toBe('ACTION_NOT_SUPPORTED');
    expect((await fails(api.createResource('app', 'hooked', { ...gitApp(undefined), source: { git: 'https://github.com/acme/web.git', ref: 'main', webhookSecret: 'nope' } })))?.code)
      .toBe('VALIDATION_FAILED');
    await api.createResource('secret', 'hook-secret', { value: 's3cret-value-long' });
    const hooked = await api.createResource('app', 'hooked', { ...gitApp(undefined), source: { git: 'https://github.com/acme/web.git', ref: 'main', webhookSecret: 'hook-secret', deployOnPush: true } });
    await vi.advanceTimersByTimeAsync(2000);
    expect(String((await api.getResource(hooked.id)).outputs.webhookUrl)).toContain(`/api/v1/hooks/git/${hooked.id}`);
  });
});

describe('mock api, v0.10 databases and volume backups (DBX-*, VBK-*)', () => {
  it('serves TLS with a CA, exposes only with TLS, and keeps the setup script', async () => {
    const api = await signedIn();
    expect((await fails(api.createResource('database', 'open', { engine: 'postgres', exposed: true } as DatabaseSpec)))?.code).toBe('VALIDATION_FAILED');
    expect((await fails(api.createResource('database', 'docs', { engine: 'mongodb', tls: true } as DatabaseSpec)))?.code).toBe('VALIDATION_FAILED');
    const db = await api.createResource('database', 'shop', { engine: 'postgres', tls: true, exposed: true, initScript: 'CREATE TABLE t (id int);' } as DatabaseSpec);
    await vi.advanceTimersByTimeAsync(2000);
    const ready = await api.getResource(db.id);
    expect(ready.outputs).toMatchObject({ tls: true, caCertificate: expect.stringContaining('BEGIN CERTIFICATE') as unknown });
    expect(ready.outputs.exposedAddresses).toEqual(expect.arrayContaining([expect.stringMatching(/:\d+$/)]));
    expect((await fails(api.updateResource(db.id, { ...ready.spec, initScript: 'DROP TABLE t;' }, ready.generation)))?.code).toBe('IMMUTABLE_FIELD');
  });

  it('backs up and restores a filesystem volume, never a disk', async () => {
    const api = await signedIn();
    await api.createResource('bucket', 'vault', {});
    const files = await api.createResource('volume', 'uploads', { type: 'filesystem' });
    await api.createResource('volume', 'root', { type: 'disk', sizeGiB: 10 });
    await vi.advanceTimersByTimeAsync(2000);
    expect((await fails(api.createResource('backup', 'disk-copy', { source: { volume: 'root' }, destination: { bucket: 'vault' } } as BackupSpec)))?.code).toBe('VALIDATION_FAILED');
    const b = await api.createResource('backup', 'uploads-nightly', { source: { volume: 'uploads' }, destination: { bucket: 'vault' } } as BackupSpec);
    await vi.advanceTimersByTimeAsync(2000);
    const run = await api.runNow(b.id);
    await vi.advanceTimersByTimeAsync(2000);
    const done = await api.getRun(b.id, run.id);
    expect(done.status).toBe('succeeded');
    expect(done.artifact?.key).toMatch(/\.tar\.gz$/);
    expect((await fails(api.deleteResource(files.id)))?.code).toBe('RESOURCE_IN_USE');
  });
});

describe('mock api, v0.10 standby (SBY-*)', () => {
  it('adds one standby, joins it with --standby and never places on it', async () => {
    const api = await signedIn({ joinAfterMs: 100 });
    const added = await api.addMachine('spare', {}, { standby: true });
    expect(added.machine.role).toBe('standby');
    expect(added.joinCommand).toContain('--standby');
    expect((await fails(api.addMachine('spare-2', {}, { standby: true })))?.code).toBe('STANDBY_EXISTS');
    await vi.advanceTimersByTimeAsync(200);
    const m = await api.getMachineByName('spare');
    expect(m).toMatchObject({ state: 'online', role: 'standby', schedulable: false, standby: { stale: false } });
    expect(m.standby?.lastCopyAt).toBeTruthy();
    expect((await fails(api.updateMachineByName('spare', { schedulable: true })))?.code).toBe('VALIDATION_FAILED');
  });
});
