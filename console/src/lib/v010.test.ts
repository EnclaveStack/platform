import { describe, expect, it } from 'vitest';
import { RULES } from '../api/generated';
import { previewName } from '../api/mock';
import type { AlertSpec, AppSpec, BackupSpec, DatabaseSpec, FunctionSpec, Resource, TopicSpec } from '../api/types';
import { cliCommand, tofuBlock } from './codegen';
import { validateAlert } from './operate';
import { validateApp, validateBackup, validateDatabase, validateFunction, validateTopic } from './validation';

// The same cases as pkg/api/v010_test.go: the console agrees with the server on v0.10 (ADR-002).

const fields = (errs: Record<string, string>) => Object.keys(errs).sort().join(',');

describe('validateFunction (FN-01 – FN-04)', () => {
  it('accepts an image with one trigger of each type', () => {
    expect(validateFunction({ image: 'alpine:3.22' })).toEqual({});
    expect(validateFunction({ image: 'alpine:3.22', triggers: [{ schedule: '@daily', timeZone: 'Europe/Berlin' }, { queue: 'jobs' }, { bucket: 'uploads', prefix: 'in/' }] })).toEqual({});
  });
  it.each<[FunctionSpec, string]>([
    [{ image: 'alpine:3.22', timeoutSeconds: 901 }, 'spec.timeoutSeconds'],
    [{ image: 'alpine:3.22', concurrency: 21 }, 'spec.concurrency'],
    [{ image: 'alpine:3.22', memoryMiB: 9000 }, 'spec.memoryMiB'],
    [{ image: 'alpine:3.22', env: { ENCLAVE_RUN_ID: 'x' } }, 'spec.env.ENCLAVE_RUN_ID'],
    [{ image: 'alpine:3.22', triggers: [{}] }, 'spec.triggers[0]'],
    [{ image: 'alpine:3.22', triggers: [{ queue: 'a', bucket: 'b' }] }, 'spec.triggers[0]'],
    [{ image: 'alpine:3.22', triggers: [{ schedule: '@daily' }, { schedule: '@hourly' }] }, 'spec.triggers[1].schedule'],
    [{ image: 'alpine:3.22', triggers: [{ schedule: 'every day' }] }, 'spec.triggers[0].schedule'],
    [{ image: 'alpine:3.22', triggers: [{ queue: 'jobs', timeZone: 'UTC' }] }, 'spec.triggers[0].timeZone'],
    [{ image: 'alpine:3.22', triggers: [{ queue: 'jobs' }, { queue: 'jobs' }] }, 'spec.triggers[1].queue'],
    [{ image: 'alpine:3.22', triggers: [{ bucket: 'uploads', prefix: '../x/' }] }, 'spec.triggers[0].prefix'],
    [{ image: 'alpine:3.22', triggers: [{ queue: 'jobs', prefix: 'a/' }] }, 'spec.triggers[0].prefix'],
    [{ image: 'alpine:3.22', triggers: [{ bucket: 'uploads' }, { bucket: 'uploads' }] }, 'spec.triggers[1]'],
  ])('refuses %j at %s', (spec, field) => expect(fields(validateFunction(spec))).toBe(field));

  it('lets a topic send to a function, alone', () => {
    expect(validateTopic({ subscriptions: [{ function: 'resize' }] } as TopicSpec)).toEqual({});
    expect(fields(validateTopic({ subscriptions: [{ function: 'resize', queue: 'q' }] } as TopicSpec))).toBe('spec.subscriptions[0]');
    expect(fields(validateTopic({ subscriptions: [{ function: 'resize', secret: 's' }] } as TopicSpec))).toBe('spec.subscriptions[0].secret');
  });
});

describe('previews (PRV-01 – PRV-04)', () => {
  const source = { git: 'https://github.com/acme/web.git', ref: 'main', webhookSecret: 'web-hook', deployOnPush: true };
  it('accepts a Git app with previews and a domain', () => {
    expect(validateApp({ source, previews: { enabled: true, domain: { suffix: 'preview.example.com' } } })).toEqual({});
  });
  it.each<[AppSpec, string]>([
    [{ image: 'nginx:1.27', previews: { enabled: true } }, 'spec.previews'],
    [{ source, previews: { enabled: true, ttlHours: 721 } }, 'spec.previews.ttlHours'],
    [{ source, previews: { enabled: true, maxActive: 21 } }, 'spec.previews.maxActive'],
    [{ source, previews: { enabled: true, domain: { suffix: '*.x.com' } } }, 'spec.previews.domain.suffix'],
    [{ source: { ...source, webhookSecret: 'Bad Name' } }, 'spec.source.webhookSecret'],
  ])('refuses %j at %s', (spec, field) => expect(fields(validateApp(spec))).toBe(field));

  it('names previews as the server does (PreviewNameForRef)', () => {
    expect(previewName('web', 'refs/heads/Feature/Login_Page')).toBe('web-feature-login-page');
    expect(previewName('web', '###')).toBe('web-preview');
  });
});

describe('database options, volume backups and event alerts', () => {
  it('needs TLS before exposed, TLS only on postgres and mysql, and a short setup script (DBX-*)', () => {
    expect(validateDatabase({ engine: 'postgres', version: '16', tls: true, exposed: true, initScript: 'CREATE TABLE t (id int);' })).toEqual({});
    expect(fields(validateDatabase({ engine: 'mongodb', version: '7.0', tls: true }))).toBe('spec.tls');
    expect(fields(validateDatabase({ engine: 'postgres', version: '16', exposed: true }))).toBe('spec.exposed');
    expect(fields(validateDatabase({ engine: 'postgres', version: '16', initScript: 'x'.repeat(RULES.initScriptMax + 1) } as DatabaseSpec))).toBe('spec.initScript');
  });
  it('backs up a volume, from one source only (VBK-01)', () => {
    expect(validateBackup({ source: { volume: 'files' }, destination: { bucket: 'backups' } })).toEqual({});
    expect(fields(validateBackup({ source: { volume: 'files', database: 'db' }, destination: { bucket: 'backups' } } as BackupSpec))).toBe('spec.source');
  });
  it.each<[AlertSpec, string]>([
    [{ condition: { type: 'event', resources: ['app/web'], events: ['exploded'] }, channels: ['ops'] }, 'spec.condition.events[0]'],
    [{ condition: { type: 'event', resources: ['app/web'], events: [] }, channels: ['ops'] }, 'spec.condition.events'],
    [{ condition: { type: 'event', resources: ['app/web'], events: ['ready', 'ready'] }, channels: ['ops'] }, 'spec.condition.events[1]'],
    [{ condition: { type: 'event', resources: ['app/web'], events: ['ready'] }, channels: ['ops'], repeatMinutes: 5 }, 'spec.repeatMinutes'],
    [{ condition: { type: 'failed', resources: ['app/web'], events: ['ready'] }, channels: ['ops'] }, 'spec.condition.events'],
  ])('refuses the event alert %j at %s (EVN-01)', (spec, field) => expect(fields(validateAlert(spec))).toBe(field));
  it('accepts an event alert', () => {
    expect(validateAlert({ condition: { type: 'event', resources: ['app/web'], events: ['ready', 'failed'] }, channels: ['ops'] })).toEqual({});
  });
});

const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'default', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };

describe('use from code, v0.10', () => {
  it('builds function snippets with triggers', () => {
    const fn = { ...base, kind: 'function', name: 'thumbs', spec: { image: 'ghcr.io/acme/thumbs:1', command: ['./run'], timeoutSeconds: 120, triggers: [{ bucket: 'uploads', prefix: 'in/' }, { schedule: '@daily' }] } } as Resource;
    const cmd = cliCommand(fn);
    expect(cmd).toContain('enclave function create thumbs');
    expect(cmd).toContain('--image ghcr.io/acme/thumbs:1');
    expect(cmd).toContain('--run-timeout 120s');
    expect(cmd).toMatch(/-- \.\/run$/);
    const hcl = tofuBlock(fn);
    expect(hcl).toContain('resource "enclave_function" "thumbs"');
    expect(hcl).toContain('bucket = "uploads"');
    expect(hcl).toContain('schedule = "@daily"');
  });
  it('carries database options, previews and the webhook into both snippets', () => {
    const db = { ...base, kind: 'database', name: 'shop', spec: { engine: 'postgres', version: '16', tls: true, exposed: true } } as Resource;
    expect(cliCommand(db)).toMatch(/--tls[\s\\]+--exposed/);
    expect(tofuBlock(db)).toContain('tls = true');
    const app = { ...base, kind: 'app', name: 'web', spec: { source: { git: 'https://github.com/acme/web.git', ref: 'main', webhookSecret: 'web-hook', deployOnPush: true }, previews: { enabled: true, ttlHours: 24 } } } as Resource;
    expect(cliCommand(app)).toContain('--webhook-secret web-hook');
    expect(cliCommand(app)).toContain('--deploy-on-push');
    expect(cliCommand(app)).toContain('--previews');
    expect(tofuBlock(app)).toContain('previews = {');
  });
});
