import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './mock';
import type { EnclaveApi } from './client';
import type { ApiError, DomainSpec, TunnelSpec } from './types';

// The mock holds public access to the server's contract (L3-002 §16): app links and their gate, domain
// tunnels with public domains, references between them, and edge machines.

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

const settle = () => vi.advanceTimersByTimeAsync(2000);

describe('mock api, public access', () => {
  it('gives an app link its address and gate, and refuses what the server refuses', async () => {
    const api = await signedIn();
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 } as never);
    await settle();
    const quick = await api.createResource('tunnel', 'demo', { provider: 'quick', app: 'web' } as TunnelSpec);
    await settle();
    const got = await api.getResource(quick.id);
    expect(got.status.phase).toBe('ready');
    expect(String(got.outputs.url)).toMatch(/^https:\/\/[a-z-]+-[a-z0-9]{4}\.trycloudflare\.com$/);
    expect(got.outputs).toMatchObject({ connection: 'connected', gate: 'signin' });

    // A private Tailscale link is gated by the tailnet; the mode defaults to private.
    await api.createResource('secret', 'ts-key', { value: 'tskey-auth-x' } as never);
    const ts = await api.createResource('tunnel', 'team', { provider: 'tailscale', app: 'web', credentialsSecret: 'ts-key' } as TunnelSpec);
    await settle();
    const tsGot = await api.getResource(ts.id);
    expect((tsGot.spec as TunnelSpec).mode).toBe('private');
    expect(tsGot.outputs).toMatchObject({ gate: 'none', funnel: false, url: 'https://default-web.tail1234.ts.net' });

    expect((await fails(api.createResource('tunnel', 'nope', { provider: 'quick', app: 'missing' } as TunnelSpec)))?.details[0]).toMatchObject({ field: 'spec.app', code: 'NOT_FOUND' });
    const pw = await fails(api.createResource('tunnel', 'pw-link', { provider: 'quick', app: 'web', gate: { mode: 'password', passwordSecret: 'nope' } } as TunnelSpec));
    expect(pw?.details.some((d) => d.field === 'spec.gate.passwordSecret')).toBe(true);
    // The app is in use while a link reaches it.
    expect((await fails(api.deleteResource((await api.listResources('app')).find((r) => r.name === 'web')?.id ?? '')))?.code).toBe('RESOURCE_IN_USE');
  });

  it('serves a public domain through a Cloudflare tunnel, with its record and Access', async () => {
    const api = await signedIn();
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 } as never);
    await api.createResource('secret', 'cf-token', { value: '{"apiToken":"t"}' } as never);
    await api.createResource('zone', 'example', { provider: 'cloudflare', zone: 'example.com', credentialsSecret: 'cf-token' } as never);
    const home = await api.createResource('tunnel', 'home', { provider: 'cloudflare', credentialsSecret: 'cf-token', accountId: '0123456789ABCDEF0123456789abcdef' } as TunnelSpec);
    await settle();
    expect((await api.getResource(home.id)).spec).toMatchObject({ accountId: '0123456789abcdef0123456789abcdef' });

    // A Cloudflare tunnel needs the zone; Access only through Cloudflare; an app link can't carry a domain.
    expect((await fails(api.createResource('domain', 'stage', { hostname: 'stage.example.com', app: 'web', tunnel: 'home' } as DomainSpec)))?.details.some((d) => d.field === 'spec.zone')).toBe(true);
    await api.createResource('tunnel', 'demo', { provider: 'quick', app: 'web' } as TunnelSpec);
    expect((await fails(api.createResource('domain', 'via-link', { hostname: 'x.example.com', app: 'web', zone: 'example', tunnel: 'demo' } as DomainSpec)))?.details.some((d) => d.field === 'spec.tunnel')).toBe(true);

    const d = await api.createResource('domain', 'stage', { hostname: 'stage.example.com', app: 'web', zone: 'example', tunnel: 'home', cloudflareAccess: { emails: ['sunny@example.com'] } } as DomainSpec);
    await settle();
    const got = await api.getResource(d.id);
    expect(got.outputs).toMatchObject({ public: true, url: 'https://stage.example.com', certificateState: 'edge', gate: 'signin', accessApplicationId: 'acc-stage' });
    expect(got.outputs.dnsRecord).toMatchObject({ type: 'CNAME', name: 'stage.example.com', state: 'created' });
    expect(String((got.outputs.dnsRecord as unknown as { value: string }).value)).toMatch(/\.cfargotunnel\.com$/);

    const used = await fails(api.deleteResource(home.id));
    expect(used?.code).toBe('RESOURCE_IN_USE');
  });

  it('adds an edge machine that only relays, and runs one relay on it', async () => {
    const api = await signedIn();
    expect((await fails(api.addMachine('vps1', {}, { address: 'not an address!' })))?.details[0]?.field).toBe('address');
    const added = await api.addMachine('vps1', {}, { address: '203.0.113.9' });
    expect(added.machine).toMatchObject({ role: 'edge', address: '203.0.113.9', schedulable: false });
    expect(added.joinCommand).toBe(`enclave agent join --edge --code ${added.joinCode}`);
    await settle();
    const vps = await api.getMachineByName('vps1');
    expect(vps).toMatchObject({ state: 'online', role: 'edge', schedulable: false });
    expect((await fails(api.updateMachineByName('vps1', { schedulable: true })))?.details[0]?.field).toBe('schedulable');

    await api.addMachine('rack-1');
    await settle();
    expect((await fails(api.createResource('tunnel', 'relay-1', { provider: 'relay', machine: 'rack-1' } as TunnelSpec)))?.details[0]?.message).toMatch(/isn't an edge machine/);
    const relay = await api.createResource('tunnel', 'edge', { provider: 'relay', machine: 'vps1' } as TunnelSpec);
    await settle();
    expect((await api.getResource(relay.id)).outputs).toMatchObject({ connection: 'connected', publicAddress: '203.0.113.9' });
    expect((await fails(api.createResource('tunnel', 'edge2', { provider: 'relay', machine: 'vps1' } as TunnelSpec)))?.details[0]?.message).toMatch(/already runs a relay/);

    // A public domain without a zone: point the A record yourself.
    await api.createResource('app', 'web', { image: 'nginx:1.27-alpine', port: 80 } as never);
    const d = await api.createResource('domain', 'stage', { hostname: 'stage.example.com', app: 'web', tunnel: 'edge' } as DomainSpec);
    await settle();
    expect((await api.getResource(d.id)).outputs.dnsRecord).toMatchObject({ type: 'A', value: '203.0.113.9', state: 'manual' });

    // The edge machine can't be removed while its relay runs.
    expect((await fails(api.removeMachine('vps1')))?.details[0]?.field).toBe('tunnel/edge');
  });
});
