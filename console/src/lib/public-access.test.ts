import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { cliCommand, tofuBlock } from './codegen';
import { validateDomainAccess, validateGate, validateTunnel } from './validation';

// Public access rules (L3-002 §16.6) as the console checks them before the server does, and the
// commands it shows (CON-76, CON-77).

const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'default', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };

describe('tunnel validation', () => {
  it('asks each way in for what it needs', () => {
    expect(validateTunnel({ provider: 'quick' })).toEqual({ 'spec.app': 'Choose the app this link makes public.' });
    expect(validateTunnel({ provider: 'quick', app: 'web', credentialsSecret: 'x' })).toHaveProperty('spec.credentialsSecret');
    expect(validateTunnel({ provider: 'tailscale', app: 'web' })).toHaveProperty('spec.credentialsSecret');
    expect(validateTunnel({ provider: 'tailscale', app: 'web', credentialsSecret: 'k', hostname: 'Bad_Name' })).toHaveProperty('spec.hostname');
    expect(validateTunnel({ provider: 'cloudflare', credentialsSecret: 'cf' })).toHaveProperty('spec.accountId');
    expect(validateTunnel({ provider: 'cloudflare', credentialsSecret: 'cf', accountId: '0123456789abcdef0123456789abcdef' })).toEqual({});
    expect(validateTunnel({ provider: 'relay' })).toEqual({ 'spec.machine': 'Choose the edge machine the relay runs on.' });
    expect(validateTunnel({ provider: 'custom', app: 'web' })).toHaveProperty('spec.image');
  });

  it('keeps app, port and gate to app links, and ENCLAVE_UPSTREAM to EnclaveStack', () => {
    expect(validateTunnel({ provider: 'relay', machine: 'vps1', app: 'web', gate: { mode: 'signin' } })).toMatchObject({ 'spec.app': expect.any(String), 'spec.gate': expect.any(String) });
    expect(validateTunnel({ provider: 'custom', app: 'web', image: 'ghcr.io/acme/tunnel:1', env: { ENCLAVE_UPSTREAM: 'x' } })).toEqual({ 'spec.env.ENCLAVE_UPSTREAM': 'EnclaveStack sets ENCLAVE_UPSTREAM itself.' });
    expect(validateTunnel({ provider: 'quick', app: 'web', image: 'nginx' })).toHaveProperty('spec.image');
  });

  it('checks the gate: a password names its secret, open paths stay out of /.enclave/', () => {
    expect(validateGate({ mode: 'password' }, 'spec.gate')).toHaveProperty('spec.gate.passwordSecret');
    expect(validateGate({ mode: 'signin', passwordSecret: 'p' }, 'spec.gate')).toHaveProperty('spec.gate.passwordSecret');
    expect(validateGate({ mode: 'none', openPaths: ['/webhooks/', 'nope', '/.enclave/x'] }, 'spec.gate')).toEqual({
      'spec.gate.openPaths[1]': 'Use a path that starts with /, like /webhooks/.',
      'spec.gate.openPaths[2]': 'Paths under /.enclave/ belong to the gate.',
    });
  });

  it("checks a public domain's certificate, name and Access", () => {
    expect(validateDomainAccess({ hostname: 'web.localhost', app: 'web', gate: { mode: 'signin' } })).toHaveProperty('spec.gate');
    expect(validateDomainAccess({ hostname: 'web.localhost', app: 'web', tunnel: 'home', tls: 'internal' })).toMatchObject({ 'spec.tls': expect.any(String), 'spec.hostname': expect.any(String) });
    expect(validateDomainAccess({ hostname: 'stage.example.com', app: 'web', tunnel: 'home', tls: 'auto', cloudflareAccess: {} })).toEqual({ 'spec.cloudflareAccess': 'List at least one email or email domain.' });
    expect(validateDomainAccess({ hostname: 'stage.example.com', app: 'web', tunnel: 'home', tls: 'auto', cloudflareAccess: { emails: ['me@example.com'], emailDomains: ['example.com'] } })).toEqual({});
  });
});

describe('public access from code', () => {
  it('shows a tunnel as a CLI command and an OpenTofu block', () => {
    const t = { ...base, kind: 'tunnel', name: 'demo', spec: { provider: 'quick', app: 'web', gate: { mode: 'password', passwordSecret: 'stage-pass', openPaths: ['/webhooks/'] } } } as Resource;
    const cmd = cliCommand(t);
    expect(cmd).toContain('enclave tunnel create demo');
    expect(cmd).toContain('--provider quick');
    expect(cmd).toContain('--app web');
    expect(cmd).toContain('--gate-password-secret stage-pass');
    expect(cmd).toContain('--gate-open-path /webhooks/');
    const hcl = tofuBlock(t);
    expect(hcl).toMatch(/^resource "enclave_tunnel" "demo" \{/);
    expect(hcl).toContain('tunnel_provider = "quick"');
    expect(hcl).toContain('password_secret = "stage-pass"');
  });

  it('shows a public domain with its tunnel and Access', () => {
    const d = { ...base, kind: 'domain', name: 'stage', spec: { hostname: 'stage.galiaraa.com', app: 'web', zone: 'galiaraa', tunnel: 'home', cloudflareAccess: { emails: ['me@example.com'] } } } as Resource;
    expect(cliCommand(d)).toContain('--tunnel home');
    expect(cliCommand(d)).toContain('--access-email me@example.com');
    expect(tofuBlock(d)).toContain('tunnel = "home"');
    expect(tofuBlock(d)).toMatch(/cloudflare_access = \{[\s\S]*emails = \["me@example.com"\]/);
  });
});
