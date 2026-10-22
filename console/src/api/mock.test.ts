import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN, DEMO_SETUP_CODE } from './mock';
import type { EnclaveApi } from './client';

// The mock is held to the same contract as the server (ADR-002).

const fast = { latencyMs: 0, stepMs: 100, storage: null } as const;

async function signedIn(): Promise<EnclaveApi> {
  const api = createMockApi(fast);
  await api.signIn(DEMO_ADMIN.email, DEMO_ADMIN.password);
  return api;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('mock api', () => {
  it('requires the setup code and refuses a second setup', async () => {
    const api = createMockApi({ ...fast, setupRequired: true });
    await expect(api.setup({ setupCode: 'WRONG', machineName: 'M', email: 'a@b.c', password: 'long-enough-pw' })).rejects.toMatchObject({ code: 'INVALID_SETUP_CODE' });
    await expect(api.setup({ setupCode: DEMO_SETUP_CODE.toLowerCase(), machineName: 'M', email: 'a@b.c', password: 'long-enough-pw' })).resolves.toMatchObject({ machineName: 'M' });
    await expect(api.setup({ setupCode: DEMO_SETUP_CODE, machineName: 'M', email: 'a@b.c', password: 'long-enough-pw' })).rejects.toMatchObject({ code: 'SETUP_COMPLETE' });
  });

  it('walks the steps to ready and creates a database\'s managed password', async () => {
    const api = await signedIn();
    const db = await api.createResource('database', 'orders', { engine: 'postgres', version: '16' });
    expect(db.status.phase).toBe('pending');
    await vi.advanceTimersByTimeAsync(1000);
    const ready = await api.getResource(db.id);
    expect(ready.status.phase).toBe('ready');
    expect(ready.status.steps.every((s) => s.state === 'done')).toBe(true);
    expect(ready.outputs.passwordSecret).toBe('orders-password');
    const pw = (await api.listResources('secret')).find((s) => s.name === 'orders-password');
    expect(pw?.ownerId).toBe(db.id);
    await expect(api.deleteResource(pw!.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });
  });

  it('enforces validation, unique names, immutability and If-Match', async () => {
    const api = await signedIn();
    await expect(api.createResource('app', 'Bad', { image: '' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    await expect(api.createResource('app', 'web', { image: 'nginx', secretEnv: { K: 'missing' } })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    const c = await api.createResource('cache', 'sessions', { engine: 'redis', version: '7' });
    await expect(api.createResource('cache', 'sessions', { engine: 'redis', version: '7' })).rejects.toMatchObject({ code: 'NAME_TAKEN' });
    await expect(api.updateResource(c.id, { maxMemoryMiB: 512 }, 99)).rejects.toMatchObject({ code: 'PRECONDITION_FAILED' });
    const db = await api.createResource('database', 'main-db', { engine: 'postgres', version: '16' });
    await expect(api.updateResource(db.id, { version: '17' }, 1)).rejects.toMatchObject({ code: 'IMMUTABLE_FIELD' });
  });

  it('refuses to delete a secret an app uses, then allows it after', async () => {
    const api = await signedIn();
    const sec = await api.createResource('secret', 'stripe-key', { value: 'v' });
    const app = await api.createResource('app', 'web', { image: 'nginx', secretEnv: { K: 'stripe-key' } });
    await expect(api.deleteResource(sec.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });
    await api.deleteResource(app.id);
    await vi.advanceTimersByTimeAsync(200);
    await expect(api.deleteResource(sec.id)).resolves.toMatchObject({ status: { phase: 'deleting' } });
  });

  it('fails names starting with fail- and refuses actions on a failed resource', async () => {
    const api = await signedIn();
    const r = await api.createResource('app', 'fail-web', { image: 'nginx' });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.getResource(r.id)).status.phase).toBe('failed');
    await expect(api.action(r.id, 'restart')).rejects.toMatchObject({ code: 'ACTION_NOT_SUPPORTED' });
  });

  it('keeps token secrets out of listings and reveals secrets with an event', async () => {
    const api = await signedIn();
    const t = await api.createToken({ name: 'ci', role: 'viewer' });
    expect(t.secret).toMatch(/^hc_pat_/);
    expect(JSON.stringify(await api.listTokens())).not.toContain(t.secret);
    const sec = await api.createResource('secret', 'k1x', { value: 'shh' });
    expect(JSON.stringify(await api.getResource(sec.id))).not.toContain('shh');
    expect(await api.revealSecret(sec.id)).toBe('shh');
    expect((await api.getEvents(sec.id))[0]?.type).toBe('secret_revealed');
  });

  it('serves domains: hostname rules, unique hostnames, the app stays while used, settings and the CA', async () => {
    const api = await signedIn();
    await expect(api.getRootCA()).rejects.toMatchObject({ code: 'NOT_FOUND' });
    await api.createResource('app', 'web', { image: 'nginx', port: 80 });
    await expect(api.createResource('domain', 'bad', { hostname: '10.0.0.1', app: 'web' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    await expect(api.createResource('domain', 'nope', { hostname: 'x.localhost', app: 'missing' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    const dom = await api.createResource('domain', 'web-local', { hostname: 'Web.Localhost', app: 'web' });
    expect(dom.spec).toMatchObject({ hostname: 'web.localhost', tls: 'internal', redirectHttp: true });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(dom.id)).outputs).toMatchObject({ url: 'https://web.localhost', certificateState: 'issued' });
    await expect(api.createResource('domain', 'dup', { hostname: 'web.localhost', app: 'web' })).rejects.toMatchObject({ code: 'HOSTNAME_TAKEN' });
    const app = (await api.listResources('app'))[0]!;
    await expect(api.deleteResource(app.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });
    await expect(api.updateResource(app.id, { port: null }, app.generation)).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    const cur = await api.getResource(dom.id);
    await expect(api.updateResource(dom.id, { hostname: 'other.localhost' }, cur.generation)).rejects.toMatchObject({ code: 'IMMUTABLE_FIELD' });
    await expect(api.getRootCA()).resolves.toContain('BEGIN CERTIFICATE');
    const m = await api.updateMachine({ publicAddress: ' 203.0.113.7 ', acmeEmail: null });
    expect(m).toMatchObject({ publicAddress: '203.0.113.7', acmeEmail: null, edge: { available: true } });
    await expect(api.updateMachine({ publicAddress: null, acmeEmail: 'nope' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
  });

  it('runs jobs: overlap, results, app in use', async () => {
    const api = await signedIn();
    await api.createResource('app', 'api', { image: 'nginx', port: 80 });
    await expect(api.createResource('job', 'bad', { schedule: '@daily', app: 'api', image: 'busybox' })).rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    const job = await api.createResource('job', 'cleanup', { schedule: '@hourly', app: 'api', command: ['sh', '-c', 'exit 2'] });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.getResource(job.id)).outputs.nextRunAt).toBeTruthy();
    const run = await api.runNow(job.id);
    expect(run).toMatchObject({ trigger: 'manual', status: 'running' });
    await expect(api.runNow(job.id)).rejects.toMatchObject({ code: 'RUN_IN_PROGRESS' });
    await vi.advanceTimersByTimeAsync(200);
    expect(await api.getRun(job.id, run.id)).toMatchObject({ status: 'failed', exitCode: 2 });
    expect((await api.listRuns(job.id))[0]).not.toHaveProperty('outputTail');
    const app = (await api.listResources('app'))[0]!;
    await expect(api.deleteResource(app.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });
  });

  it('builds apps from Git: outputs, skipped rebuilds, failing builds, build logs and credentials (BLD-*)', async () => {
    const api = await signedIn();
    await expect(api.createResource('app', 'priv', { source: { git: 'https://github.com/acme/api.git', ref: 'main', credentialsSecret: 'git-token' } }))
      .rejects.toMatchObject({ code: 'VALIDATION_FAILED', details: [{ field: 'spec.source.credentialsSecret' }] });
    const sec = await api.createResource('secret', 'git-token', { value: '{"token":"t"}' });
    const app = await api.createResource('app', 'site', { source: { git: 'https://github.com/acme/api.git', ref: 'main', credentialsSecret: 'git-token' }, port: 8080 });
    expect(app.spec).toMatchObject({ source: { dockerfile: 'Dockerfile', context: '.' } });
    await vi.advanceTimersByTimeAsync(3000);
    const ready = await api.getResource(app.id);
    expect(ready.status.phase).toBe('ready');
    expect(ready.outputs).toMatchObject({ builtCommit: expect.stringMatching(/^[0-9a-f]{40}$/), builtImage: expect.stringMatching(/^enclave\.local\/site:r1-[0-9a-f]{12}$/), lastBuild: 'succeeded' });
    expect((await api.getLogs(app.id, 200, true)).map((l) => l.message)).toContain('#4 [3/4] RUN npm ci');
    await api.action(app.id, 'redeploy');
    await vi.advanceTimersByTimeAsync(3000);
    expect((await api.getResource(app.id)).outputs).toMatchObject({ lastBuild: 'skipped', builtCommit: ready.outputs.builtCommit });
    await expect(api.deleteResource(sec.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });
    await expect(api.getLogs(sec.id, 10, true)).rejects.toMatchObject({ code: 'ACTION_NOT_SUPPORTED' });

    const broken = await api.createResource('app', 'broken', { source: { git: 'https://github.com/acme/fail-tests.git', ref: 'main' } });
    await vi.advanceTimersByTimeAsync(3000);
    const failed = await api.getResource(broken.id);
    expect(failed.status).toMatchObject({ phase: 'failed', message: expect.stringContaining('did not complete successfully: exit code: 1') });
    expect(failed.outputs.lastBuild).toBe('failed');
    expect((await api.getLogs(broken.id, 5, true)).at(-1)?.message).toMatch(/ERROR/);
  });

  it('backs up, restores into a new database or over the source, and guards its references (BAK-*)', async () => {
    const api = await signedIn();
    const db = await api.createResource('database', 'shop', { engine: 'postgres', version: '16' });
    await api.createResource('bucket', 'vault', {});
    await expect(api.createResource('backup', 'bad', { source: { database: 'nope' }, destination: { bucket: 'vault' } }))
      .rejects.toMatchObject({ code: 'VALIDATION_FAILED', details: [{ field: 'spec.source', code: 'NOT_FOUND' }] });
    await expect(api.createResource('backup', 'both', { source: { database: 'shop', cache: 'c' }, destination: { bucket: 'vault' } }))
      .rejects.toMatchObject({ code: 'VALIDATION_FAILED' });
    const bk = await api.createResource('backup', 'nightly', { source: { database: 'shop' }, destination: { bucket: 'vault' } });
    expect(bk.spec).toMatchObject({ schedule: '0 3 * * *', keep: 7, prefix: 'enclave-backups', timeoutSeconds: 7200 });
    await vi.advanceTimersByTimeAsync(2000);
    const run = await api.runNow(bk.id);
    await expect(api.runNow(bk.id)).rejects.toMatchObject({ code: 'RUN_IN_PROGRESS' });
    await expect(api.restore(bk.id, { runId: run.id, mode: 'new', name: 'shop-copy' })).rejects.toMatchObject({ code: 'RUN_NOT_RESTORABLE' });
    await vi.advanceTimersByTimeAsync(700);
    const [done] = await api.listRuns(bk.id);
    expect(done).toMatchObject({ status: 'succeeded', artifact: { key: expect.stringMatching(/^enclave-backups\/shop\/\d{8}T\d{6}\.\d{3}Z-run_\w+\.pgdump$/) } });
    expect((await api.getResource(bk.id)).outputs.lastSuccessAt).toBeTruthy();

    const copy = await api.restore(bk.id, { runId: run.id, mode: 'new', name: 'shop-copy' });
    expect(copy).toMatchObject({ kind: 'database', name: 'shop-copy', spec: { version: '16' } });
    await expect(api.restore(bk.id, { runId: run.id, mode: 'replace', confirmName: 'shopp' }))
      .rejects.toMatchObject({ code: 'VALIDATION_FAILED', details: [{ field: 'confirmName' }] });
    const replaced = await api.restore(bk.id, { runId: run.id, mode: 'replace', confirmName: 'shop' });
    expect(replaced).toMatchObject({ id: db.id, status: { phase: 'updating' } });
    expect((await api.listRuns(bk.id))[0]).toMatchObject({ trigger: 'pre_restore' });

    await expect(api.deleteResource(db.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE', details: [{ field: 'backup/nightly' }] });
    const cur = await api.getResource(bk.id);
    await expect(api.updateResource(bk.id, { source: { database: 'shop-copy' } }, cur.generation)).rejects.toMatchObject({ code: 'IMMUTABLE_FIELD' });
    await expect(api.getLogs(bk.id)).rejects.toMatchObject({ code: 'ACTION_NOT_SUPPORTED' });

    // Retention: keep 1 leaves only the newest run restorable.
    const cur2 = await api.getResource(bk.id);
    await api.updateResource(bk.id, { keep: 1 }, cur2.generation);
    await vi.advanceTimersByTimeAsync(2000);
    await api.runNow(bk.id);
    await vi.advanceTimersByTimeAsync(700);
    const after = await api.listRuns(bk.id);
    expect(after.filter((x) => x.artifact)).toHaveLength(1);
    expect(after.find((x) => x.id === run.id)?.message).toContain('Removed by retention.');
    await expect(api.restore(bk.id, { runId: run.id, mode: 'new', name: 'shop-old' })).rejects.toMatchObject({ code: 'RUN_NOT_RESTORABLE' });
  });
});
