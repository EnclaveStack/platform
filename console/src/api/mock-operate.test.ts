import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, ChannelSpec, Resource } from './types';

// The mock holds v0.7 to the server's contract (L3-002 §10): projects as namespaces, roles, people,
// tokens, metrics, tests, and secrets read from a store.

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

describe('mock api, v0.7', () => {
  it('keeps names per project: the same name twice, references inside the project, private DNS with the project (PRJ-02, ADR-035)', async () => {
    const api = await signedIn();
    await api.createProject('shop', 'the shop');
    const a = await api.createResource('secret', 'key', { value: 'one' });
    const b = await api.createResource('secret', 'key', { value: 'two' }, 'shop');
    expect([a.project, b.project]).toEqual(['default', 'shop']);
    expect((await fails(api.createResource('secret', 'key', { value: 'x' }, 'shop')))?.code).toBe('NAME_TAKEN');
    // An app in shop can't use default's secret by name.
    await api.createResource('secret', 'only-default', { value: 'x' });
    expect((await fails(api.createResource('app', 'api', { image: 'nginx', secretEnv: { K: 'only-default' } }, 'shop')))?.code).toBe('VALIDATION_FAILED');
    const app = await api.createResource('app', 'api', { image: 'nginx', secretEnv: { K: 'key' } }, 'shop');
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(app.id)).outputs.privateDns).toBe('api.app.shop.internal');
    expect((await api.listResources('secret', 'shop')).map((r) => r.name)).toEqual(['key']);
    expect((await fails(api.listResources(undefined, 'nope')))?.code).toBe('NOT_FOUND');
  });

  it('refuses to delete a project that still has resources, and lists them (PRJ-05)', async () => {
    const api = await signedIn();
    await api.createProject('staging', '');
    const s = await api.createResource('secret', 'key', { value: 'v' }, 'staging');
    const e = await fails(api.deleteProject('staging'));
    expect(e?.code).toBe('PROJECT_NOT_EMPTY');
    expect(e?.details.map((d) => d.field)).toEqual(['secret/key']);
    await vi.advanceTimersByTimeAsync(500);
    await api.deleteResource(s.id);
    await vi.advanceTimersByTimeAsync(500);
    await api.deleteProject('staging');
    expect((await api.listProjects()).map((p) => p.name)).not.toContain('staging');
    expect((await fails(api.deleteProject('default')))?.code).toBe('ACTION_NOT_SUPPORTED');
  });

  it('invites people with a one-time link, changes and removes roles, and audits it (IAM-01 – IAM-09)', async () => {
    const api = await signedIn();
    await api.createProject('shop', '');
    expect((await fails(api.inviteUser({ email: 'dev@example.test' })))?.code).toBe('VALIDATION_FAILED');
    const inv = await api.inviteUser({ email: 'Dev@Example.test', projectRoles: { shop: 'viewer' } });
    expect(inv.user).toMatchObject({ email: 'dev@example.test', status: 'invited', projectRoles: { shop: 'viewer' } });
    const code = new URL(inv.url).searchParams.get('code') ?? '';
    expect(await api.getInvite(code)).toEqual({ email: 'dev@example.test', kind: 'invite' });
    expect((await fails(api.acceptInvite(code, 'short')))?.code).toBe('VALIDATION_FAILED');
    await api.acceptInvite(code, 'a-long-enough-password');
    expect((await fails(api.getInvite(code)))?.code).toBe('NOT_FOUND');
    const updated = await api.updateUser(inv.user.id, { projectRoles: { shop: null }, machineRole: 'editor' });
    expect(updated).toMatchObject({ machineRole: 'editor', projectRoles: {}, status: 'active' });
    expect((await api.resetUser(inv.user.id)).url).toContain('/invite?code=');
    await api.removeUser(inv.user.id);
    expect((await api.listUsers()).map((u) => u.email)).toEqual([DEMO_ADMIN.email]);
    expect((await api.listAudit()).items.map((a) => a.action)).toEqual(expect.arrayContaining(['user_invited', 'invite_accepted', 'user_updated', 'user_removed', 'project_created']));
  });

  it('gives tokens a role and projects (TOK-01 – TOK-03)', async () => {
    const api = await signedIn();
    await api.createProject('shop', '');
    const t = await api.createToken({ name: 'ci', role: 'viewer', projects: ['shop'] });
    expect(t.token).toMatchObject({ role: 'viewer', projects: ['shop'], scope: 'read', userEmail: DEMO_ADMIN.email });
    expect((await api.createToken({ name: 'all' })).token).toMatchObject({ role: 'admin', projects: null });
    expect((await fails(api.createToken({ name: 'x', projects: ['nope'] })))?.code).toBe('VALIDATION_FAILED');
  });

  it("answers 403 to a viewer's change and 404 for a project they can't see (ADR-036)", async () => {
    const api = await signedIn({ as: { machineRole: null, projectRoles: { default: 'viewer' } }, seed: 'demo' });
    const list = await api.listResources();
    expect(list.every((r) => r.project === 'default')).toBe(true);
    const e = await fails(api.createResource('secret', 'x', { value: 'v' }));
    expect(e).toMatchObject({ code: 'FORBIDDEN', message: 'This needs an editor role in default. Ask an admin of default.' });
    expect((await fails(api.createResource('secret', 'x', { value: 'v' }, 'shop')))?.code).toBe('NOT_FOUND');
    const secret = list.find((r) => r.kind === 'secret');
    expect((await fails(api.revealSecret(secret?.id ?? '')))?.code).toBe('FORBIDDEN');
  });

  it('samples metrics in the catalogue shape, no older than the resource, at most 500 points (MET-01 – MET-03)', async () => {
    vi.setSystemTime(new Date('2026-10-22T12:00:00Z'));
    const api = await signedIn();
    const app = await api.createResource('app', 'web', { image: 'nginx', port: 80 });
    const now = Math.floor(Date.now() / 1000);
    const d = await api.getMetrics(app.id, ['cpu', 'memory'], now - 3600, now);
    expect(d.items.map((s) => [s.metric, s.unit])).toEqual([['cpu', 'cores'], ['memory', 'bytes']]);
    expect(d.stepSeconds).toBe(30);
    expect(d.items[0]?.points.length).toBeGreaterThan(100);
    const month = await api.getMetrics(app.id, [], now - 30 * 86400, now);
    expect(month.items.length).toBeGreaterThan(5);
    expect(Math.max(...month.items.map((s) => s.points.length))).toBeLessThanOrEqual(500);
    expect((month.items[0]?.points[0]?.t ?? 0) >= now - 7 * 3600).toBe(true);
    expect((await api.getMachineMetrics(['diskFree'], now - 3600, now)).items[0]?.unit).toBe('bytes');
  });

  it('tests channels and alerts, and keeps their deliveries (NCH-03, NCH-04, ALR-07)', async () => {
    const api = await signedIn();
    const ok = await api.createResource('channel', 'ops', { type: 'webhook', webhook: { url: 'https://hooks.example.com/x' } } as ChannelSpec);
    const bad = await api.createResource('channel', 'broken', { type: 'webhook', webhook: { url: 'https://fail.example.com/x' } } as ChannelSpec);
    await vi.advanceTimersByTimeAsync(500);
    expect(await api.test(ok.id)).toMatchObject({ ok: true, items: [{ target: 'ops', ok: true }] });
    const app = await api.createResource('app', 'web', { image: 'nginx' });
    const alert = await api.createResource('alert', 'web-down', { condition: { type: 'failed', resources: ['app/web'] }, channels: ['ops', 'broken'] });
    expect((await fails(api.createResource('alert', 'x', { condition: { type: 'failed', resources: ['app/nope'] }, channels: ['ops'] })))?.code).toBe('VALIDATION_FAILED');
    await vi.advanceTimersByTimeAsync(500);
    const res = await api.test(alert.id);
    expect(res.ok).toBe(false);
    expect(res.items.map((i) => i.ok)).toEqual([true, false]);
    expect((await api.listNotifications(ok.id)).map((n) => n.event)).toEqual(['test', 'test']);
    expect((await api.listNotifications(bad.id))[0]).toMatchObject({ status: 'failed', lastError: 'connection refused' });
    expect(await api.listIncidents(alert.id)).toEqual([]);
    // The channel and the app are in use by the alert.
    expect((await fails(api.deleteResource(ok.id)))?.code).toBe('RESOURCE_IN_USE');
    expect((await fails(api.deleteResource(app.id)))?.code).toBe('RESOURCE_IN_USE');
  });

  it('reads a secret from a store, refreshes it, and fails it when the store refuses (EXT-02, EXT-03)', async () => {
    const api = await signedIn();
    await api.createResource('secret', 'vault-token', { value: 's.x' });
    const store = await api.createResource('secretstore', 'vault', { provider: 'vault', vault: { address: 'https://vault.example.com', tokenSecret: 'vault-token' } });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.getResource(store.id)).spec).toMatchObject({ refreshSeconds: 300, vault: { mount: 'secret' } });
    expect((await api.test(store.id)).ok).toBe(true);
    const synced = await api.createResource('secret', 'stripe', { source: { store: 'vault', key: 'shop/stripe', field: 'key' } });
    expect(synced.spec).toEqual({ source: { store: 'vault', key: 'shop/stripe', field: 'key' } });
    await vi.advanceTimersByTimeAsync(500);
    expect(await api.revealSecret(synced.id)).toBe('mock-shop/stripe#key');
    expect((await api.action(synced.id, 'refresh')).outputs.syncedAt).toBeTruthy();
    expect((await fails(api.createResource('secret', 'both', { value: 'v', source: { store: 'vault', key: 'k' } })))?.code).toBe('VALIDATION_FAILED');
    expect((await fails(api.deleteResource(store.id)))?.code).toBe('RESOURCE_IN_USE');
    const broken = await api.createResource('secretstore', 'broken', { provider: 'vault', vault: { address: 'https://fail.example.com', tokenSecret: 'vault-token' } });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.test(broken.id)).ok).toBe(false);
    const failing = await api.createResource('secret', 'nope', { source: { store: 'broken', key: 'k' } });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.getResource(failing.id)).status).toMatchObject({ phase: 'failed', message: 'The vault store refused: permission denied.' });
  });

  it('fills in the defaults of dashboards, alerts and log exports like the server', async () => {
    const api = await signedIn();
    await api.createResource('app', 'web', { image: 'nginx' });
    const alert = await api.createResource('channel', 'ops', { type: 'webhook', webhook: { url: 'https://h.example.com' } } as ChannelSpec)
      .then(() => api.createResource('alert', 'loop', { condition: { type: 'crash_loop', resources: ['app/web'] }, channels: ['ops'] }));
    expect((alert as Resource<'alert'>).spec.condition).toMatchObject({ restarts: 3, windowMinutes: 10 });
    await api.createResource('bucket', 'logs', {});
    const lx = await api.createResource('logexport', 'out', { destination: { bucket: { bucket: 'logs', prefix: 'app' } } });
    expect(lx.spec).toMatchObject({ flushSeconds: 60, destination: { bucket: { bucket: 'logs', prefix: 'app/' } } });
    await vi.advanceTimersByTimeAsync(500);
    expect((await api.getResource(lx.id)).outputs).toMatchObject({ linesSent: 0, lastError: '' });
  });

  it('emails invite and reset links through the chosen email service, and keeps the setting on other changes (IAM-04)', async () => {
    const api = await signedIn();
    const mail = await api.createResource('email', 'mail', {});
    await vi.advanceTimersByTimeAsync(500);
    expect((await fails(api.updateMachine({ publicAddress: null, acmeEmail: null, inviteEmail: { service: 'nope', from: 'hc@example.test' } })))?.code).toBe('VALIDATION_FAILED');
    await api.updateMachine({ publicAddress: null, acmeEmail: null, inviteEmail: { service: 'mail', from: 'hc@example.test' } });
    await api.updateMachine({ publicAddress: '203.0.113.7', acmeEmail: null });
    expect((await api.getMachine()).inviteEmail).toEqual({ project: 'default', service: 'mail', from: 'hc@example.test' });
    const inv = await api.inviteUser({ email: 'dev@example.test', machineRole: 'viewer' });
    expect(inv.emailed).toBe(true);
    const list = await api.listEmails(mail.id);
    expect(list.items[0]).toMatchObject({ to: ['dev@example.test'], from: 'hc@example.test' });
    expect((await api.getEmail(mail.id, list.items[0]?.id ?? '')).text).toContain(inv.url);
    await api.updateMachine({ publicAddress: null, acmeEmail: null, inviteEmail: null });
    expect((await api.inviteUser({ email: 'two@example.test', machineRole: 'viewer' })).emailed).toBe(false);
  });
});
