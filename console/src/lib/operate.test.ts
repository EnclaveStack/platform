import { describe, expect, it } from 'vitest';
import type { Resource, Session } from '../api/types';
import { cliCommand, tofuBlock } from './codegen';
import { formatMetric, parseRef, validateAlert, validateChannel, validateDashboard, validateLogExport, validateSecretSource, validateSecretStore } from './operate';
import { can, isMachineAdmin, roleIn } from './roles';
import { validateSpec } from './validation';

// v0.7 rules in the console, with the server's wording (pkg/api operate.go), roles (ADR-036) and
// "Use from code" for the new kinds.

describe('operate validation (L3-002 §10.6)', () => {
  it('checks dashboards: widgets, titles, metrics the kinds have, at most 8 lines', () => {
    expect(validateDashboard({ widgets: [] })).toEqual({ 'spec.widgets': 'Use 1–24 widgets.' });
    expect(validateDashboard({ widgets: [{ title: ' ', metric: 'cpu', resources: ['app/api'] }] })).toEqual({ 'spec.widgets[0].title': 'Use a title of 1–80 characters.' });
    expect(validateDashboard({ widgets: [{ title: 'Q', metric: 'queueDepth', resources: ['app/api'] }] })).toEqual({ 'spec.widgets[0].resources[0]': 'A app has no queueDepth metric.' });
    expect(validateDashboard({ widgets: [{ title: 'M', metric: 'diskFree', resources: ['machine'] }] })).toEqual({});
    const nine = Array.from({ length: 9 }, (_, i) => `app/app-${i}`);
    expect(validateDashboard({ widgets: [{ title: 'Many', metric: 'cpu', resources: nine }] })).toEqual({ 'spec.widgets[0].resources': 'Name 1–8 resources as kind/name, like app/api.' });
  });

  it('checks alerts per condition type', () => {
    expect(validateAlert({ condition: { type: 'metric', resources: ['app/api'], metric: 'cpu', operator: 'above', threshold: 0.9 }, channels: ['ops'] })).toEqual({});
    expect(validateAlert({ condition: { type: 'metric', resources: ['app/api'], metric: 'cpu', operator: 'above' }, channels: [] })).toEqual({
      'spec.condition.threshold': 'Enter the threshold, a number.', 'spec.channels': 'Choose 1–10 channels.',
    });
    expect(validateAlert({ condition: { type: 'crash_loop', resources: ['bucket/logs'] }, channels: ['ops'] })['spec.condition.resources[0]']).toMatch(/^Choose one of: app, database/);
    expect(validateAlert({ condition: { type: 'run_failed', resources: ['job/nightly'] }, channels: ['ops', 'ops'] })).toEqual({ 'spec.channels[1]': 'ops is listed twice.' });
  });

  it('checks channels per type', () => {
    expect(validateChannel({ type: 'webhook', webhook: { url: 'http://example.com' } })).toEqual({ 'spec.webhook.url': 'Use an https:// URL (http:// only for localhost).' });
    expect(validateChannel({ type: 'telegram', secret: 'bot-token' })).toEqual({ 'spec.chatId': 'Enter the chat ID, like -1001234567890 or @channelname.' });
    expect(validateChannel({ type: 'slack' })).toEqual({ 'spec.secret': 'Name the secret holding the incoming-webhook URL.' });
    expect(validateChannel({ type: 'email', email: { service: 'mail', from: 'a@b.co', to: ['x'] } })).toEqual({ 'spec.email.to[0]': 'Enter an address like oncall@example.com.' });
  });

  it('checks log exports: one destination, a valid filter, Loki-only labels', () => {
    expect(validateLogExport({ destination: {} })).toEqual({ 'spec.destination': 'Choose one destination: a bucket, S3, HTTP, OpenTelemetry or Loki.' });
    expect(validateLogExport({ filter: '(', destination: { http: { url: 'https://l.example.com' } } })['spec.filter']).toMatch(/^That isn't a valid regular expression/);
    expect(validateLogExport({ destination: { http: { url: 'https://l.example.com', labels: { a: 'b' } } } })).toEqual({ 'spec.destination.http.labels': 'Only Loki takes labels.' });
    expect(validateLogExport({ flushSeconds: 1, destination: { loki: { url: 'https://loki.example.com', labels: { env: 'prod' } } } })).toEqual({ 'spec.flushSeconds': 'Use 5–300 seconds.' });
    expect(validateLogExport({ sources: ['bucket/logs'], destination: { otlp: { endpoint: 'https://otel.example.com' } } })['spec.sources[0]']).toMatch(/^Choose one of:/);
  });

  it('checks secret stores and sources', () => {
    expect(validateSecretStore({ provider: 'vault', vault: { address: 'https://v.example.com', tokenSecret: 'vault-token' } })).toEqual({});
    expect(validateSecretStore({ provider: 'aws', aws: { region: 'europe', credentialsSecret: 'aws-creds' } })).toEqual({ 'spec.aws.region': 'Use a region like eu-west-1.' });
    expect(validateSecretStore({ provider: 'doppler', vault: { address: 'x', tokenSecret: 'y' } })).toMatchObject({ 'spec.doppler': 'Fill in the doppler settings.', 'spec.vault': 'Only the doppler settings belong here.' });
    expect(validateSecretSource({ store: 'vault', key: '' })).toEqual({ 'spec.source.key': 'Enter the key: a path, item or secret name, up to 512 characters.' });
    expect(validateSpec('secret', { value: 'v', source: { store: 'vault', key: 'k' } })).toEqual({ 'spec.value': 'A secret has a value or a source in a secret store, not both.' });
  });

  it('reads refs and formats metric values', () => {
    expect(parseRef('app/api')).toEqual(['app', 'api']);
    expect(parseRef('app')).toBeUndefined();
    expect(formatMetric(1.5 * 2 ** 30, 'bytes')).toBe('1.5 GB');
    expect(formatMetric(0.5, 'cores')).toBe('0.50 cores');
    expect(formatMetric(12.34, 'perSecond')).toBe('12.3/s');
    expect(formatMetric(600, 'seconds')).toBe('10 min');
    expect(formatMetric(3, 'count')).toBe('3');
    expect(formatMetric(90, 'percent')).toBe('90 %');
  });
});

describe('roles in the interface (ADR-036)', () => {
  const s = (over: Partial<Session>): Session => ({ user: { id: 'u', email: 'u@x.co' }, machineName: 'm', authMethod: 'session', scope: 'full', machineRole: null, projectRoles: {}, token: null, ...over });

  it('takes the stronger of machine and project role', () => {
    expect(roleIn(s({ machineRole: 'viewer', projectRoles: { shop: 'admin' } }), 'shop')).toBe('admin');
    expect(roleIn(s({ machineRole: 'editor' }), 'shop')).toBe('editor');
    expect(roleIn(s({ projectRoles: { shop: 'viewer' } }), 'default')).toBeNull();
  });

  it("narrows by a token's role and projects", () => {
    const tok = s({ machineRole: 'admin', authMethod: 'token', token: { role: 'viewer', projects: ['shop'] } });
    expect(roleIn(tok, 'shop')).toBe('viewer');
    expect(roleIn(tok, 'default')).toBeNull();
    expect(can(tok, 'shop', 'editor')).toBe(false);
    expect(isMachineAdmin(tok)).toBe(false);
    expect(isMachineAdmin(s({ machineRole: 'admin' }))).toBe(true);
  });
});

describe('use from code, v0.7 (CON-53)', () => {
  const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'shop', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };
  const alert = { ...base, kind: 'alert', name: 'api-cpu', spec: { condition: { type: 'metric', resources: ['app/api'], metric: 'cpu', operator: 'above', threshold: 0.9, forMinutes: 5 }, channels: ['ops'], repeatMinutes: 60 } } as Resource;

  it('names the project and the condition in both forms', () => {
    expect(cliCommand(alert)).toBe('enclave --project shop alert create api-cpu \\\n  --metric cpu \\\n  --resource app/api \\\n  --above 0.9 \\\n  --channel ops \\\n  --repeat 1h \\\n  --wait');
    const hcl = tofuBlock(alert);
    expect(hcl).toContain('project = "shop"');
    expect(hcl).toContain('condition = { type = "metric", resources = ["app/api"], metric = "cpu", operator = "above", threshold = 0.9 }');
    expect(hcl).toContain('repeat_minutes = 60');
  });

  it('builds dashboards, log exports, secret stores and synced secrets the CLI and provider accept', () => {
    const dash = { ...base, project: 'default', kind: 'dashboard', name: 'health', spec: { widgets: [{ title: 'CPU', metric: 'cpu', resources: ['app/api', 'app/web'] }] } } as Resource;
    expect(cliCommand(dash)).toBe("enclave dashboard create health \\\n  --widget 'title=CPU,metric=cpu,resources=app/api+app/web' \\\n  --wait");
    const lx = { ...base, kind: 'logexport', name: 'out', spec: { flushSeconds: 10, destination: { loki: { url: 'https://loki.example.com', labels: { env: 'prod' } } } } } as Resource;
    expect(cliCommand(lx)).toContain('--loki https://loki.example.com \\\n  --label env=prod');
    expect(tofuBlock(lx)).toMatch(/^resource "enclave_log_export" "out" \{/);
    expect(tofuBlock(lx)).toContain('loki = { url = "https://loki.example.com", labels = { env = "prod" } }');
    const st = { ...base, kind: 'secretstore', name: 'vault', spec: { provider: 'vault', refreshSeconds: 300, vault: { address: 'https://v.example.com', mount: 'secret', tokenSecret: 'vault-token' } } } as Resource;
    expect(tofuBlock(st)).toContain('secret_provider = "vault"');
    expect(tofuBlock(st)).not.toContain('mount');
    const synced = { ...base, kind: 'secret', name: 'stripe', spec: { source: { store: 'vault', key: 'shop/stripe', field: 'key' } } } as Resource;
    expect(cliCommand(synced)).toBe('enclave --project shop secret create stripe --from-store vault --key shop/stripe --field key --wait');
    expect(tofuBlock(synced)).toContain('source = { store = "vault", key = "shop/stripe", field = "key" }');
  });
});
