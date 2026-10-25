import { describe, expect, it } from 'vitest';
import { describeHealthCheck, validateApp, validateCache, validateDomain, validateBackup, validateGitSource, validateHealthCheck, validateJob, validateDatabase, validateEmail, validateName, validatePassword, validateSecret } from './validation';

// The same cases as pkg/api/validation_test.go: the console must agree with the server (ADR-002).

describe('validateName', () => {
  it.each(['abc', 'orders', 'orders-db', 'a1-b2', 'a'.repeat(40)])('accepts %s', (n) => expect(validateName(n)).toBeUndefined());
  it.each(['', 'ab', 'a'.repeat(41), 'Orders', '1abc', 'abc-', 'a_b_c', 'ab c'])('rejects %j', (n) => expect(validateName(n)).toBeDefined());
});

describe('validateApp', () => {
  it('accepts a minimal app and real-world image references', () => {
    for (const image of ['nginx', 'nginx:1.27-alpine', 'ghcr.io/acme/api:1.4', 'localhost:5000/team/app', 'redis@sha256:' + 'a'.repeat(64)]) {
      expect(validateApp({ image })).toEqual({});
    }
  });
  it('reports each broken rule on its field', () => {
    expect(validateApp({ image: '' })['spec.image']).toMatch(/Enter an image/);
    expect(validateApp({ image: 'NGINX' })['spec.image']).toMatch(/image reference/);
    expect(validateApp({ image: 'nginx', port: 70000 })['spec.port']).toBeDefined();
    expect(validateApp({ image: 'nginx', publish: true })['spec.port']).toMatch(/publish/);
    expect(validateApp({ image: 'nginx', memoryMiB: 32 })['spec.memoryMiB']).toBeDefined();
    expect(validateApp({ image: 'nginx', env: { '1BAD': 'x' } })['spec.env.1BAD']).toBeDefined();
    expect(validateApp({ image: 'nginx', env: { A: 'x' }, secretEnv: { A: 'sec' } })['spec.secretEnv.A']).toMatch(/already set/);
    expect(validateApp({ image: 'nginx', secretEnv: { K: 'Not_A_Name' } })['spec.secretEnv.K']).toBeDefined();
  });
});

describe('other kinds', () => {
  it('databases accept 16 and 17 only', () => {
    expect(validateDatabase({ engine: 'postgres', version: '16' })).toEqual({});
    expect(validateDatabase({ engine: 'postgres', version: '15' })['spec.version']).toBeDefined();
  });
  it('caches bound max memory', () => {
    expect(validateCache({ engine: 'redis', version: '7', maxMemoryMiB: 128 })).toEqual({});
    expect(validateCache({ engine: 'redis', version: '7', maxMemoryMiB: 5000 })['spec.maxMemoryMiB']).toBeDefined();
  });
  it('secrets need a value up to 64 KiB (counted in bytes)', () => {
    expect(validateSecret({ value: 'x' })).toEqual({});
    expect(validateSecret({})['spec.value']).toMatch(/Enter/);
    expect(validateSecret({ value: '€'.repeat(22000) })['spec.value']).toMatch(/64 KiB/);
  });
  it('setup fields', () => {
    expect(validateEmail('a@b.c')).toBeUndefined();
    expect(validateEmail('nope')).toBeDefined();
    expect(validatePassword('123456789012')).toBeUndefined();
    expect(validatePassword('short')).toBeDefined();
  });
});

describe('validateHealthCheck (pkg/api TestNormalizeHealthCheckAndStrategy)', () => {
  it('accepts valid checks with server defaults', () => {
    expect(validateHealthCheck({ type: 'http' }, 80)).toEqual({});
    expect(validateHealthCheck({ type: 'tcp', port: 8080 })).toEqual({});
    expect(validateHealthCheck({ type: 'command', command: ['pg_isready'] })).toEqual({});
    expect(validateHealthCheck({ type: 'none' })).toEqual({});
  });
  it.each([
    [{ type: 'tcp' as const }, undefined, 'spec.healthCheck.port'],
    [{ type: 'http' as const, path: 'healthz' }, 80, 'spec.healthCheck.path'],
    [{ type: 'command' as const }, undefined, 'spec.healthCheck.command'],
    [{ type: 'tcp' as const, intervalSeconds: 301 }, 80, 'spec.healthCheck.intervalSeconds'],
    [{ type: 'tcp' as const, intervalSeconds: 2, timeoutSeconds: 5 }, 80, 'spec.healthCheck.timeoutSeconds'],
    [{ type: 'tcp' as const, retries: 101 }, 80, 'spec.healthCheck.retries'],
    [{ type: 'tcp' as const, startPeriodSeconds: 601 }, 80, 'spec.healthCheck.startPeriodSeconds'],
    [{ type: 'tcp' as const, intervalSeconds: 60, retries: 20 }, 80, 'spec.healthCheck.retries'],
  ])('rejects %j (port %s) on %s', (h, port, field) => expect(validateHealthCheck(h, port)).toHaveProperty([field]));
  it('flags an unknown strategy on the app', () => {
    expect(validateApp({ image: 'nginx', strategy: 'blue-green' as never })).toHaveProperty(['spec.strategy']);
  });
  it('names checks in words', () => {
    expect(describeHealthCheck(undefined, 80)).toBe('Default: TCP on port 80');
    expect(describeHealthCheck({ type: 'http', path: '/healthz', intervalSeconds: 5, retries: 12 })).toBe('HTTP /healthz every 5 s, up to 12 times');
  });
});

describe('validateDomain (pkg/api ValidateHostname)', () => {
  it.each(['api.example.com', 'web.localhost', 'a-b.c-d.io', 'API.Example.COM.'])('accepts %s', (h) => expect(validateDomain({ hostname: h, app: 'web' })).toEqual({}));
  it.each([['', 'spec.hostname'], ['intranet', 'spec.hostname'], ['10.0.0.1', 'spec.hostname'], ['*.example.com', 'spec.hostname'], ['-bad.example.com', 'spec.hostname']])(
    'rejects %j', (h, field) => expect(validateDomain({ hostname: h, app: 'web' })).toHaveProperty([field]));
  it('needs an app, a valid port, and no public certificate for .localhost', () => {
    expect(validateDomain({ hostname: 'a.example.com', app: '' })).toHaveProperty(['spec.app']);
    expect(validateDomain({ hostname: 'a.example.com', app: 'web', port: 70000 })).toHaveProperty(['spec.port']);
    expect(validateDomain({ hostname: 'a.localhost', app: 'web', tls: 'auto' })).toHaveProperty(['spec.tls']);
  });
});

describe('validateGitSource and apps from Git (pkg/api validateGitSource)', () => {
  it('accepts https, ssh and local http repositories', () => {
    expect(validateApp({ source: { git: 'https://github.com/acme/api.git', ref: 'main' } })).toEqual({});
    expect(validateGitSource({ git: 'ssh://git@github.com/acme/api.git', ref: 'v1.2.0', dockerfile: 'docker/Dockerfile', context: 'services/api', buildArgs: { NODE_ENV: 'production' } })).toEqual({});
    expect(validateGitSource({ git: 'http://localhost:3000/acme/api.git', ref: 'main' })).toEqual({});
  });
  it('wants exactly one of image and source', () => {
    expect(validateApp({ image: 'nginx', source: { git: 'https://github.com/a/b.git', ref: 'main' } })).toHaveProperty(['spec.image']);
    expect(validateApp({})).toHaveProperty(['spec.image']);
  });
  it.each([
    [{ git: 'http://github.com/acme/api.git', ref: 'main' }, 'spec.source.git'],
    [{ git: 'github.com/acme/api', ref: 'main' }, 'spec.source.git'],
    [{ git: 'https://github.com/acme/api.git', ref: '' }, 'spec.source.ref'],
    [{ git: 'https://github.com/acme/api.git', ref: 'feature x' }, 'spec.source.ref'],
    [{ git: 'https://github.com/acme/api.git', ref: 'main', dockerfile: '../Dockerfile' }, 'spec.source.dockerfile'],
    [{ git: 'https://github.com/acme/api.git', ref: 'main', context: '/etc' }, 'spec.source.context'],
    [{ git: 'https://github.com/acme/api.git', ref: 'main', credentialsSecret: 'Bad Name' }, 'spec.source.credentialsSecret'],
    [{ git: 'https://github.com/acme/api.git', ref: 'main', buildArgs: { '1X': 'y' } }, 'spec.source.buildArgs.1X'],
  ])('rejects %j on %s', (g, field) => expect(validateGitSource(g)).toHaveProperty([field]));
});

describe('validateBackup (pkg/api validateBackup)', () => {
  const s3 = { endpoint: 'https://s3.eu-central-1.amazonaws.com', bucket: 'acme-backups', credentialsSecret: 'offsite' };
  it('accepts a bucket backup with defaults and an S3 backup', () => {
    expect(validateBackup({ source: { database: 'shop' }, destination: { bucket: 'vault' } })).toEqual({});
    expect(validateBackup({ source: { cache: 'sessions' }, destination: { s3 }, schedule: '@hourly', keep: 30, keepDays: 90, timeZone: 'Asia/Kolkata' })).toEqual({});
    expect(validateBackup({ source: { cache: 'sessions' }, destination: { s3: { ...s3, endpoint: 'http://localhost:9000' } } })).toEqual({});
  });
  it.each([
    [{ source: {}, destination: { bucket: 'vault' } }, 'spec.source'],
    [{ source: { database: 'a-db', cache: 'c' }, destination: { bucket: 'vault' } }, 'spec.source'],
    [{ source: { database: 'Bad_Name' }, destination: { bucket: 'vault' } }, 'spec.source'],
    [{ source: { database: 'shop' }, destination: {} }, 'spec.destination'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault', s3 } }, 'spec.destination'],
    [{ source: { database: 'shop' }, destination: { s3: { ...s3, endpoint: 'http://s3.example.com' } } }, 'spec.destination.s3.endpoint'],
    [{ source: { database: 'shop' }, destination: { s3: { ...s3, endpoint: 'https://s3.example.com/path' } } }, 'spec.destination.s3.endpoint'],
    [{ source: { database: 'shop' }, destination: { s3: { ...s3, bucket: 'UP' } } }, 'spec.destination.s3.bucket'],
    [{ source: { database: 'shop' }, destination: { s3: { ...s3, credentialsSecret: '' } } }, 'spec.destination.s3.credentialsSecret'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, prefix: 'a/../b' }, 'spec.prefix'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, prefix: '/abs' }, 'spec.prefix'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, schedule: '@every 1h' }, 'spec.schedule'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, keep: 0 }, 'spec.keep'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, keepDays: 4000 }, 'spec.keepDays'],
    [{ source: { database: 'shop' }, destination: { bucket: 'vault' }, timeoutSeconds: 30 }, 'spec.timeoutSeconds'],
  ])('rejects %j on %s', (spec, field) => expect(validateBackup(spec)).toHaveProperty([field]));
});

describe('validateJob (pkg/api validateJob)', () => {
  it('accepts an app job and an image job', () => {
    expect(validateJob({ schedule: '@daily', app: 'api' })).toEqual({});
    expect(validateJob({ schedule: '0 3 * * *', timeZone: 'Asia/Kolkata', image: 'busybox:1.37', timeoutSeconds: 60 })).toEqual({});
  });
  it.each([
    [{ schedule: '' , image: 'busybox' }, 'spec.schedule'],
    [{ schedule: '@every 5m', image: 'busybox' }, 'spec.schedule'],
    [{ schedule: '@daily', image: 'busybox', timeZone: 'Mars/Olympus' }, 'spec.timeZone'],
    [{ schedule: '@daily' }, 'spec.app'],
    [{ schedule: '@daily', app: 'api', image: 'busybox' }, 'spec.app'],
    [{ schedule: '@daily', image: 'NOPE' }, 'spec.image'],
    [{ schedule: '@daily', image: 'busybox', timeoutSeconds: 90000 }, 'spec.timeoutSeconds'],
    [{ schedule: '@daily', image: 'busybox', env: { '1BAD': 'x' } }, 'spec.env.1BAD'],
  ])('rejects %j on %s', (spec, field) => expect(validateJob(spec)).toHaveProperty([field]));
});
