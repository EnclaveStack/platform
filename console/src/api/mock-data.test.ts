import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, DatabaseSpec, TranscoderSpec } from './types';

// The mock holds v0.6 to the server's contract (L3-002 §9): engines, managed passwords, references,
// immutable fields, messages, emails and transcodes.

async function signedIn(): Promise<EnclaveApi> {
  const api = createMockApi({ latencyMs: 0, stepMs: 50, storage: null });
  await api.signIn(DEMO_ADMIN.email, DEMO_ADMIN.password);
  return api;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const code = async (p: Promise<unknown>) => {
  try {
    await p;
    return 'ok';
  } catch (e) {
    return (e as ApiError).code;
  }
};

describe('mock api, v0.6', () => {
  it('runs every database engine with its own default version and ports', async () => {
    const api = await signedIn();
    const ch = await api.createResource('database', 'analytics', { engine: 'clickhouse' } as DatabaseSpec);
    await vi.advanceTimersByTimeAsync(1000);
    const ready = await api.getResource(ch.id);
    expect((ready.spec as DatabaseSpec).version).toBe('26.3');
    expect(ready.outputs).toMatchObject({ engine: 'clickhouse', internalPort: 8123, internalNativePort: 9000, passwordSecret: 'analytics-password' });
    expect(await code(api.updateResource(ch.id, { engine: 'postgres', version: '16' }, ready.generation))).toBe('IMMUTABLE_FIELD');
    expect(await code(api.createResource('database', 'bad', { engine: 'mysql', version: '17' }))).toBe('VALIDATION_FAILED');
    expect((await api.getMachine()).memoryReservedBytes).toBe(2048 * 2 ** 20);
  });

  it('sends, receives, deletes and purges queue messages; topics fan out (QUE-*, TOP-*)', async () => {
    const api = await signedIn();
    const q = await api.createResource('queue', 'inbox', { visibilityTimeoutSeconds: 5 });
    const failed = await api.createResource('queue', 'failed', {});
    await vi.advanceTimersByTimeAsync(1000);
    expect(await code(api.createResource('queue', 'emails', { maxReceives: 3, deadLetterQueue: 'nowhere' }))).toBe('VALIDATION_FAILED');
    await api.createResource('queue', 'emails', { maxReceives: 3, deadLetterQueue: 'failed' });
    expect((await api.listResources('secret')).map((s) => s.name)).toEqual(expect.arrayContaining(['inbox-queue-password', 'broker-credentials']));
    const t = await api.createResource('topic', 'orders', { subscriptions: [{ queue: 'inbox' }, { url: 'https://hooks.example.com/a' }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(await code(api.deleteResource(failed.id))).toBe('RESOURCE_IN_USE');
    expect(await code(api.deleteResource(q.id))).toBe('RESOURCE_IN_USE');

    await api.sendMessage(q.id, { body: 'direct', attributes: { kind: 'x' } });
    await api.sendMessage(t.id, { body: 'fanned' });
    const list = await api.listMessages(q.id);
    expect(list.items.map((m) => m.body)).toEqual(['direct', 'fanned']);
    expect(list.items[0]?.attributes).toEqual({ kind: 'x' });
    expect((await api.getDeliveries(t.id)).items).toEqual([
      expect.objectContaining({ target: 'queue/inbox', delivered: 1 }), expect.objectContaining({ target: 'https://hooks.example.com/a', delivered: 1 })]);
    const got = await api.receiveMessages(q.id, 10);
    expect(got).toHaveLength(2);
    expect(await api.receiveMessages(q.id, 10)).toHaveLength(0);
    expect((await api.listMessages(q.id)).stats).toMatchObject({ messages: 0, inFlight: 2 });
    const del = await api.deleteMessages(q.id, [got[0]!.receipt, got[0]!.receipt]);
    expect(del).toMatchObject({ deleted: 1, failed: [expect.objectContaining({ receipt: got[0]!.receipt })] });
    await vi.advanceTimersByTimeAsync(6000);
    expect((await api.receiveMessages(q.id, 10))[0]).toMatchObject({ body: 'fanned', receiveCount: 2 });
    await api.action(q.id, 'purge');
    expect((await api.listMessages(q.id)).items).toHaveLength(0);
    expect(await code(api.action(t.id, 'purge'))).toBe('ACTION_NOT_SUPPORTED');
    expect(await code(api.sendMessage(q.id, { body: 'x'.repeat(256 * 1024 + 1) }))).toBe('VALIDATION_FAILED');
  });

  it('keeps emails, checks senders and relays when asked (EML-*)', async () => {
    const api = await signedIn();
    await api.createResource('secret', 'smtp-login', { value: '{"username":"u","password":"p"}' });
    const e = await api.createResource('email', 'shop-mail', { domain: 'example.com' });
    await vi.advanceTimersByTimeAsync(1000);
    expect(await code(api.sendEmail(e.id, { from: 'x@other.test', to: ['a@b.test'], subject: 'Hi', text: 'x' }))).toBe('VALIDATION_FAILED');
    const sent = await api.sendEmail(e.id, { from: 'shop@example.com', to: ['a@b.test'], subject: 'Receipt', html: '<p>Thanks</p>' });
    expect((await api.listEmails(e.id)).items[0]).toMatchObject({ subject: 'Receipt', relay: 'captured' });
    expect((await api.getEmail(e.id, sent.id)).html).toBe('<p>Thanks</p>');
    const cur = await api.getResource(e.id);
    await api.updateResource(e.id, { mode: 'relay', relay: { host: 'smtp.example.com', credentialsSecret: 'smtp-login' } }, cur.generation);
    await vi.advanceTimersByTimeAsync(1000);
    await api.sendEmail(e.id, { from: 'shop@example.com', to: ['a@b.test'], subject: 'Second', text: 'x' });
    expect((await api.listEmails(e.id)).items[0]?.relay).toBe('relayed');
    const secret = (await api.listResources('secret')).find((s) => s.name === 'smtp-login');
    expect(await code(api.deleteResource(secret!.id))).toBe('RESOURCE_IN_USE');
  });

  it('serves CDNs on free hostnames and transcodes files by hand (CDN-*, TRN-*)', async () => {
    const api = await signedIn();
    await api.createResource('app', 'web', { image: 'nginx:1.27', port: 80 });
    await api.createResource('bucket', 'uploads', {});
    await vi.advanceTimersByTimeAsync(1000);
    const cdn = await api.createResource('cdn', 'assets', { hostname: 'Assets.localhost', origin: 'app/web' });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(cdn.id)).outputs).toMatchObject({ url: 'https://assets.localhost', origin: 'es-app-web:80' });
    expect(await code(api.createResource('domain', 'clash', { hostname: 'assets.localhost', app: 'web' }))).toBe('HOSTNAME_TAKEN');
    expect(await code(api.updateResource(cdn.id, { hostname: 'other.localhost' }, 1))).toBe('IMMUTABLE_FIELD');
    expect(await code(api.action(cdn.id, 'purge'))).toBe('ok');

    const tc = await api.createResource('transcoder', 'videos', { input: { bucket: 'uploads', prefix: 'videos' }, output: { bucket: 'uploads', prefix: 'media' }, presets: ['mp4-720p', 'hls-720p'] });
    await vi.advanceTimersByTimeAsync(1000);
    expect((tc.spec as TranscoderSpec).input.prefix).toBe('videos/');
    expect(await code(api.transcode(tc.id, 'other/a.mov'))).toBe('VALIDATION_FAILED');
    expect(await code(api.runNow(tc.id))).toBe('VALIDATION_FAILED');
    const run = await api.transcode(tc.id, 'videos/cat.mov');
    await vi.advanceTimersByTimeAsync(1000);
    const done = await api.getRun(tc.id, run.id);
    expect(done).toMatchObject({ status: 'succeeded', key: 'videos/cat.mov', outputs: ['media/cat/mp4-720p.mp4', 'media/cat/hls-720p/index.m3u8', 'media/cat/hls-720p/segment-000.ts'] });
    const bucket = (await api.listResources('bucket'))[0]!;
    expect(await code(api.deleteResource(bucket.id))).toBe('RESOURCE_IN_USE');
  });
});
