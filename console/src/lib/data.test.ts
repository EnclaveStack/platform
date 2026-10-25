import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { cliCommand, tofuBlock } from './codegen';
import { normalizePrefix, validateCDN, validateDatabase, validateEmailService, validateFirewall, validateQueue, validateSearch, validateTopic, validateTranscoder, validWebhookUrl } from './validation';

// v0.6 in the console's own logic (L3-002 §9.4, CON-44): the same rules as pkg/api, and "Use from code".

const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'default', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };
const res = (kind: Resource['kind'], name: string, spec: object) => ({ ...base, kind, name, spec }) as Resource;

describe('v0.6 validation', () => {
  it('checks engines and versions', () => {
    expect(validateDatabase({ engine: 'clickhouse', version: '26.3' })).toEqual({});
    expect(validateDatabase({ engine: 'mysql', version: '16' })['spec.version']).toMatch(/mysql version: 8\.4/);
  });
  it('checks queues, topics and webhooks', () => {
    expect(validateQueue({ deadLetterQueue: 'failed' })['spec.maxReceives']).toMatch(/dead-letter/);
    expect(validateQueue({ visibilityTimeoutSeconds: 43201 })['spec.visibilityTimeoutSeconds']).toBeDefined();
    expect(validateTopic({ subscriptions: [{ queue: 'a-q', url: 'https://x.example' }] })['spec.subscriptions[0]']).toMatch(/choose one/);
    expect(validateTopic({ subscriptions: [{ queue: 'inbox' }, { queue: 'inbox' }] })['spec.subscriptions[1]']).toMatch(/twice/);
    expect(validWebhookUrl('https://hooks.example.com/a')).toBe(true);
    expect(validWebhookUrl('http://localhost:8080/h')).toBe(true);
    expect(validWebhookUrl('http://example.com/h')).toBe(false);
  });
  it('checks search, transcoders, CDNs and email', () => {
    expect(validateSearch({ memoryMiB: 2100 })['spec.memoryMiB']).toBeDefined();
    expect(normalizePrefix('/videos')).toBe('videos/');
    expect(validateTranscoder({ input: { bucket: 'up', prefix: 'v/' }, output: { bucket: 'up', prefix: 'v/out/' }, presets: ['mp3'] })['spec.output.prefix']).toMatch(/inside the input/);
    expect(validateTranscoder({ input: { bucket: 'up' }, output: { bucket: 'other' }, presets: [] })['spec.presets']).toBeDefined();
    expect(validateCDN({ hostname: 'cdn.localhost', origin: 'bucket/uploads', originPort: 80 })['spec.originPort']).toMatch(/Only an app/);
    expect(validateCDN({ hostname: 'cdn.example.com', origin: 'vm/web' })['spec.origin']).toBeDefined();
    expect(validateEmailService({ mode: 'relay' })['spec.relay']).toBeDefined();
    expect(validateEmailService({ mode: 'relay', relay: { host: 'smtp.example.com', credentialsSecret: '' } })['spec.relay.credentialsSecret']).toBeDefined();
  });
});

describe('v0.6 firewalls', () => {
  it('protect search and email services, not queues (L3-002 §9.6)', () => {
    expect(validateFirewall({ targets: ['search/finder', 'email/shop-mail'] })).toEqual({});
    expect(validateFirewall({ targets: ['queue/emails'] })['spec.targets[0]']).toMatch(/search service or email service/);
  });
});

describe('v0.6 use from code', () => {
  it('writes every new kind for the CLI and OpenTofu', () => {
    const db = res('database', 'analytics', { engine: 'clickhouse', version: '26.3' });
    expect(cliCommand(db)).toBe('enclave db create analytics --engine clickhouse --version 26.3 --wait');
    expect(tofuBlock(db)).toContain('engine = "clickhouse"');
    const q = res('queue', 'emails', { visibilityTimeoutSeconds: 45, retentionHours: 96, maxMessageBytes: 262144, maxReceives: 5, deadLetterQueue: 'failed' });
    expect(cliCommand(q)).toBe('enclave queue create emails --visibility-timeout 45s --max-receives 5 --dead-letter-queue failed --wait');
    expect(tofuBlock(q)).toContain('dead_letter_queue = "failed"');
    const t = res('topic', 'orders', { retentionHours: 24, subscriptions: [{ queue: 'emails' }, { url: 'https://h.example/a', secret: 'k-ey' }] });
    expect(cliCommand(t)).toContain("--webhook 'https://h.example/a,secret=k-ey'");
    expect(tofuBlock(t)).toContain('{ url = "https://h.example/a", secret = "k-ey" },');
    const tc = res('transcoder', 'videos', { input: { bucket: 'uploads', prefix: 'videos/' }, output: { bucket: 'uploads', prefix: 'media/' }, presets: ['mp4-720p'], watch: false });
    expect(cliCommand(tc)).toContain('--input uploads/videos/');
    expect(cliCommand(tc)).toContain('--no-watch');
    expect(tofuBlock(tc)).toContain('watch = false');
    const cdn = res('cdn', 'assets', { hostname: 'cdn.example.com', origin: 'app/web', originPort: 8080, defaultTtlSeconds: 600, maxSizeMiB: 1024, tls: 'auto' });
    expect(cliCommand(cdn)).toContain('--origin app/web:8080');
    expect(tofuBlock(cdn)).toContain('default_ttl_seconds = 600');
    const em = res('email', 'shop-mail', { mode: 'relay', relay: { host: 'smtp.example.com', port: 465, tls: 'tls', credentialsSecret: 'smtp' }, keepMessages: 500 });
    expect(cliCommand(em)).toContain('--relay-port 465');
    expect(tofuBlock(em)).toContain('relay = { host = "smtp.example.com", credentials_secret = "smtp", port = 465, tls = "tls" }');
    expect(tofuBlock(res('search', 'catalogue', { version: '2', memoryMiB: 3072 }))).toContain('version = "2"');
  });
});
