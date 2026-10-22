import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { choose, mainMachine, joined, waitingMachine } from './mock-machines';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, StackMember, StackSpec } from './types';

// The mock holds v0.8 to the server's contract (L3-002 §11): placement, moves, machines, the catalogue
// and stacks.

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

describe('placement (ADR-044)', () => {
  const GiB = 2 ** 30;
  const rack = joined(waitingMachine('mch_1', 'rack-1', { disk: 'ssd' }), '10.0.0.1');
  it('takes the machine with the most free memory, main on a tie, and honours labels', () => {
    const main = mainMachine('Mac', 0, 0);
    expect(choose({}, [main, rack], GiB)).toEqual({ machine: 'main' }); // 16 GiB free beats 8
    expect(choose({}, [{ ...main, memoryTotalBytes: 8 * GiB }, rack], GiB)).toEqual({ machine: 'main' });
    expect(choose({ machineLabels: { disk: 'ssd' } }, [main, rack], GiB)).toEqual({ machine: 'rack-1' });
    expect(choose({ machineLabels: { disk: 'nvme' } }, [main, rack], GiB)).toEqual({ reason: 'No machine has the labels disk=nvme.' });
    expect(choose({}, [main, { ...rack, schedulable: false }], 20 * GiB)).toEqual({ reason: 'No machine has 20480 MiB free.' });
    expect(choose({ machine: 'nowhere' }, [main], GiB)).toEqual({ reason: "There's no machine named nowhere." });
  });
});

describe('mock api, v0.8', () => {
  it('lists machines, refuses main as a name, and removes only empty ones (L3-002 §11.2)', async () => {
    const api = await signedIn({ machines: [{ name: 'rack-1' }] });
    expect((await api.listMachines()).map((m) => m.name)).toEqual(['main', 'rack-1']);
    expect((await fails(api.addMachine('main')))?.code).toBe('VALIDATION_FAILED');
    expect((await fails(api.addMachine('rack-1')))?.code).toBe('NAME_TAKEN');
    const added = await api.addMachine('rack-2', { disk: 'ssd' });
    expect(added.machine.state).toBe('waiting');
    expect(added.joinCommand).toContain(`--code ${added.joinCode}`);
    await api.createResource('database', 'orders', { engine: 'postgres', version: '16', machine: 'rack-1' } as never);
    await vi.advanceTimersByTimeAsync(400);
    const e = await fails(api.removeMachine('rack-1'));
    expect(e?.code).toBe('MACHINE_NOT_EMPTY');
    expect(e?.details[0]?.field).toBe('database/orders');
    expect((await fails(api.removeMachine('rack-1', true)))?.code).toBe('MACHINE_ONLINE');
    expect((await fails(api.removeMachine('main')))?.code).toBe('ACTION_NOT_SUPPORTED');
    expect((await fails(api.drainMachine('main')))?.code).toBe('ACTION_NOT_SUPPORTED');
    await api.removeMachine('rack-2');
    expect((await fails(api.getMachineByName('rack-2')))?.code).toBe('NOT_FOUND');
  });

  it('moves a resource when its machine changes, with move first in its steps (MOVE-*)', async () => {
    const api = await signedIn({ machines: [{ name: 'rack-1' }] });
    const db = await api.createResource('database', 'orders', { engine: 'postgres', version: '16' });
    await vi.advanceTimersByTimeAsync(400);
    expect((await api.getResource(db.id)).outputs.machine).toBe('main');
    const moving = await api.updateResource(db.id, { machine: 'rack-1' }, 1);
    expect(moving.status.steps[0]?.name).toBe('move');
    await vi.advanceTimersByTimeAsync(600);
    expect((await api.getResource(db.id)).outputs.machine).toBe('rack-1');
    expect((await api.getEvents(db.id)).map((x) => x.type)).toContain('moved');
    expect((await fails(api.createResource('cache', 'nowhere', { engine: 'redis', version: '7', machine: 'mars' } as never)))?.details[0]?.field).toBe('spec.machine');
  });

  it('installs a stack with its members, refuses member changes, and deletes them together (L3-002 §11.7)', async () => {
    const api = await signedIn();
    expect((await api.listCatalogue()).map((e) => e.name)).toEqual(['ghost', 'gitea', 'n8n', 'plausible', 'uptime-kuma', 'wordpress']);
    expect((await fails(api.createResource('stack', 'pub', { template: 'ghost' } as StackSpec)))?.details[0]?.field).toBe('spec.parameters.domain');
    await api.createResource('database', 'taken-db', { engine: 'postgres', version: '16' });
    expect((await fails(api.createResource('stack', 'taken', { template: 'wordpress' } as StackSpec)))?.code).toBe('NAME_TAKEN');
    expect((await api.listResources('volume')).length).toBe(0); // nothing created
    const st = await api.createResource('stack', 'blog', { template: 'wordpress' } as StackSpec);
    await vi.advanceTimersByTimeAsync(1000);
    const ready = await api.getResource(st.id);
    expect(ready.status.phase).toBe('ready');
    const members = ready.outputs.members as unknown as StackMember[];
    expect(members.map((m) => `${m.kind}/${m.name}`).sort()).toEqual(['app/blog', 'database/blog-db', 'volume/blog-content']);
    const db = members.find((m) => m.kind === 'database');
    expect((await fails(api.deleteResource(db?.id ?? '')))?.code).toBe('PART_OF_STACK');
    expect((await fails(api.updateResource(st.id, { template: 'ghost' }, ready.generation)))?.code).toBe('IMMUTABLE_FIELD');
    await api.updateResource(st.id, { parameters: { domain: 'blog.example.com' } }, ready.generation);
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.listResources('domain')).map((d) => d.name)).toEqual(['blog-site']);
    await api.deleteResource(st.id);
    await vi.advanceTimersByTimeAsync(200);
    expect((await api.listResources()).filter((r) => r.name.startsWith('blog'))).toEqual([]);
  });

  it('keeps its own templates by version and refuses removing one in use (L3-002 §11.5)', async () => {
    const api = await signedIn();
    const body = (v: string) => `name: tiny\ntitle: Tiny\nversion: ${v}\nresources:\n  - kind: app\n    name: ""\n`;
    expect((await fails(api.addTemplate(body('1.0.0').replace('name: tiny', 'name: wordpress'))))?.code).toBe('NAME_TAKEN');
    await api.addTemplate(body('1.0.0'));
    expect((await fails(api.addTemplate(body('1.0.0'))))?.code).toBe('NAME_TAKEN');
    expect((await api.addTemplate(body('1.1.0'))).versions).toEqual(['1.1.0', '1.0.0']);
    await api.createResource('stack', 'small', { template: 'tiny' } as StackSpec);
    expect((await fails(api.removeTemplate('tiny')))?.code).toBe('RESOURCE_IN_USE');
    expect((await fails(api.removeTemplate('wordpress')))?.code).toBe('ACTION_NOT_SUPPORTED');
  });
});
