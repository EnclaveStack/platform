import { describe, expect, it } from 'vitest';
import type { Resource } from '../api/types';
import { cliCommand, tofuBlock } from './codegen';

const base: Omit<Resource, 'kind' | 'name' | 'spec'> = { id: 'x', project: 'default', generation: 1, ownerId: null, stack: null, status: { phase: 'ready', message: '', observedGeneration: 1, attempts: 0, steps: [], lastTransitionAt: '' }, outputs: {}, createdAt: '', updatedAt: '' };

describe('use from code', () => {
  const app = { ...base, kind: 'app', name: 'api-web', spec: { image: 'ghcr.io/acme/api:1.4', port: 8080, publish: true, memoryMiB: 512, env: { LOG_LEVEL: 'info', GREETING: "it's on" }, secretEnv: { STRIPE_KEY: 'stripe-key' } } } as Resource;

  it('builds a runnable CLI command with every setting and quoting', () => {
    const cmd = cliCommand(app);
    expect(cmd).toContain('enclave app create api-web');
    expect(cmd).toContain('--image ghcr.io/acme/api:1.4');
    expect(cmd).toContain('--port 8080');
    expect(cmd).toContain('--publish');
    expect(cmd).toContain('--memory 512');
    expect(cmd).toContain("--env 'GREETING=it'\\''s on'");
    expect(cmd).toContain('--secret-env STRIPE_KEY=stripe-key');
  });

  it('builds a matching OpenTofu block', () => {
    const hcl = tofuBlock(app);
    expect(hcl).toMatch(/^resource "enclave_app" "api_web" \{/);
    expect(hcl).toContain('image = "ghcr.io/acme/api:1.4"');
    expect(hcl).toContain('memory_mib = 512');
    expect(hcl).toContain('STRIPE_KEY = "stripe-key"');
  });

  it('never includes a secret value', () => {
    const sec = { ...base, kind: 'secret', name: 'stripe-key', spec: {} } as Resource;
    expect(cliCommand(sec)).toContain('--from-stdin');
    expect(tofuBlock(sec)).toContain('var.stripe_key');
  });

  it('covers databases, caches and buckets', () => {
    expect(cliCommand({ ...base, kind: 'database', name: 'orders', spec: { engine: 'postgres', version: '17' } } as Resource)).toBe('enclave db create orders --version 17 --wait');
    expect(tofuBlock({ ...base, kind: 'cache', name: 'c1x', spec: { engine: 'redis', version: '7', maxMemoryMiB: 64 } } as Resource)).toContain('max_memory_mib = 64');
    expect(cliCommand({ ...base, kind: 'bucket', name: 'media', spec: { versioning: true } } as Resource)).toContain('--versioning');
  });

  it('carries strategy and health check into both snippets', () => {
    const r = { ...base, kind: 'app', name: 'web', spec: { image: 'nginx', port: 80, strategy: 'recreate',
      healthCheck: { type: 'http', path: '/healthz', intervalSeconds: 2, timeoutSeconds: 2, retries: 5 } } } as Resource;
    const cmd = cliCommand(r);
    expect(cmd).toContain('--strategy recreate');
    expect(cmd).toContain('--health http:/healthz');
    expect(cmd).toContain('--health-interval 2');
    expect(cmd).toContain('--health-retries 5');
    expect(cmd).not.toContain('--health-timeout');
    const hcl = tofuBlock(r);
    expect(hcl).toContain('strategy = "recreate"');
    expect(hcl).toContain('health_check = {');
    expect(hcl).toContain('path = "/healthz"');
    expect(hcl).toContain('interval_seconds = 2');
  });

  it('builds domain snippets without repeating defaults', () => {
    const local = { ...base, kind: 'domain', name: 'web-local', spec: { hostname: 'web.localhost', app: 'web', tls: 'internal', redirectHttp: true } } as Resource;
    expect(cliCommand(local)).toBe('enclave domain create web-local \\\n  --hostname web.localhost \\\n  --app web \\\n  --wait');
    expect(tofuBlock(local)).toBe('resource "enclave_domain" "web_local" {\n  name = "web-local"\n  hostname = "web.localhost"\n  app = "web"\n}');
    const pub = { ...base, kind: 'domain', name: 'api', spec: { hostname: 'api.example.com', app: 'api', port: 8080, tls: 'off', redirectHttp: false } } as Resource;
    expect(cliCommand(pub)).toContain('--port 8080');
    expect(cliCommand(pub)).toContain('--tls off');
    expect(cliCommand(pub)).toContain('--no-redirect');
    expect(tofuBlock(pub)).toContain('tls = "off"');
    expect(tofuBlock(pub)).toContain('redirect_http = false');
  });

  it('builds job snippets with the command last', () => {
    const job = { ...base, kind: 'job', name: 'cleanup', spec: { schedule: '0 3 * * *', timeZone: 'Asia/Kolkata', app: 'api', command: ['./bin/cleanup', '--older-than', '7d'],
      timeoutSeconds: 3600, overlap: 'forbid', memoryMiB: 256 } } as Resource;
    const cmd = cliCommand(job);
    expect(cmd).toContain("--schedule '0 3 * * *'");
    expect(cmd).toContain('--time-zone Asia/Kolkata');
    expect(cmd).toContain('--app api');
    expect(cmd).not.toContain('--run-timeout');
    expect(cmd.trimEnd().endsWith('-- ./bin/cleanup --older-than 7d')).toBe(true);
    const hcl = tofuBlock(job);
    expect(hcl).toContain('schedule = "0 3 * * *"');
    expect(hcl).toContain('command = ["./bin/cleanup", "--older-than", "7d"]');
    expect(hcl).not.toContain('timeout_seconds');
  });

  it('builds snippets for an app from Git (CLI-17, TF-15)', () => {
    const app = { ...base, kind: 'app', name: 'site', spec: { source: { git: 'https://github.com/acme/site.git', ref: 'main', dockerfile: 'Dockerfile', context: 'web',
      credentialsSecret: 'git-token', buildArgs: { MODE: 'prod' } }, port: 8080, memoryMiB: 256, strategy: 'rolling' } } as Resource;
    const cmd = cliCommand(app);
    for (const part of ['--git https://github.com/acme/site.git', '--ref main', '--context web', '--git-credentials git-token', '--build-arg MODE=prod', '--port 8080'])
      expect(cmd).toContain(part);
    expect(cmd).not.toContain('--image');
    expect(cmd).not.toContain('--dockerfile');
    const hcl = tofuBlock(app);
    expect(hcl).toContain('source = {\n    git = "https://github.com/acme/site.git"\n    ref = "main"\n    context = "web"\n    credentials_secret = "git-token"\n    build_args = { MODE = "prod" }\n  }');
    expect(hcl).not.toContain('image =');
  });

  it('builds backup snippets: defaults left out, S3 as a block, source as an object (TF-14)', () => {
    const local = { ...base, kind: 'backup', name: 'nightly', spec: { source: { database: 'shop' }, destination: { bucket: 'vault' }, prefix: 'enclave-backups',
      schedule: '0 3 * * *', keep: 7, keepDays: 0, timeoutSeconds: 7200 } } as Resource;
    expect(cliCommand(local)).toBe('enclave backup create nightly \\\n  --source database/shop \\\n  --bucket vault \\\n  --wait');
    expect(tofuBlock(local)).toBe('resource "enclave_backup" "nightly" {\n  name = "nightly"\n  source = { database = "shop" }\n  bucket = "vault"\n}');
    const offsite = { ...base, kind: 'backup', name: 'offsite', spec: { source: { cache: 'sessions' }, destination: { s3: { endpoint: 'https://s3.eu-central-1.amazonaws.com',
      region: 'eu-central-1', bucket: 'acme', credentialsSecret: 'offsite-s3' } }, schedule: '@hourly', timeZone: 'Asia/Kolkata', keep: 30, keepDays: 90, timeoutSeconds: 7200 } } as Resource;
    const cmd = cliCommand(offsite);
    for (const part of ['--source cache/sessions', '--s3-endpoint https://s3.eu-central-1.amazonaws.com', '--s3-region eu-central-1', '--s3-bucket acme',
      '--s3-credentials offsite-s3', '--schedule @hourly', '--time-zone Asia/Kolkata', '--keep 30', '--keep-days 90']) expect(cmd).toContain(part);
    expect(cmd).not.toContain('--run-timeout');
    const hcl = tofuBlock(offsite);
    expect(hcl).toContain('source = { cache = "sessions" }');
    expect(hcl).toContain('s3 = {\n    endpoint = "https://s3.eu-central-1.amazonaws.com"\n    region = "eu-central-1"\n    bucket = "acme"\n    credentials_secret = "offsite-s3"\n  }');
    expect(hcl).toContain('keep_days = 90');
  });
});
