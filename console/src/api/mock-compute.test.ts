import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, VMSpec } from './types';

// The mock holds v0.5 to the server's contract (L3-002 §8): references, immutable fields, power, outputs.

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave';

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

describe('mock api, v0.5', () => {
  it('runs VMs with disks, guards attached volumes and keeps keys immutable', async () => {
    const api = await signedIn();
    const disk = await api.createResource('volume', 'data', { type: 'disk', sizeGiB: 20 });
    const fs = await api.createResource('volume', 'files', { type: 'filesystem' });
    await vi.advanceTimersByTimeAsync(1000);
    expect(await code(api.createResource('vm', 'bad', { image: 'debian-12', sshKeys: [KEY], disks: ['files'] }))).toBe('VALIDATION_FAILED');
    const vm = await api.createResource('vm', 'worker', { image: 'debian-12', sshKeys: [KEY], disks: ['data'] });
    await vi.advanceTimersByTimeAsync(1000);
    const ready = await api.getResource(vm.id);
    expect(ready.outputs).toMatchObject({ power: 'running', sshUser: 'enclave', privateDns: 'worker.vm.internal' });
    expect((await api.getResource(disk.id)).outputs.attachedTo).toBe('');
    expect(await code(api.deleteResource(disk.id, 1))).toBe('RESOURCE_IN_USE');
    expect(await code(api.updateResource(vm.id, { sshKeys: [KEY, KEY] }, ready.generation))).toBe('IMMUTABLE_FIELD');
    expect(await code(api.updateResource(vm.id, { diskGiB: 5 }, ready.generation))).toBe('VALIDATION_FAILED');

    expect(await code(api.action(fs.id, 'start'))).toBe('ACTION_NOT_SUPPORTED');
    await api.action(vm.id, 'stop');
    await vi.advanceTimersByTimeAsync(1000);
    expect(((await api.getResource(vm.id)).spec as VMSpec).power).toBe('stopped');
    expect((await api.getMachine()).reservedCpus).toBe(0);
    await api.action(vm.id, 'start');
    await vi.advanceTimersByTimeAsync(1000);
    const m = await api.getMachine();
    expect(m.reservedCpus).toBe(1);
    expect(m.vms.available).toBe(true);
    expect(m.images[0]).toMatchObject({ name: 'debian-12', usedBy: 1 });
    expect((await api.getLogs(vm.id)).at(-1)?.message).toBe('worker login:');
  });

  it('takes snapshots that start volumes of the same type only', async () => {
    const api = await signedIn();
    await api.createResource('volume', 'files', { type: 'filesystem' });
    await vi.advanceTimersByTimeAsync(1000);
    const snap = await api.createResource('snapshot', 'before', { source: 'volume/files' });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(snap.id)).outputs.type).toBe('filesystem');
    expect(await code(api.createResource('volume', 'as-disk', { type: 'disk', snapshot: 'before' }))).toBe('VALIDATION_FAILED');
    expect(await code(api.createResource('volume', 'copy', { type: 'filesystem', snapshot: 'before' }))).toBe('ok');
    expect(await code(api.updateResource(snap.id, { source: 'volume/copy' }, 1))).toBe('IMMUTABLE_FIELD');
    expect(await code(api.createResource('snapshot', 'orphan', { source: 'vm/nowhere' }))).toBe('VALIDATION_FAILED');
  });
});
