import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { ruleRows, rulesOf, ruleSentence } from '../components/NetworkingFields';
import { cliCommand, tofuBlock } from './codegen';
import { parseCidr, validateApp, validateDomain, validateFirewall, validateLoadBalancer, validateNetwork, validateRecord, validateZone } from './validation';

// v0.4 rules in the console, held to pkg/api/networking_test.go case by case.

describe('validateNetwork (pkg/api TestNetworkValidation)', () => {
  it('accepts a private range with subnets', () => {
    expect(validateNetwork({ cidr: '10.40.0.0/16', subnets: [{ name: 'apps', cidr: '10.40.2.0/24' }] })).toEqual({});
    expect(parseCidr('10.40.0.0/16')).toEqual([0x0a280000, 16]);
  });
  it.each([
    [{ cidr: '' }, 'spec.cidr'],
    [{ cidr: '10.40.0.1/16' }, 'spec.cidr'],
    [{ cidr: '10.0.0.0/8' }, 'spec.cidr'],
    [{ cidr: '8.8.0.0/16' }, 'spec.cidr'],
    [{ cidr: '10.40.0.0/16', subnets: [{ name: 'data', cidr: '10.41.1.0/24' }] }, 'spec.subnets[0].cidr'],
    [{ cidr: '10.40.0.0/16', subnets: [{ name: 'data', cidr: '10.40.1.0/24' }, { name: 'apps', cidr: '10.40.1.128/25' }] }, 'spec.subnets[1].cidr'],
    [{ cidr: '10.40.0.0/16', subnets: [{ name: 'data', cidr: '10.40.1.0/24' }, { name: 'data', cidr: '10.40.2.0/24' }] }, 'spec.subnets[1].name'],
  ])('%j fails on %s', (spec, field) => {
    expect(Object.keys(validateNetwork(spec))).toContain(field);
  });
  it('refuses a range another network uses', () => {
    expect(validateNetwork({ cidr: '10.40.128.0/17' }, ['10.40.0.0/16'])['spec.cidr']).toMatch(/Overlaps/);
  });
});

describe('placement and replicas', () => {
  it('needs a network for a subnet and one replica when published', () => {
    expect(validateApp({ image: 'nginx:1.27', subnet: 'data' })['spec.subnet']).toBeDefined();
    expect(validateApp({ image: 'nginx:1.27', port: 80, publish: true, replicas: 2 })['spec.replicas']).toMatch(/one replica/);
    expect(validateApp({ image: 'nginx:1.27', replicas: 11 })['spec.replicas']).toBeDefined();
    expect(validateApp({ image: 'nginx:1.27', network: 'backend', replicas: 3 })).toEqual({});
  });
});

describe('validateFirewall (pkg/api TestFirewallValidation)', () => {
  it('accepts every peer form', () => {
    expect(validateFirewall({ targets: ['database/orders'], ingress: [{ from: ['app/api', '10.0.0.0/8', 'network/backend', 'any', '10.1.2.3'], ports: ['5432', '8000-8100'] }] })).toEqual({});
  });
  it.each([
    [{ targets: [] }, 'spec.targets'],
    [{ targets: ['secret/key'] }, 'spec.targets[0]'],
    [{ targets: ['app/api', 'app/api'] }, 'spec.targets[1]'],
    [{ targets: ['app/api'], ingress: [{ from: ['10.0.0.1/8'] }] }, 'spec.ingress[0].from[0]'],
    [{ targets: ['app/api'], ingress: [{ from: ['any'], protocol: 'icmp' as const, ports: ['80'] }] }, 'spec.ingress[0].ports'],
    [{ targets: ['app/api'], ingress: [{ from: ['any'], ports: ['90-80'] }] }, 'spec.ingress[0].ports[0]'],
    [{ targets: ['app/api'], egress: [{ to: ['any'] }] }, 'spec.egress'],
  ])('%j fails on %s', (spec, field) => {
    expect(Object.keys(validateFirewall(spec))).toContain(field);
  });
  it('round-trips the rule editor and reads rules as sentences', () => {
    const rules = rulesOf([{ protocol: 'tcp', ports: '5432, 8000-8100', peers: 'app/api, 10.0.0.0/8' }, { protocol: 'icmp', ports: '9', peers: 'any' }], 'from');
    expect(rules).toEqual([{ protocol: 'tcp', ports: ['5432', '8000-8100'], from: ['app/api', '10.0.0.0/8'] }, { protocol: 'icmp', from: ['any'] }]);
    expect(ruleRows(rules, 'from')[0]).toEqual({ protocol: 'tcp', ports: '5432, 8000-8100', peers: 'app/api, 10.0.0.0/8' });
    expect(ruleSentence(rules[0]!, 'from')).toBe('Allow tcp 5432, 8000-8100 from app/api, 10.0.0.0/8');
    expect(ruleSentence({ protocol: 'all', from: ['network/backend'] }, 'from')).toBe('Allow all from network/backend');
  });
});

describe('load balancers, zones, records, DNS-01 domains', () => {
  it('load balancer rules', () => {
    expect(validateLoadBalancer({ port: 80, targets: [{ app: 'api' }] })).toEqual({});
    expect(validateLoadBalancer({ port: 61999, targets: [{ app: 'api' }] })['spec.port']).toMatch(/reserved/);
    expect(validateLoadBalancer({ port: 80, targets: [{ app: 'api' }, { app: 'api' }] })['spec.targets[1].app']).toBeDefined();
    expect(validateLoadBalancer({ port: 80, targets: [{ app: 'api', weight: 300 }] })['spec.targets[0].weight']).toBeDefined();
  });
  it('zone and record rules (pkg/api TestZoneAndRecordValidation)', () => {
    expect(validateZone({ provider: 'cloudflare', zone: 'Example.com.', credentialsSecret: 'cf-token' })).toEqual({});
    for (const ok of [
      { zone: 'example', name: 'api', type: 'A' as const, machine: true },
      { zone: 'example', name: '@', type: 'A' as const, values: ['203.0.113.7'] },
      { zone: 'example', name: '*.dev', type: 'AAAA' as const, values: ['2001:db8::7'] },
      { zone: 'example', name: '_dmarc', type: 'TXT' as const, values: ['v=DMARC1; p=none'] },
    ]) expect(validateRecord(ok)).toEqual({});
    expect(validateRecord({ zone: 'example', name: 'api', type: 'A' })['spec.values']).toBeDefined();
    expect(validateRecord({ zone: 'example', name: 'api', type: 'TXT', machine: true })['spec.machine']).toBeDefined();
    expect(validateRecord({ zone: 'example', name: '@', type: 'CNAME', values: ['a.example.net'] })['spec.name']).toBeDefined();
    expect(validateRecord({ zone: 'example', name: 'api', type: 'A', values: ['2001:db8::1'] })['spec.values[0]']).toBeDefined();
  });
  it('wildcard hostnames need a zone', () => {
    expect(validateDomain({ hostname: '*.example.com', app: 'web' })['spec.hostname']).toBeDefined();
    expect(validateDomain({ hostname: '*.example.com', app: 'web', zone: 'example' })).toEqual({});
  });
});

const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'default', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };

describe('use from code for v0.4 (CON-31)', () => {
  it('networks, firewalls, load balancers, zones and records', () => {
    const net = { ...base, kind: 'network', name: 'backend', spec: { cidr: '10.40.0.0/16', subnets: [{ name: 'data', cidr: '10.40.1.0/24' }], internet: false } } as Resource;
    expect(cliCommand(net)).toBe('enclave network create backend \\\n  --cidr 10.40.0.0/16 \\\n  --subnet data=10.40.1.0/24 \\\n  --no-internet \\\n  --wait');
    expect(tofuBlock(net)).toContain('subnet = [\n    { name = "data", cidr = "10.40.1.0/24" },\n  ]');
    const fw = { ...base, kind: 'firewall', name: 'db-access', spec: { targets: ['database/orders'], ingress: [{ from: ['app/api'], protocol: 'tcp', ports: ['5432'] }], egressDefault: 'allow' } } as Resource;
    expect(cliCommand(fw)).toContain(`--allow 'tcp 5432 from app/api'`);
    expect(tofuBlock(fw)).toContain('ingress = [\n    { from = ["app/api"], ports = ["5432"] },\n  ]');
    const lb = { ...base, kind: 'loadbalancer', name: 'api-lb', spec: { port: 80, protocol: 'http', algorithm: 'least_conn', targets: [{ app: 'api', port: 8080, weight: 3 }], publish: true } } as Resource;
    expect(cliCommand(lb)).toContain('--target api:8080@3');
    expect(tofuBlock(lb)).toMatch(/^resource "enclave_load_balancer" "api_lb"/);
    const zone = { ...base, kind: 'zone', name: 'example', spec: { provider: 'cloudflare', zone: 'example.com', credentialsSecret: 'cf-token' } } as Resource;
    expect(tofuBlock(zone)).toContain('dns_provider = "cloudflare"');
    const rec = { ...base, kind: 'record', name: 'api', spec: { zone: 'example', name: '@', type: 'A', machine: true, ttl: 300 } } as Resource;
    expect(cliCommand(rec)).toContain('--name @ \\\n');
    expect(cliCommand({ ...rec, spec: { zone: 'example', name: '*.dev', type: 'A', machine: true } } as Resource)).toContain("--name '*.dev'");
    expect(tofuBlock(rec)).toContain('record_name = "@"');
    const app = { ...base, kind: 'app', name: 'api', spec: { image: 'nginx:1.27', network: 'backend', subnet: 'apps', replicas: 3 } } as Resource;
    expect(cliCommand(app)).toContain('--replicas 3');
    expect(cliCommand(app)).toContain('--network backend --subnet apps');
    expect(tofuBlock(app)).toContain('  replicas = 3\n  network = "backend"\n  subnet = "apps"');
  });
});
