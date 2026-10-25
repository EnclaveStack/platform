import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, ZoneSpec } from './types';

// The mock holds v0.4 to the server's contract: placement, references and outputs (L3-002 §7).

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

describe('mock api, v0.4', () => {
  it('places resources, guards networks, firewalls, load balancers and zones', async () => {
    const api = await signedIn();
    const net = await api.createResource('network', 'backend', { cidr: '10.40.0.0/16', subnets: [{ name: 'data', cidr: '10.40.1.0/24' }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(await code(api.createResource('database', 'orders', { engine: 'postgres', version: '16', network: 'backend', subnet: 'nope' }))).toBe('VALIDATION_FAILED');
    const db = await api.createResource('database', 'orders', { engine: 'postgres', version: '16', network: 'backend', subnet: 'data' });
    const app = await api.createResource('app', 'api', { image: 'nginx:1.27', port: 8080, network: 'backend', replicas: 3 });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(db.id)).outputs.privateDns).toBe('orders.database.internal');
    expect((await api.getResource(app.id)).outputs.runningReplicas).toBe(3);

    expect(await code(api.createResource('firewall', 'bad', { targets: ['app/nope'] }))).toBe('VALIDATION_FAILED');
    await api.createResource('firewall', 'db-access', { targets: ['database/orders'], ingress: [{ from: ['app/api'], ports: ['5432'] }] });
    const lb = await api.createResource('loadbalancer', 'api-lb', { port: 80, targets: [{ app: 'api' }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(lb.id)).outputs.healthyTargets).toBe(3);

    expect(await code(api.deleteResource(net.id))).toBe('RESOURCE_IN_USE');
    expect(await code(api.deleteResource(db.id))).toBe('RESOURCE_IN_USE'); // the firewall names it
    expect(await code(api.deleteResource(app.id))).toBe('RESOURCE_IN_USE'); // a source and a load balancer target

    await api.createResource('secret', 'cf-token', { value: '{"apiToken":"t"}' });
    const zone = await api.createResource('zone', 'example', { provider: 'cloudflare', zone: 'Example.com', credentialsSecret: 'cf-token' });
    expect((zone.spec as ZoneSpec).zone).toBe('example.com');
    await api.updateMachine({ publicAddress: '203.0.113.7', acmeEmail: null });
    const rec = await api.createResource('record', 'apex', { zone: 'example', name: '@', type: 'A', machine: true });
    await vi.advanceTimersByTimeAsync(1000);
    expect((await api.getResource(rec.id)).outputs.fqdn).toBe('example.com');
    expect((await api.getResource(rec.id)).outputs.values).toBe('203.0.113.7');
    expect(await code(api.deleteResource(zone.id))).toBe('RESOURCE_IN_USE');
  });
});
