import type { EnclaveApi } from './client';
import { mockMetrics, normalizeOperate, operateOutputs } from './mock-operate';
import { ID_PREFIX, MANAGED, ROLES, RULES, STEP_NAMES } from './generated';
import {
  ApiError,
  type AppSpec,
  type BackupSpec,
  type GitSource,
  type CreatedToken,
  type DatabaseSpec,
  type DomainSpec,
  type FirewallRule,
  type FirewallSpec,
  type JobSpec,
  type LoadBalancerCheck,
  type LoadBalancerSpec,
  type LoadBalancerTarget,
  type NetworkSpec,
  type RecordSpec,
  type SnapshotSpec,
  type CDNSpec,
  type Email,
  type EmailSpec,
  type QueueSpec,
  type ReceivedMessage,
  type SearchSpec,
  type TopicSpec,
  type TranscoderSpec,
  type VMSpec,
  type VolumeSpec,
  type ZoneSpec,
  type Event,
  type FieldError,
  type Kind,
  type LogLine,
  type Machine,
  type Phase,
  type Resource,
  type Run,
  type RunDetail,
  type Session,
  type SpecByKind,
  type Token,
  type AlertSpec,
  type AuditEntry,
  type ChannelSpec,
  type DashboardSpec,
  type Incident,
  type Invitation,
  type LogExportSpec,
  type NotificationRecord,
  type Project,
  type Role,
  type SecretSource,
  type SecretStoreSpec,
  type TestResult,
  type UserSummary,
  type InviteEmail,
} from './types';
import { can, roleIn } from '../lib/roles';
import { nextRuns } from '../lib/schedule';
import { normalizePrefix, validateEmail, validateName, validateSpec } from '../lib/validation';

// In-memory stand-in for the control plane (ADR-002), used by UI tests and offline demos. It follows
// L3-002: same shapes, same error codes, same phase transitions, same managed-resource rules.

export const DEMO_ADMIN = { email: 'admin@enclave.local', password: 'enclave-demo', machineName: 'This Mac' } as const;
export const DEMO_SETUP_CODE = 'DEMO-SETU-PCOD';

export interface MockOptions {
  latencyMs?: number;
  /** Time each provisioning step takes. */
  stepMs?: number;
  /** Start before first-run setup. */
  setupRequired?: boolean;
  /** Pre-populate resources ("demo"), or start empty. */
  seed?: 'demo' | 'empty';
  /** Persist sign-in across reloads (sessionStorage); null keeps it in memory (tests). */
  storage?: Storage | null;
  /** Simulate a machine without QEMU (VM-11). */
  noVMs?: boolean;
  /** The signed-in person's roles (ADR-036); machine admin when left out. */
  as?: { machineRole: Role | null; projectRoles?: Record<string, Role> };
}

interface Stored {
  r: Resource;
  secret?: string;
  events: Event[];
  timer?: ReturnType<typeof setTimeout>;
  /** A source app's latest build log. */
  buildLog?: string[];
  /** Job, backup and transcoder runs, newest first. */
  runs?: RunDetail[];
  /** A queue's messages, oldest first (QUE-*). */
  messages?: (ReceivedMessage & { invisibleUntil: number })[];
  /** A topic's message count and per-subscription counters (TOP-07). */
  published?: number;
  deliveries?: Record<string, number>;
  /** An email service's kept messages, newest last (EML-03). */
  emails?: Email[];
}

interface Admin {
  email: string;
  password: string;
  machineName: string;
}

const STORAGE_KEY = 'enclave.mock.v2';
const MOCK_CA = '-----BEGIN CERTIFICATE-----\nbW9jayBob21lY2xvdWQgcm9vdA==\n-----END CERTIFICATE-----\n';

let idCounter = 0;
function newId(kind: Kind): string {
  idCounter++;
  return `${ID_PREFIX[kind]}_${(Date.now().toString(32) + idCounter.toString(32)).padStart(16, 'a').slice(-16)}`;
}

/** A run without its output, as listings return it. */
function toRun(r: RunDetail): Run {
  return { id: r.id, trigger: r.trigger, status: r.status, scheduledFor: r.scheduledFor, startedAt: r.startedAt, finishedAt: r.finishedAt,
    exitCode: r.exitCode, message: r.message, ...(r.skippedCount ? { skippedCount: r.skippedCount } : {}), ...(r.artifact ? { artifact: { ...r.artifact } } : {}),
    ...(r.key ? { key: r.key } : {}), ...(r.outputs ? { outputs: [...r.outputs] } : {}) };
}

/** A stable 40-hex "commit" for a string (FNV-1a, repeated). */
export function fakeCommit(seed: string): string {
  let out = '';
  let h = 0x811c9dc5;
  for (let round = 0; out.length < 40; round++) {
    for (const ch of seed + round) h = Math.imul(h ^ ch.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, '0');
  }
  return out.slice(0, 40);
}

function mockBuildLog(src: GitSource, failing: boolean): string[] {
  const lines = [`#1 [internal] load build definition from ${src.dockerfile ?? RULES.gitDockerfileDefault}`, '#2 [1/4] FROM docker.io/library/node:22-alpine',
    '#3 [2/4] COPY package*.json ./', '#4 [3/4] RUN npm ci', '#4 added 212 packages in 3s', '#5 [4/4] RUN npm test'];
  for (const [k, v] of Object.entries(src.buildArgs ?? {})) lines.splice(1, 0, `#1 build arg ${k}=${v}`);
  return failing ? [...lines, '#5 1 failing test', '#5 ERROR: process "/bin/sh -c npm test" did not complete successfully: exit code: 1']
    : [...lines, '#5 12 passing', '#6 exporting to image', '#6 DONE 0.4s'];
}

function err(code: ConstructorParameters<typeof ApiError>[0], message: string, status: number, details: FieldError[] = []) {
  return new ApiError(code, message, status, { requestId: 'req_mock', details });
}

function withoutSecret(t: Token & { secret: string }): Token {
  return { id: t.id, name: t.name, prefix: t.prefix, scope: t.scope, role: t.role, projects: t.projects, userEmail: t.userEmail, createdAt: t.createdAt, lastUsedAt: t.lastUsedAt, expiresAt: t.expiresAt };
}

export function createMockApi(options: MockOptions = {}): EnclaveApi {
  const latency = options.latencyMs ?? 120;
  const stepMs = options.stepMs ?? 600;
  const storage = options.storage === undefined ? safeSessionStorage() : options.storage;
  const store = new Map<string, Stored>();
  const tokens: (Token & { secret: string })[] = [];
  // v0.7: projects, the people invited to this machine, and the audit log (L3-002 §10).
  const projects = new Map<string, Project>([[RULES.defaultProject, { name: RULES.defaultProject, description: 'Everything made before projects existed, and anything made without a project.', createdAt: new Date().toISOString(), resources: 0 }]]);
  const people: (UserSummary & { code?: string; password?: string })[] = [];
  const audit: AuditEntry[] = [];
  const incidents = new Map<string, Incident[]>();
  const deliveries = new Map<string, NotificationRecord[]>();
  /** The project names are resolved in: references in a spec point inside its own project (ADR-035). */
  let scope: string = RULES.defaultProject;
  let eventId = 1;
  let nextPort = 41000;

  const persisted = options.setupRequired ? null : load(storage);
  let admin: Admin | null = persisted?.admin ?? (options.setupRequired ? null : { ...DEMO_ADMIN });
  let signedIn = persisted?.signedIn ?? false;
  const save = () => {
    try {
      storage?.setItem(STORAGE_KEY, JSON.stringify({ admin, signedIn }));
    } catch {
      // storage blocked: sign-in lasts until reload
    }
  };

  const later = (fn: () => void) => (latency > 0 ? setTimeout(fn, latency) : queueMicrotask(fn));
  const wait = <T,>(v: T) => new Promise<T>((r) => later(() => r(v)));
  const fail = (e: ApiError) => new Promise<never>((_, rej) => later(() => rej(e)));
  const now = () => new Date().toISOString();
  const guard = () => (signedIn ? null : fail(err('UNAUTHENTICATED', 'Sign in to continue.', 401)));
  const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
  const byName = (kind: Kind, name: string) => [...store.values()].find((s) => s.r.kind === kind && s.r.name === name && s.r.project === scope);
  /** Runs fn with names resolved in a project. */
  const within = <T,>(project: string, fn: () => T): T => {
    const prev = scope;
    scope = project;
    try {
      return fn();
    } finally {
      scope = prev;
    }
  };
  const record = (action: string, target: string, detail = '') =>
    audit.unshift({ id: audit.length + 1, at: now(), actor: admin?.email ?? '', action, target, detail, clientIp: '127.0.0.1' });
  const addEvent = (s: Stored, type: string, message: string) => s.events.unshift({ id: eventId++, type, message, createdAt: now() });
  const notFound = () => fail(err('NOT_FOUND', "That resource doesn't exist.", 404));

  function outputs(r: Resource): Resource['outputs'] {
    const op = operateOutputs(r, now());
    if (op) return op;
    const internal = `es-${r.kind}-${r.name}${r.project === RULES.defaultProject ? '' : `.${r.project}`}`;
    const port = (r.outputs.port as number | undefined) ?? nextPort++;
    switch (r.kind) {
      case 'database': {
        const ident = r.name.replace(/-/g, '_');
        const engine = (r.spec as DatabaseSpec).engine;
        const internalPort = { postgres: 5432, mysql: 3306, mongodb: 27017, clickhouse: 8123 }[engine];
        const out: Resource['outputs'] = { engine, host: '127.0.0.1', port, internalHost: internal, internalPort, database: ident, username: ident, passwordSecret: r.name + MANAGED.passwordSecretSuffix };
        if (engine === 'clickhouse') Object.assign(out, { nativePort: port + 10000, internalNativePort: 9000 });
        return out;
      }
      case 'queue':
      case 'topic': {
        const q = r.kind === 'queue';
        const out: Resource['outputs'] = { url: 'nats://127.0.0.1:44222', internalUrl: 'nats://es-system-broker:4222', subject: `es.${q ? 'q' : 't'}.${r.name}`,
          stream: `HC${q ? 'Q' : 'T'}_${r.name}`, username: `${q ? 'q' : 't'}-${r.name}`, passwordSecret: `${r.name}-${r.kind}${MANAGED.passwordSecretSuffix}` };
        if (q) Object.assign(out, { consumer: 'workers', messages: 0, inFlight: 0 });
        else out.subscriptions = ((r.spec as TopicSpec).subscriptions ?? []).length;
        return out;
      }
      case 'search':
        return { internalHost: internal, internalPort: 9200, internalUrl: `http://${internal}:9200`, host: '127.0.0.1', port, url: `http://127.0.0.1:${port}`,
          username: 'admin', passwordSecret: `${r.name}-search${MANAGED.passwordSecretSuffix}`, version: (r.spec as SearchSpec).version === '2' ? '2.19.4' : '3.3.2' };
      case 'email':
        return { internalHost: internal, smtpPort: 1025, host: '127.0.0.1', port, webUrl: `http://127.0.0.1:${port + 10000}`, username: r.name,
          passwordSecret: `${r.name}-email${MANAGED.passwordSecretSuffix}`, mode: (r.spec as EmailSpec).mode ?? 'capture' };
      case 'transcoder': {
        const t = r.spec as TranscoderSpec;
        const out: Resource['outputs'] = { watching: t.watch !== false, input: `${t.input.bucket}/${t.input.prefix ?? ''}` };
        for (const k of ['lastRunStatus', 'lastRunAt']) if (r.outputs[k] !== undefined) out[k] = r.outputs[k] as string;
        return out;
      }
      case 'cdn': {
        // The mock edge issues certificates at once, as for domains.
        const c = r.spec as CDNSpec;
        const [kind, name] = c.origin.split('/');
        const origin = kind === 'bucket' ? 'es-system-objectstore:9000' : `es-app-${name ?? ''}:${c.originPort ?? (byName('app', name ?? '')?.r.spec as AppSpec | undefined)?.port ?? 80}`;
        return { internalHost: internal, internalPort: 8080, origin, url: `${c.tls === 'off' ? 'http' : 'https'}://${c.hostname}`, certificateState: c.tls === 'off' ? 'none' : 'issued',
          cachedBytes: Number(r.outputs.cachedBytes ?? 0) };
      }
      case 'cache':
        return { host: '127.0.0.1', port, internalHost: internal, internalPort: 6379 };
      case 'bucket':
        return { endpoint: 'http://127.0.0.1:41999', internalEndpoint: 'http://es-system-objectstore:9000', bucket: r.name, credentialsSecret: MANAGED.objectStoreCredentials };
      case 'app': {
        // Every deploy is a new revision (ADR-016); numbers only grow.
        const s = r.spec as AppSpec;
        const revision = Number(r.outputs.latestRevision ?? 0) + 1;
        const out: Resource['outputs'] = { internalHost: internal, servingRevision: revision, latestRevision: revision, lastDeploy: 'succeeded' };
        if (s.source) {
          // The mock has no Git: a ref resolves to a commit derived from it, so a redeploy finds nothing new.
          const commit = fakeCommit(`${s.source.git}#${s.source.ref}`);
          Object.assign(out, { builtCommit: commit, builtImage: `enclave.local/${r.name}:r${revision}-${commit.slice(0, 12)}`,
            lastBuild: r.outputs.builtCommit === commit ? 'skipped' : 'succeeded' });
        }
        if (s.port) {
          out.internalPort = s.port;
          if (s.publish) Object.assign(out, { host: '127.0.0.1', port });
        }
        return out;
      }
      case 'secret': {
        const src = (r.spec as { source?: SecretSource }).source;
        return src ? { version: r.generation, valueUpdatedAt: r.updatedAt, store: src.store, key: src.key, syncedAt: now() } : { version: r.generation, valueUpdatedAt: r.updatedAt };
      }
      case 'job': {
        // The mock schedules in the browser's zone; the server uses the job's time zone (ADR-017).
        const j = r.spec as JobSpec;
        const out: Resource['outputs'] = {};
        const next = nextRuns(j.schedule, new Date(), 1)[0];
        if (next) out.nextRunAt = next.toISOString();
        for (const k of ['lastRunStatus', 'lastRunAt']) if (r.outputs[k] !== undefined) out[k] = r.outputs[k] as string;
        return out;
      }
      case 'backup': {
        const b = r.spec as BackupSpec;
        const out: Resource['outputs'] = {};
        const next = nextRuns(b.schedule ?? RULES.backupScheduleDefault, new Date(), 1)[0];
        if (next) out.nextRunAt = next.toISOString();
        for (const k of ['lastRunStatus', 'lastRunAt', 'lastSuccessAt']) if (r.outputs[k] !== undefined) out[k] = r.outputs[k] as string;
        return out;
      }
      case 'domain': {
        // The mock edge issues certificates at once; the real one waits for DNS (ADR-015).
        const d = r.spec as DomainSpec;
        const out: Resource['outputs'] = { url: `${d.tls === 'off' ? 'http' : 'https'}://${d.hostname}`, certificateState: d.tls === 'off' ? 'none' : 'issued' };
        if (d.tls !== 'off') {
          out.certificateIssuer = d.tls === 'internal' ? 'EnclaveStack Local CA' : "Let's Encrypt (mock)";
          out.certificateExpiresAt = new Date(Date.now() + (d.tls === 'internal' ? 7 : 90) * 86_400_000).toISOString();
        }
        if (settings.publicAddress) out.publicAddress = settings.publicAddress;
        if (d.zone) out.certificateIssuer = "Let's Encrypt (mock, DNS-01)";
        return out;
      }
      case 'network': {
        const n = r.spec as NetworkSpec;
        const [base] = n.cidr.split('/');
        return { dockerNetwork: `es-net-${r.name}`, gateway: (base ?? '').replace(/\.0$/, '.1') };
      }
      case 'firewall':
        return { appliedContainers: (r.spec as FirewallSpec).targets.length, appliedAt: now() };
      case 'loadbalancer': {
        const lb = r.spec as LoadBalancerSpec;
        const replicas = lb.targets.reduce((n, t) => n + Number((byName('app', t.app)?.r.spec as AppSpec | undefined)?.replicas ?? 1), 0);
        const out: Resource['outputs'] = { internalHost: internal, internalPort: lb.port, privateDns: `${r.name}.loadbalancer.internal`, targets: replicas, healthyTargets: replicas };
        if (lb.publish) Object.assign(out, { host: '127.0.0.1', port });
        return out;
      }
      case 'zone':
        return { zoneId: `mock-${(r.spec as ZoneSpec).zone}`, nameServers: 'ada.ns.example.net,bob.ns.example.net', provider: (r.spec as ZoneSpec).provider };
      case 'record': {
        const rec = r.spec as RecordSpec;
        const zone = byName('zone', rec.zone)?.r.spec as ZoneSpec | undefined;
        const fqdn = rec.name === '@' ? (zone?.zone ?? '') : `${rec.name}.${zone?.zone ?? ''}`;
        return { fqdn, values: (rec.machine ? [settings.publicAddress ?? ''] : (rec.values ?? [])).join(','), owned: true };
      }
      case 'vm': {
        // The mock boots at once; the real hypervisor takes a minute or two (ADR-025).
        const v = r.spec as VMSpec;
        const ssh = Number(String(r.outputs.sshAddress ?? '').split(':')[1] ?? 0) || nextPort++;
        const out: Resource['outputs'] = { power: v.power ?? 'running', sshAddress: `127.0.0.1:${ssh}`, sshUser: 'enclave', imageSha256: 'f'.repeat(64) };
        if (v.publish?.length) out.host = '127.0.0.1';
        return out;
      }
      case 'volume': {
        const v = r.spec as VolumeSpec;
        const out: Resource['outputs'] = { attachedTo: volumeUsers(r.name).map((u) => `${u.r.kind}/${u.r.name}`).sort().join(',') };
        if (v.type === 'disk') out.sizeGiB = v.sizeGiB ?? RULES.volumeSizeDefault;
        else out.usedBytes = 0;
        if (v.snapshot) out.filledFrom = v.snapshot;
        return out;
      }
      case 'snapshot': {
        if (r.outputs.takenAt) return r.outputs; // a snapshot never changes (SNP-03)
        const [k, name] = (r.spec as SnapshotSpec).source.split('/');
        const src = byName(k as Kind, name ?? '');
        const type = k === 'vm' ? 'disk' : ((src?.r.spec as VolumeSpec | undefined)?.type ?? 'filesystem');
        const out: Resource['outputs'] = { type, sizeBytes: 4_915_200, takenAt: now(), inUse: src ? (k === 'vm' ? (src.r.spec as VMSpec).power !== 'stopped' : volumeUsers(name ?? '').length > 0) : false };
        if (type === 'disk') out.sizeGiB = k === 'vm' ? ((src?.r.spec as VMSpec | undefined)?.diskGiB ?? RULES.vmDiskDefault) : ((src?.r.spec as VolumeSpec | undefined)?.sizeGiB ?? RULES.volumeSizeDefault);
        return out;
      }
    }
    return {};
  }

  /** What uses a volume: apps and jobs that mount it, the VM it is attached to (L3-002 §8.5). */
  function volumeUsers(name: string) {
    return [...store.values()].filter((o) => o.r.status.phase !== 'deleting' && o.r.project === scope && (
      ((o.r.kind === 'app' || o.r.kind === 'job') && ((o.r.spec as AppSpec | JobSpec).volumes ?? []).some((m) => m.volume === name))
      || (o.r.kind === 'vm' && ((o.r.spec as VMSpec).disks ?? []).includes(name))));
  }

  /** Placed kinds report a private address and name (ADR-020). */
  function withPrivate(r: Resource, out: Resource['outputs']): Resource['outputs'] {
    if (!['app', 'database', 'cache', 'vm', 'search', 'email'].includes(r.kind)) return out;
    const net = (r.spec as { network?: string }).network;
    const cidr = net ? (byName('network', net)?.r.spec as NetworkSpec | undefined)?.cidr : '172.30.0.0/16';
    const [base] = (cidr ?? '172.30.0.0/16').split('/');
    const octets = (base ?? '').split('.');
    octets[3] = String(10 + (r.name.length % 200));
    const extra: Resource['outputs'] = { privateIp: octets.join('.'), privateDns: `${r.name}.${r.kind}.${r.project === RULES.defaultProject ? '' : `${r.project}.`}internal` };
    if (r.kind === 'app') extra.runningReplicas = Number((r.spec as AppSpec).replicas ?? 1);
    return { ...out, ...extra };
  }

  // Simulated reconciler: walks the kind's steps, then ready — or failed for names starting "fail-".
  function reconcile(s: Stored, from: Phase) {
    if (s.timer) clearTimeout(s.timer);
    const r = s.r;
    r.status = { ...r.status, phase: from, message: '', attempts: 0, steps: STEP_NAMES[r.kind].map((name) => ({ name, state: 'pending' as const })), lastTransitionAt: now() };
    let i = 0;
    const tick = () => {
      if (r.status.phase === 'deleting') return;
      const prev = r.status.steps[i - 1];
      if (prev) prev.state = 'done';
      if (r.name.startsWith('fail-') && i === 1) {
        const step = r.status.steps[0];
        if (step) step.state = 'failed';
        r.status = { ...r.status, phase: 'failed', message: 'image not found: the registry has no such image', lastTransitionAt: now() };
        addEvent(s, 'failed', r.status.message);
        return;
      }
      const src = r.kind === 'app' ? (r.spec as AppSpec).source : undefined;
      if (src && i === 1) {
        // A build runs before the deploy; a repository URL containing "fail" fails its tests step.
        const commit = fakeCommit(`${src.git}#${src.ref}`);
        const failing = src.git.includes('fail');
        s.buildLog = mockBuildLog(src, failing);
        if (failing) {
          const step = r.status.steps[0];
          if (step) step.state = 'failed';
          const served = Number(r.outputs.servingRevision ?? 0) > 0;
          r.outputs = { ...r.outputs, lastBuild: 'failed', lastDeploy: 'failed' };
          r.status = { ...r.status, phase: 'failed', lastTransitionAt: now(),
            message: `build of ${commit.slice(0, 7)} failed: process "/bin/sh -c npm test" did not complete successfully: exit code: 1${served ? '. The serving revision keeps running' : ''}` };
          addEvent(s, 'build_failed', r.status.message);
          return;
        }
        addEvent(s, r.outputs.builtCommit === commit ? 'build_skipped' : 'built', r.outputs.builtCommit === commit
          ? `Commit ${commit.slice(0, 7)} and the build settings are unchanged; build skipped.` : `Built ${commit.slice(0, 7)} from ${src.ref}.`);
      }
      if (i === r.status.steps.length) {
        r.outputs = within(r.project, () => withPrivate(r, outputs(r)));
        r.status = { ...r.status, phase: 'ready', observedGeneration: r.generation, lastTransitionAt: now() };
        r.updatedAt = now();
        addEvent(s, 'ready', 'Ready.');
        return;
      }
      if (r.status.phase === 'pending') r.status.phase = 'provisioning';
      const step = r.status.steps[i];
      if (step) step.state = 'active';
      i++;
      s.timer = setTimeout(tick, stepMs);
    };
    s.timer = setTimeout(tick, stepMs);
  }

  function insert(kind: Kind, name: string, spec: Record<string, unknown>, ownerId: string | null, secret?: string, project: string = scope): Stored {
    const t = now();
    // A secret keeps only its source; its value never shows in the spec (EXT-03).
    const shown = kind === 'secret' ? (spec.source ? { source: spec.source } : {}) : spec;
    const r: Resource = {
      id: newId(kind), kind, name, project, generation: 1, ownerId, spec: shown as SpecByKind[Kind],
      status: { phase: 'pending', message: '', observedGeneration: 0, attempts: 0, steps: [], lastTransitionAt: t }, outputs: {}, createdAt: t, updatedAt: t,
    };
    const s: Stored = { r, secret, events: [] };
    store.set(r.id, s);
    addEvent(s, 'created', ownerId ? 'Created and managed by EnclaveStack.' : 'Created.');
    reconcile(s, 'pending');
    return s;
  }

  function normalize(kind: Kind, spec: Record<string, unknown>): Record<string, unknown> {
    switch (kind) {
      case 'app': {
        const hc = spec.healthCheck as Record<string, unknown> | undefined;
        const healthCheck = hc && {
          intervalSeconds: RULES.healthIntervalDefault, retries: RULES.healthRetriesDefault,
          ...(hc.type === 'http' ? { path: '/' } : {}), ...hc,
          timeoutSeconds: (hc.timeoutSeconds as number | undefined) ?? Math.min(RULES.healthTimeoutDefault, (hc.intervalSeconds as number | undefined) ?? RULES.healthIntervalDefault),
        };
        const src = spec.source as Record<string, unknown> | undefined;
        const source = src && { dockerfile: RULES.gitDockerfileDefault, context: RULES.gitContextDefault, ...src };
        return { memoryMiB: RULES.appMemoryDefault, strategy: 'rolling', ...spec, ...(healthCheck ? { healthCheck } : {}), ...(source ? { source } : {}) };
      }
      case 'database': {
        const engine = (spec.engine as string | undefined) ?? RULES.databaseEngine;
        return { ...spec, engine, version: spec.version ?? (RULES.databaseDefaultVersions as Record<string, string>)[engine] };
      }
      case 'queue':
        return { visibilityTimeoutSeconds: RULES.queueVisibilityDefault, retentionHours: RULES.queueRetentionDefault, maxMessageBytes: RULES.messageBytesDefault, ...spec };
      case 'topic':
        return { retentionHours: RULES.topicRetentionDefault, ...spec };
      case 'search':
        return { version: '3', memoryMiB: RULES.searchMemoryDefault, ...spec };
      case 'transcoder': {
        const t = spec as unknown as TranscoderSpec;
        return { timeoutSeconds: RULES.transcodeTimeoutDefault, concurrency: RULES.concurrencyDefault, memoryMiB: RULES.transcodeMemoryDefault, ...spec,
          input: { ...t.input, prefix: normalizePrefix(t.input?.prefix) }, output: { ...t.output, prefix: normalizePrefix(t.output?.prefix || RULES.transcodeOutputDefault) } };
      }
      case 'cdn': {
        const hostname = String(spec.hostname ?? '').trim().toLowerCase().replace(/\.$/, '');
        return { redirectHttp: true, defaultTtlSeconds: RULES.cdnTtlDefault, maxSizeMiB: RULES.cdnSizeDefault, ...spec, hostname,
          tls: spec.tls ?? (hostname.endsWith(RULES.localhostSuffix) ? 'internal' : 'auto') };
      }
      case 'email': {
        const relay = spec.relay as Record<string, unknown> | undefined;
        return { mode: 'capture', keepMessages: RULES.keepMessagesDefault, ...spec, ...(relay ? { relay: { port: RULES.relayPortDefault, tls: 'starttls', ...relay } } : {}) };
      }
      case 'cache':
        return { engine: RULES.cacheEngine, version: RULES.cacheVersion, maxMemoryMiB: RULES.cacheMemoryDefault, ...spec };
      case 'bucket':
        return { versioning: false, ...spec };
      case 'secret':
        return spec;
      case 'job':
        return { timeoutSeconds: RULES.jobTimeoutDefault, overlap: 'forbid', memoryMiB: RULES.appMemoryDefault, ...spec, schedule: String(spec.schedule ?? '').trim() };
      case 'backup':
        return { prefix: RULES.backupPrefixDefault, schedule: RULES.backupScheduleDefault, keep: RULES.backupKeepDefault, keepDays: 0, timeoutSeconds: RULES.backupTimeoutDefault,
          ...spec, ...(typeof spec.schedule === 'string' ? { schedule: spec.schedule.trim() || RULES.backupScheduleDefault } : {}) };
      case 'domain': {
        const hostname = String(spec.hostname ?? '').trim().toLowerCase().replace(/\.$/, '');
        return { redirectHttp: true, ...spec, hostname, tls: spec.tls ?? (hostname.endsWith(RULES.localhostSuffix) ? 'internal' : 'auto') };
      }
      case 'network': {
        const cidr = String(spec.cidr ?? '').trim();
        const subnets = (spec.subnets as unknown[] | undefined)?.length ? spec.subnets : [{ name: RULES.defaultSubnetName, cidr }];
        return { internet: true, ...spec, cidr, subnets };
      }
      case 'firewall': {
        const withProtocol = (rules: unknown) => (rules as FirewallRule[] | undefined)?.map((r) => ({ protocol: 'tcp', ...r }));
        return { egressDefault: 'allow', ...spec, ...(spec.ingress ? { ingress: withProtocol(spec.ingress) } : {}), ...(spec.egress ? { egress: withProtocol(spec.egress) } : {}) };
      }
      case 'loadbalancer': {
        const targets = (spec.targets as LoadBalancerTarget[] | undefined)?.map((t) => ({ weight: RULES.lbWeightDefault, ...t })) ?? [];
        const hc = (spec.healthCheck as LoadBalancerCheck | undefined) ?? { type: 'tcp' };
        const healthCheck = { intervalSeconds: RULES.lbIntervalDefault, timeoutSeconds: RULES.lbTimeoutDefault, healthyThreshold: RULES.lbHealthyDefault,
          unhealthyThreshold: RULES.lbUnhealthyDefault, ...(hc.type === 'http' ? { path: '/' } : {}), ...hc };
        return { protocol: 'http', algorithm: 'round_robin', ...spec, targets, healthCheck };
      }
      case 'zone':
        return { ...spec, zone: String(spec.zone ?? '').trim().toLowerCase().replace(/\.$/, '') };
      case 'record':
        return { ttl: RULES.recordTtlDefault, ...spec, name: String(spec.name ?? '').trim().toLowerCase(), type: String(spec.type ?? '').toUpperCase() };
      case 'vm':
        return { cpus: RULES.vmCpusDefault, memoryMiB: RULES.vmMemoryDefault, diskGiB: RULES.vmDiskDefault, power: 'running', ...spec, image: String(spec.image ?? '').trim() };
      case 'volume':
        return spec.type === 'disk' ? { sizeGiB: RULES.volumeSizeDefault, ...spec } : spec;
      case 'snapshot':
        return { ...spec, source: String(spec.source ?? '').trim() };
    }
    return normalizeOperate(kind, spec) ?? spec;
  }

  function checkSpec(kind: Kind, spec: Record<string, unknown>): FieldError[] {
    const details: FieldError[] = Object.entries(validateSpec(kind, spec)).map(([field, message]) => ({ field, code: 'INVALID_FORMAT', message }));
    if (kind === 'app') {
      for (const [k, name] of Object.entries((spec as unknown as AppSpec).secretEnv ?? {})) {
        if (!byName('secret', name)) details.push({ field: `spec.secretEnv.${k}`, code: 'NOT_FOUND', message: `There's no secret named ${name}.` });
      }
      const cred = (spec as unknown as AppSpec).source?.credentialsSecret;
      if (cred && !byName('secret', cred)) details.push({ field: 'spec.source.credentialsSecret', code: 'NOT_FOUND', message: `There's no secret named ${cred}.` });
    }
    if (kind === 'job') {
      const j = spec as unknown as JobSpec;
      if (j.app && !byName('app', j.app)) details.push({ field: 'spec.app', code: 'NOT_FOUND', message: `There's no app named ${j.app}.` });
      for (const [k, name] of Object.entries(j.secretEnv ?? {})) {
        if (!byName('secret', name)) details.push({ field: `spec.secretEnv.${k}`, code: 'NOT_FOUND', message: `There's no secret named ${name}.` });
      }
    }
    if (kind === 'backup') {
      const b = spec as unknown as BackupSpec;
      const missing = (k: Kind, name: string | undefined, field: string) => {
        const o = name ? byName(k, name) : undefined;
        if (name && (!o || o.r.status.phase === 'deleting')) details.push({ field, code: 'NOT_FOUND', message: `There's no ${k} named ${name}.` });
      };
      if (!details.some((d) => d.field === 'spec.source')) {
        if (b.source.database) missing('database', b.source.database, 'spec.source');
        else missing('cache', b.source.cache, 'spec.source');
      }
      if (b.destination.bucket) missing('bucket', b.destination.bucket, 'spec.destination.bucket');
      else if (b.destination.s3) missing('secret', b.destination.s3.credentialsSecret, 'spec.destination.s3.credentialsSecret');
    }
    const exists = (k: Kind, name: string | undefined, field: string) => {
      const o = name ? byName(k, name) : undefined;
      if (name && (!o || o.r.status.phase === 'deleting')) details.push({ field, code: 'NOT_FOUND', message: `There's no ${k === 'loadbalancer' ? 'load balancer' : k} named ${name}.` });
      return o;
    };
    const placed = spec as { network?: string; subnet?: string; publish?: boolean };
    if ((kind === 'app' || kind === 'database' || kind === 'cache' || kind === 'job') && placed.network) {
      const n = exists('network', placed.network, 'spec.network');
      const ns = n?.r.spec as NetworkSpec | undefined;
      if (ns && placed.subnet && !(ns.subnets ?? []).some((x) => x.name === placed.subnet))
        details.push({ field: 'spec.subnet', code: 'NOT_FOUND', message: `The network ${placed.network} has no subnet named ${placed.subnet}.` });
      if (ns && ns.internet === false && placed.publish)
        details.push({ field: 'spec.publish', code: 'NOT_ALLOWED', message: `The network ${placed.network} has no internet access, so nothing in it can publish a host port.` });
    }
    if (kind === 'firewall') {
      const fw = spec as unknown as FirewallSpec;
      fw.targets.forEach((t, i) => {
        const [k = '', name] = t.split('/');
        exists(k as Kind, name, `spec.targets[${i}]`);
      });
    }
    if (kind === 'loadbalancer') {
      const lb = spec as unknown as LoadBalancerSpec;
      const nets = new Set(lb.targets.map((t) => (exists('app', t.app, 'spec.targets')?.r.spec as AppSpec | undefined)?.network ?? ''));
      if (nets.size > 1) details.push({ field: 'spec.targets', code: 'NOT_ALLOWED', message: "A load balancer's apps share one network." });
    }
    if (kind === 'app' || kind === 'job') {
      ((spec as unknown as AppSpec).volumes ?? []).forEach((m, i) => {
        const v = exists('volume', m.volume, `spec.volumes[${i}].volume`);
        if (v && (v.r.spec as VolumeSpec).type !== 'filesystem') details.push({ field: `spec.volumes[${i}].volume`, code: 'NOT_ALLOWED', message: `${m.volume} is a disk volume, which only a VM can use. Mount a filesystem volume.` });
      });
    }
    if (kind === 'vm') {
      const v = spec as unknown as VMSpec;
      if (v.network) exists('network', v.network, 'spec.network');
      (v.disks ?? []).forEach((d, i) => {
        const o = exists('volume', d, `spec.disks[${i}]`);
        if (o && (o.r.spec as VolumeSpec).type !== 'disk') details.push({ field: `spec.disks[${i}]`, code: 'NOT_ALLOWED', message: `${d} is a filesystem volume, which apps and jobs mount. Attach a disk volume.` });
      });
      if (v.sshKeySecret) exists('secret', v.sshKeySecret, 'spec.sshKeySecret');
      if (v.image.startsWith(RULES.snapshotImagePrefix)) exists('snapshot', v.image.slice(RULES.snapshotImagePrefix.length), 'spec.image');
    }
    if (kind === 'volume' && (spec as unknown as VolumeSpec).snapshot) {
      const v = spec as unknown as VolumeSpec;
      const snap = exists('snapshot', v.snapshot, 'spec.snapshot');
      if (snap && snap.r.outputs.type !== v.type) details.push({ field: 'spec.snapshot', code: 'NOT_ALLOWED', message: `The snapshot ${v.snapshot} is a ${String(snap.r.outputs.type)} snapshot; it can only start a ${String(snap.r.outputs.type)} volume.` });
    }
    if (kind === 'snapshot') {
      const [k = '', name] = (spec as unknown as SnapshotSpec).source.split('/');
      if (k === 'volume' || k === 'vm') exists(k, name, 'spec.source');
    }
    if (kind === 'zone') exists('secret', (spec as unknown as ZoneSpec).credentialsSecret, 'spec.credentialsSecret');
    if ((kind === 'search' || kind === 'email') && placed.network) exists('network', placed.network, 'spec.network');
    if (kind === 'queue' && (spec as QueueSpec).deadLetterQueue) exists('queue', (spec as QueueSpec).deadLetterQueue, 'spec.deadLetterQueue');
    if (kind === 'topic') {
      ((spec as TopicSpec).subscriptions ?? []).forEach((sub, i) => {
        if (sub.queue) exists('queue', sub.queue, `spec.subscriptions[${i}].queue`);
        if (sub.secret) exists('secret', sub.secret, `spec.subscriptions[${i}].secret`);
      });
    }
    if (kind === 'transcoder') {
      const t = spec as unknown as TranscoderSpec;
      exists('bucket', t.input.bucket, 'spec.input.bucket');
      exists('bucket', t.output.bucket, 'spec.output.bucket');
    }
    if (kind === 'email' && (spec as EmailSpec).relay) exists('secret', (spec as EmailSpec).relay?.credentialsSecret, 'spec.relay.credentialsSecret');
    if (kind === 'cdn') {
      const c = spec as unknown as CDNSpec;
      const [k = '', name] = c.origin.split('/');
      const origin = exists(k as Kind, name, 'spec.origin');
      if (origin && k === 'app' && !c.originPort && !(origin.r.spec as AppSpec).port)
        details.push({ field: 'spec.originPort', code: 'REQUIRED', message: `The app ${name ?? ''} has no port. Give the app a port, or set the origin port here.` });
      if (c.zone) exists('zone', c.zone, 'spec.zone');
    }
    if (kind === 'record') exists('zone', (spec as unknown as RecordSpec).zone, 'spec.zone');
    // v0.7: names inside the project (ADR-035).
    const refExists = (ref: string, field: string) => {
      if (ref === 'machine') return;
      const [k = '', name] = ref.split('/');
      exists(k as Kind, name, field);
    };
    if (kind === 'dashboard') (spec as unknown as DashboardSpec).widgets.forEach((w, i) => w.resources.forEach((x, j) => refExists(x, `spec.widgets[${i}].resources[${j}]`)));
    if (kind === 'alert') {
      const a = spec as unknown as AlertSpec;
      a.condition.resources.forEach((x, i) => refExists(x, `spec.condition.resources[${i}]`));
      a.channels.forEach((c, i) => exists('channel', c, `spec.channels[${i}]`));
    }
    if (kind === 'channel') {
      const c = spec as unknown as ChannelSpec;
      if (c.email) exists('email', c.email.service, 'spec.email.service');
      if (c.webhook?.secret) exists('secret', c.webhook.secret, 'spec.webhook.secret');
      if (c.secret) exists('secret', c.secret, 'spec.secret');
    }
    if (kind === 'logexport') {
      const l = spec as unknown as LogExportSpec;
      (l.sources ?? []).forEach((x, i) => refExists(x, `spec.sources[${i}]`));
      if (l.destination.bucket) exists('bucket', l.destination.bucket.bucket, 'spec.destination.bucket.bucket');
      if (l.destination.s3) exists('secret', l.destination.s3.credentialsSecret, 'spec.destination.s3.credentialsSecret');
      const h = l.destination.http ?? l.destination.otlp ?? l.destination.loki;
      if (h?.headersSecret) exists('secret', h.headersSecret, `spec.destination.${l.destination.http ? 'http' : l.destination.otlp ? 'otlp' : 'loki'}.headersSecret`);
    }
    if (kind === 'secretstore') {
      const st = spec as unknown as SecretStoreSpec;
      const block = st[st.provider] as { tokenSecret?: string; credentialsSecret?: string } | undefined;
      const cred = block?.tokenSecret ?? block?.credentialsSecret;
      if (cred) exists('secret', cred, `spec.${st.provider}.${block?.tokenSecret ? 'tokenSecret' : 'credentialsSecret'}`);
    }
    if (kind === 'secret' && (spec as { source?: SecretSource }).source) exists('secretstore', (spec as { source: SecretSource }).source.store, 'spec.source.store');
    if (kind === 'domain' && (spec as unknown as DomainSpec).zone) exists('zone', (spec as unknown as DomainSpec).zone, 'spec.zone');
    if (kind === 'domain') {
      const d = spec as unknown as DomainSpec;
      const app = byName('app', d.app);
      if (!app) details.push({ field: 'spec.app', code: 'NOT_FOUND', message: `There's no app named ${d.app}.` });
      else if (!d.port && !(app.r.spec as AppSpec).port) details.push({ field: 'spec.port', code: 'REQUIRED', message: `The app ${d.app} has no port. Give the app a port, or set the port here.` });
    }
    return details;
  }

  /** Backups whose source or destination is r (CP-12, BAK-08). */
  const backupsUsing = (r: Resource) =>
    [...store.values()].filter((o) => {
      if (o.r.kind !== 'backup' || o.r.status.phase === 'deleting' || o.r.project !== r.project) return false;
      const b = o.r.spec as BackupSpec;
      return (r.kind === 'database' && b.source.database === r.name) || (r.kind === 'cache' && b.source.cache === r.name)
        || (r.kind === 'bucket' && b.destination.bucket === r.name) || (r.kind === 'secret' && b.destination.s3?.credentialsSecret === r.name);
    });

  /** A mock backup run: succeeds after a step with a made-up object (no data is kept). */
  function backupRun(s: Stored, trigger: Run['trigger']): RunDetail {
    const b = s.r.spec as BackupSpec;
    const run: RunDetail = { id: 'run_' + Math.random().toString(36).slice(2, 18).padEnd(16, 'a'), trigger, status: 'running', scheduledFor: null,
      startedAt: now(), finishedAt: null, exitCode: null, message: '', outputTail: '' };
    (s.runs ??= []).unshift(run);
    addEvent(s, trigger === 'pre_restore' ? 'safety_backup' : 'action_run', trigger === 'pre_restore' ? `Safety backup ${run.id} before a restore.` : `Run ${run.id} requested.`);
    setTimeout(() => {
      const isDb = Boolean(b.source.database);
      const source = b.source.database ?? b.source.cache ?? '';
      const size = 1_200_000 + Math.floor(Math.random() * 400_000);
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.(\d{3})Z$/, '.$1Z');
      const where = b.destination.bucket ? `http://127.0.0.1:41999/${b.destination.bucket}` : `${(b.destination.s3?.endpoint ?? '').replace(/\/$/, '')}/${b.destination.s3?.bucket ?? ''}`;
      Object.assign(run, { status: 'succeeded', finishedAt: now(), message: `Backed up ${isDb ? 'database' : 'cache'} ${source} (${isDb ? 'postgres' : 'redis'}): ${(size / 2 ** 20).toFixed(1)} MiB.`,
        artifact: { key: `${b.prefix ?? RULES.backupPrefixDefault}/${source}/${stamp}-${run.id}.${isDb ? 'pgdump' : 'rdb'}`, sizeBytes: size,
          sha256: Array.from({ length: 64 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join(''), destination: where } });
      s.r.outputs = { ...s.r.outputs, lastRunStatus: 'succeeded', lastRunAt: run.finishedAt ?? now(), lastSuccessAt: run.finishedAt ?? now() };
      // Retention (BAK-04): beyond the newest `keep`, runs lose their stored backup and can't be restored.
      const stored = (s.runs ?? []).filter((x) => x.artifact);
      for (const old of stored.slice(b.keep ?? RULES.backupKeepDefault)) {
        delete old.artifact;
        old.message += ' Removed by retention.';
      }
    }, stepMs);
    return run;
  }

  /** Resources whose v0.4 references name r (L3-002 §7.4): placement, firewall targets and peers, load balancer targets, zones. */
  const v04Users = (r: Resource) =>
    [...store.values()].filter((o) => {
      if (o.r.status.phase === 'deleting' || o.r.id === r.id || o.r.project !== r.project) return false;
      const spec = o.r.spec as unknown as Record<string, unknown>;
      const ref = `${r.kind}/${r.name}`;
      switch (o.r.kind) {
        case 'dashboard':
          return (o.r.spec as DashboardSpec).widgets.some((w) => w.resources.includes(ref));
        case 'alert':
          return (o.r.spec as AlertSpec).condition.resources.includes(ref) || (r.kind === 'channel' && (o.r.spec as AlertSpec).channels.includes(r.name));
        case 'channel': {
          const c = o.r.spec as ChannelSpec;
          return (r.kind === 'email' && c.email?.service === r.name) || (r.kind === 'secret' && (c.secret === r.name || c.webhook?.secret === r.name));
        }
        case 'logexport': {
          const l = o.r.spec as LogExportSpec;
          const h = l.destination.http ?? l.destination.otlp ?? l.destination.loki;
          return (l.sources ?? []).includes(ref) || (r.kind === 'bucket' && l.destination.bucket?.bucket === r.name)
            || (r.kind === 'secret' && (l.destination.s3?.credentialsSecret === r.name || h?.headersSecret === r.name));
        }
        case 'secretstore': {
          const st = o.r.spec as SecretStoreSpec;
          const block = st[st.provider] as { tokenSecret?: string; credentialsSecret?: string } | undefined;
          return r.kind === 'secret' && (block?.tokenSecret ?? block?.credentialsSecret) === r.name;
        }
        case 'secret':
          return r.kind === 'secretstore' && (o.r.spec as { source?: SecretSource }).source?.store === r.name;
        case 'firewall': {
          const fw = o.r.spec as FirewallSpec;
          return fw.targets.includes(ref) || [...(fw.ingress ?? []), ...(fw.egress ?? [])].some((rule) => [...(rule.from ?? []), ...(rule.to ?? [])].includes(ref));
        }
        case 'loadbalancer':
          return r.kind === 'app' && (o.r.spec as LoadBalancerSpec).targets.some((t) => t.app === r.name);
        case 'record':
          return r.kind === 'zone' && (o.r.spec as RecordSpec).zone === r.name;
        case 'zone':
          return r.kind === 'secret' && (o.r.spec as ZoneSpec).credentialsSecret === r.name;
        case 'domain':
          return r.kind === 'zone' && (o.r.spec as DomainSpec).zone === r.name;
        case 'queue':
          return r.kind === 'queue' && (o.r.spec as QueueSpec).deadLetterQueue === r.name;
        case 'topic':
          return ((o.r.spec as TopicSpec).subscriptions ?? []).some((sub) => (r.kind === 'queue' && sub.queue === r.name) || (r.kind === 'secret' && sub.secret === r.name));
        case 'transcoder':
          return r.kind === 'bucket' && [(o.r.spec as TranscoderSpec).input.bucket, (o.r.spec as TranscoderSpec).output.bucket].includes(r.name);
        case 'cdn':
          return (o.r.spec as CDNSpec).origin === ref || (r.kind === 'zone' && (o.r.spec as CDNSpec).zone === r.name);
        case 'email':
          return (r.kind === 'secret' && (o.r.spec as EmailSpec).relay?.credentialsSecret === r.name) || (r.kind === 'network' && spec.network === r.name);
        default:
          return r.kind === 'network' && spec.network === r.name;
      }
    });
  const domainsOf = (app: string) => [...store.values()].filter((o) => o.r.kind === 'domain' && o.r.project === scope && (o.r.spec as DomainSpec).app === app && o.r.status.phase !== 'deleting');
  function hostnameTaken(hostname: string, exceptId?: string): ApiError | null {
    const other = [...store.values()].find((o) => (o.r.kind === 'domain' || o.r.kind === 'cdn') && o.r.id !== exceptId && (o.r.spec as DomainSpec | CDNSpec).hostname === hostname);
    if (!other) return null;
    const msg = `The ${other.r.kind} ${other.r.name} already serves ${hostname}.`;
    return err('HOSTNAME_TAKEN', msg, 409, [{ field: 'spec.hostname', code: 'NOT_ALLOWED', message: msg }]);
  }
  const settings: { publicAddress: string | null; acmeEmail: string | null; inviteEmail: InviteEmail | null } = { publicAddress: null, acmeEmail: null, inviteEmail: null };

  if (options.seed === 'demo') {
    insert('secret', 'stripe-key', {}, null, 'sk_demo');
    projects.set('shop', { name: 'shop', description: 'The online shop', createdAt: now(), resources: 0 });
    const db = insert('database', 'orders', { engine: 'postgres', version: '16' }, null);
    insert('secret', 'orders-password', {}, db.r.id, 'demo-password');
    insert('cache', 'sessions', { engine: 'redis', version: '7', maxMemoryMiB: 256 }, null);
    insert('app', 'api', { image: 'ghcr.io/acme/api:1.4', port: 8080, publish: true, memoryMiB: 256, env: { LOG_LEVEL: 'info' }, secretEnv: { STRIPE_KEY: 'stripe-key' } }, null);
    insert('channel', 'ops', { type: 'webhook', webhook: { url: 'https://hooks.example.com/enclave' } }, null);
    insert('alert', 'api-cpu', normalize('alert', { condition: { type: 'metric', resources: ['app/api'], metric: 'cpu', operator: 'above', threshold: 0.9 }, channels: ['ops'] }), null);
    insert('dashboard', 'health', { widgets: [{ title: 'API CPU', metric: 'cpu', resources: ['app/api'] }, { title: 'Requests', metric: 'requests', resources: ['app/api'] }] }, null);
    insert('app', 'storefront', { image: 'ghcr.io/acme/storefront:2.0', port: 3000, memoryMiB: 512 }, null, undefined, 'shop');
  }

  const session = (): Session => ({ user: { id: 'usr_mock', email: admin?.email ?? '' }, machineName: admin?.machineName ?? '', authMethod: 'session', scope: 'full',
    machineRole: options.as ? options.as.machineRole : 'admin', projectRoles: { ...(options.as?.projectRoles ?? {}) }, token: null });
  /** The server's refusal when a role is too weak (L3-002 §10.2): a project you can't see is not found. */
  const refuse = (project: string, need: Role) => {
    if (!roleIn(session(), project)) return fail(err('NOT_FOUND', "That resource doesn't exist.", 404));
    if (!can(session(), project, need)) return fail(err('FORBIDDEN', `This needs ${need === 'viewer' ? 'a' : 'an'} ${need} role in ${project}. Ask an admin of ${project}.`, 403));
    return null;
  };
  const adminSummary = (): UserSummary => ({ id: 'usr_mock', email: admin?.email ?? '', machineRole: 'admin', projectRoles: {}, status: 'active', createdAt: now(), lastSignInAt: now() });
  const publicUser = (u: UserSummary): UserSummary => ({ id: u.id, email: u.email, machineRole: u.machineRole, projectRoles: { ...u.projectRoles }, status: u.status,
    createdAt: u.createdAt, lastSignInAt: u.lastSignInAt });
  const invitation = (u: UserSummary & { code?: string }, reset = false): Invitation => {
    u.code = Math.random().toString(36).slice(2, 14);
    const inv: Invitation = { user: publicUser(u), url: `${typeof window === 'undefined' ? 'http://localhost' : window.location.origin}/invite?code=${u.code}`,
      expiresAt: new Date(Date.now() + 7 * 86400000).toISOString(), emailed: false };
    // With inviteEmail set, the link also goes through that email service (IAM-04).
    const ie = settings.inviteEmail;
    if (ie) {
      const svc = within(ie.project ?? RULES.defaultProject, () => byName('email', ie.service));
      if (!svc || svc.r.status.phase !== 'ready') inv.emailError = `The link wasn't emailed: the email service ${ie.service} isn't ready. Send it yourself.`;
      else {
        const subject = reset ? `Reset your password on ${admin?.machineName ?? ''}` : `You're invited to ${admin?.machineName ?? ''}`;
        const text = `${admin?.email ?? ''} sent you a link:\n\n${inv.url}\n`;
        (svc.emails ??= []).push({ id: 'msg' + String(svc.emails.length + 1).padStart(6, '0'), from: ie.from, to: [u.email], subject, sentAt: now(), sizeBytes: text.length + 400,
          relay: (svc.r.spec as EmailSpec).mode === 'relay' ? 'relayed' : 'captured', cc: [], bcc: [], replyTo: '', text, html: '', headers: { Subject: [subject], From: [ie.from] } });
        inv.emailed = true;
      }
    }
    return inv;
  };
  const validationFailed = (details: FieldError[]) => fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
  /** A secret store answers with a made-up value; settings containing "fail" refuse (EXT-02). */
  const storeAnswer = (st: Stored | undefined, src: SecretSource): { value: string } | { refused: string } => {
    const sp = st?.r.spec as SecretStoreSpec | undefined;
    if (!sp || JSON.stringify(sp[sp.provider] ?? {}).includes('fail')) return { refused: `The ${sp?.provider ?? src.store} store refused: permission denied.` };
    return { value: `mock-${src.key}${src.field ? `#${src.field}` : ''}` };
  };
  /** A sync that fails fails the secret, as the reconciler does; the last value is kept. */
  const failSoon = (s: Stored, message: string) => {
    if (s.timer) clearTimeout(s.timer);
    s.timer = setTimeout(() => {
      s.r.status = { ...s.r.status, phase: 'failed', message, lastTransitionAt: now() };
      addEvent(s, 'failed', message);
    }, stepMs);
  };

  const resourcesIn = (p: string) => [...store.values()].filter((o) => o.r.project === p && o.r.status.phase !== 'deleting');
  const userById = (id: string) => (id === 'usr_mock' ? null : people.find((u) => u.id === id));
  function checkRoles(machineRole: Role | null | undefined, projectRoles: Record<string, Role | null> | undefined): FieldError[] {
    const details: FieldError[] = [];
    for (const [p, role] of Object.entries(projectRoles ?? {})) {
      if (!projects.has(p)) details.push({ field: `projectRoles.${p}`, code: 'NOT_FOUND', message: `There's no project named ${p}.` });
      else if (role !== null && !(ROLES as readonly string[]).includes(role)) details.push({ field: `projectRoles.${p}`, code: 'NOT_ALLOWED', message: 'Choose viewer, editor or admin.' });
    }
    if (machineRole && !(ROLES as readonly string[]).includes(machineRole)) details.push({ field: 'machineRole', code: 'NOT_ALLOWED', message: 'Choose viewer, editor or admin.' });
    return details;
  }

  const api: EnclaveApi = {
    getSetupRequired: () => wait(admin === null),
    setup: (input) => {
      if (admin) return fail(err('SETUP_COMPLETE', 'This machine is already set up. Sign in instead.', 409));
      if (input.setupCode.replace(/[\s-]/g, '').toUpperCase() !== DEMO_SETUP_CODE.replace(/-/g, '')) {
        return fail(err('INVALID_SETUP_CODE', "That setup code isn't right. Use the code printed where enclave server is running.", 403));
      }
      const details: FieldError[] = [];
      if (!input.machineName.trim()) details.push({ field: 'machineName', code: 'OUT_OF_RANGE', message: 'Use 1–64 characters.' });
      if (!input.email.includes('@')) details.push({ field: 'email', code: 'INVALID_FORMAT', message: 'Enter an email address like you@example.com.' });
      if ([...input.password].length < RULES.passwordMin) details.push({ field: 'password', code: 'OUT_OF_RANGE', message: 'Use at least 12 characters.' });
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      admin = { email: input.email.trim(), password: input.password, machineName: input.machineName.trim() };
      signedIn = true;
      save();
      return wait(session());
    },
    signIn: (email, password) => {
      if (!admin) return fail(err('SETUP_REQUIRED', 'Finish first-run setup before signing in.', 409));
      if (admin.email.toLowerCase() !== email.trim().toLowerCase() || admin.password !== password) {
        return fail(err('INVALID_CREDENTIALS', "That email and password don't match.", 401));
      }
      signedIn = true;
      save();
      return wait(session());
    },
    getSession: () => wait(signedIn ? session() : null),
    signOut: () => {
      signedIn = false;
      save();
      return wait(undefined);
    },

    listTokens: () => guard() ?? wait(tokens.map(withoutSecret)),
    createToken: (input) => {
      const blocked = guard();
      if (blocked) return blocked;
      if (!input.name.trim()) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'name', code: 'OUT_OF_RANGE', message: 'Use 1–64 characters.' }]));
      const secret = 'hc_pat_' + Math.random().toString(36).slice(2).padEnd(26, 'x');
      const unknown = (input.projects ?? []).findIndex((p) => !projects.has(p));
      if (unknown >= 0) return validationFailed([{ field: `projects[${unknown}]`, code: 'NOT_FOUND', message: `There's no project named ${input.projects?.[unknown] ?? ''}.` }]);
      const role: Role = input.role ?? 'admin';
      const t = { id: 'tok_' + Math.random().toString(36).slice(2, 12), name: input.name.trim(), prefix: secret.slice(0, 13), scope: role === 'viewer' ? 'read' as const : 'write' as const,
        role, projects: input.projects?.length ? [...input.projects] : null, userEmail: admin?.email ?? '', createdAt: now(),
        lastUsedAt: null, expiresAt: input.expiresInDays ? new Date(Date.now() + input.expiresInDays * 86400000).toISOString() : null, secret };
      record('token_created', `token/${t.name}`, `${role}${t.projects ? ` in ${t.projects.join(', ')}` : ''}`);
      tokens.unshift(t);
      return wait<CreatedToken>({ token: withoutSecret(t), secret });
    },
    revokeToken: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const i = tokens.findIndex((t) => t.id === id);
      if (i < 0) return fail(err('NOT_FOUND', "That token doesn't exist or was already revoked.", 404));
      tokens.splice(i, 1);
      return wait(undefined);
    },

    changePassword: (currentPassword, newPassword) => {
      const blocked = guard();
      if (blocked) return blocked;
      if (!admin || currentPassword !== admin.password) return validationFailed([{ field: 'currentPassword', code: 'NOT_ALLOWED', message: "That isn't your current password." }]);
      if ([...newPassword].length < RULES.passwordMin) return validationFailed([{ field: 'newPassword', code: 'OUT_OF_RANGE', message: 'Use at least 12 characters.' }]);
      admin = { ...admin, password: newPassword };
      save();
      record('password_changed', `user/${admin.email}`);
      return wait(undefined);
    },
    getInvite: (code) => {
      const u = people.find((p) => p.code && p.code === code);
      return u ? wait({ email: u.email, kind: u.status === 'invited' ? 'invite' : 'reset' }) : fail(err('NOT_FOUND', 'This link was used already or has expired. Ask for a new one.', 404));
    },
    acceptInvite: (code, password) => {
      const u = people.find((p) => p.code && p.code === code);
      if (!u) return fail(err('NOT_FOUND', 'This link was used already or has expired. Ask for a new one.', 404));
      if ([...password].length < RULES.passwordMin) return validationFailed([{ field: 'password', code: 'OUT_OF_RANGE', message: 'Use at least 12 characters.' }]);
      Object.assign(u, { status: 'active', password, code: undefined, lastSignInAt: now() });
      record('invite_accepted', `user/${u.email}`);
      // The mock has one signed-in person: the demo admin keeps the session.
      return wait({ user: { id: u.id, email: u.email }, machineName: admin?.machineName ?? '', authMethod: 'session' as const, scope: 'full' as const,
        machineRole: u.machineRole, projectRoles: { ...u.projectRoles }, token: null });
    },

    listProjects: () => guard() ?? wait([...projects.values()].map((p) => ({ ...p, resources: resourcesIn(p.name).length }))),
    createProject: (name, description) => {
      const blocked = guard();
      if (blocked) return blocked;
      const n = validateName(name);
      if (n) return validationFailed([{ field: 'name', code: 'INVALID_FORMAT', message: n }]);
      if ([...description].length > RULES.projectDescriptionMax) return validationFailed([{ field: 'description', code: 'TOO_LONG', message: 'Use at most 280 characters.' }]);
      if (projects.has(name)) return fail(err('NAME_TAKEN', `A project named ${name} already exists.`, 409, [{ field: 'name', code: 'NOT_ALLOWED', message: `A project named ${name} already exists.` }]));
      const p: Project = { name, description: description.trim(), createdAt: now(), resources: 0 };
      projects.set(name, p);
      record('project_created', `project/${name}`);
      return wait({ ...p });
    },
    updateProject: (name, description) => {
      const blocked = guard();
      if (blocked) return blocked;
      const p = projects.get(name);
      if (!p) return fail(err('NOT_FOUND', `There's no project named ${name}.`, 404));
      if ([...description].length > RULES.projectDescriptionMax) return validationFailed([{ field: 'description', code: 'TOO_LONG', message: 'Use at most 280 characters.' }]);
      p.description = description.trim();
      record('project_updated', `project/${name}`);
      return wait({ ...p, resources: resourcesIn(name).length });
    },
    deleteProject: (name) => {
      const blocked = guard();
      if (blocked) return blocked;
      if (!projects.has(name)) return fail(err('NOT_FOUND', `There's no project named ${name}.`, 404));
      if (name === RULES.defaultProject) return fail(err('ACTION_NOT_SUPPORTED', "The default project can't be deleted.", 409));
      const left = resourcesIn(name).filter((o) => !o.r.ownerId);
      if (left.length) {
        return fail(err('PROJECT_NOT_EMPTY', `${name} still has ${left.length} resource${left.length === 1 ? '' : 's'}. Delete them first.`, 409,
          left.map((o) => ({ field: `${o.r.kind}/${o.r.name}`, code: 'NOT_ALLOWED' as const, message: `The ${o.r.kind} ${o.r.name} is in it.` }))));
      }
      projects.delete(name);
      for (const u of people) u.projectRoles = Object.fromEntries(Object.entries(u.projectRoles).filter(([p]) => p !== name));
      record('project_deleted', `project/${name}`);
      return wait(undefined);
    },
    listUsers: () => guard() ?? wait([adminSummary(), ...people.map(publicUser)]),
    inviteUser: (input) => {
      const blocked = guard();
      if (blocked) return blocked;
      const email = input.email.trim().toLowerCase();
      const details: FieldError[] = [];
      if (validateEmail(email)) details.push({ field: 'email', code: 'INVALID_FORMAT', message: 'Enter an email address like you@example.com.' });
      details.push(...checkRoles(input.machineRole, input.projectRoles));
      if (!input.machineRole && !Object.keys(input.projectRoles ?? {}).length) details.push({ field: 'machineRole', code: 'REQUIRED', message: 'Give them a machine role or a role in at least one project.' });
      if (details.length) return validationFailed(details);
      if (email === admin?.email.toLowerCase() || people.some((p) => p.email === email)) {
        return fail(err('NAME_TAKEN', `${email} already has an account here.`, 409, [{ field: 'email', code: 'NOT_ALLOWED', message: `${email} already has an account here.` }]));
      }
      const u: UserSummary & { code?: string } = { id: 'usr_' + Math.random().toString(36).slice(2, 12), email, machineRole: input.machineRole ?? null,
        projectRoles: { ...(input.projectRoles ?? {}) }, status: 'invited', createdAt: now(), lastSignInAt: null };
      people.push(u);
      record('user_invited', `user/${email}`, [u.machineRole ? `machine ${u.machineRole}` : '', ...Object.entries(u.projectRoles).map(([p, r]) => `${p}=${r}`)].filter(Boolean).join(', '));
      return wait(invitation(u));
    },
    updateUser: (id, input) => {
      const blocked = guard();
      if (blocked) return blocked;
      if (id === 'usr_mock') return fail(err('ACTION_NOT_SUPPORTED', "You can't change your own roles. Ask another admin.", 409));
      const u = userById(id);
      if (!u) return fail(err('NOT_FOUND', "That person doesn't have an account here.", 404));
      const details = checkRoles(input.machineRole, input.projectRoles);
      if (details.length) return validationFailed(details);
      if (input.machineRole !== undefined) u.machineRole = input.machineRole;
      const roles: Record<string, Role | null> = { ...u.projectRoles, ...(input.projectRoles ?? {}) };
      u.projectRoles = Object.fromEntries(Object.entries(roles).filter((e): e is [string, Role] => e[1] !== null));
      record('user_updated', `user/${u.email}`);
      return wait(publicUser(u));
    },
    removeUser: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      if (id === 'usr_mock') return fail(err('ACTION_NOT_SUPPORTED', "You can't remove yourself. Ask another admin.", 409));
      const i = people.findIndex((p) => p.id === id);
      if (i < 0) return fail(err('NOT_FOUND', "That person doesn't have an account here.", 404));
      const [gone] = people.splice(i, 1);
      record('user_removed', `user/${gone?.email ?? ''}`);
      return wait(undefined);
    },
    resetUser: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const u = userById(id);
      if (!u) return fail(err('NOT_FOUND', "That person doesn't have an account here.", 404));
      record(u.status === 'invited' ? 'invite_renewed' : 'password_reset_issued', `user/${u.email}`);
      return wait(invitation(u, u.status === 'active'));
    },
    listAudit: (cursor) => {
      const blocked = guard();
      if (blocked) return blocked;
      const start = cursor ? Number(cursor) : 0;
      const page = audit.slice(start, start + 50);
      return wait({ items: clone(page), nextCursor: start + 50 < audit.length ? String(start + 50) : null });
    },

    getMachine: () => {
      const blocked = guard();
      if (blocked) return blocked;
      let reserved = 0;
      let reservedCpus = 0;
      const broker = [...store.values()].some((o) => (o.r.kind === 'queue' || o.r.kind === 'topic') && o.r.status.phase !== 'deleting');
      if (broker) reserved += 512 * 2 ** 20;
      for (const { r } of store.values()) {
        if (r.kind === 'app') reserved += ((r.spec as AppSpec).memoryMiB ?? RULES.appMemoryDefault) * 2 ** 20;
        if (r.kind === 'database') reserved += { postgres: 512, mysql: 1024, mongodb: 1024, clickhouse: 2048 }[(r.spec as DatabaseSpec).engine] * 2 ** 20;
        if (r.kind === 'search') reserved += ((r.spec as SearchSpec).memoryMiB ?? RULES.searchMemoryDefault) * 2 ** 20;
        if (r.kind === 'email' || r.kind === 'cdn') reserved += 128 * 2 ** 20;
        if (r.kind === 'transcoder') reserved += ((r.spec as TranscoderSpec).memoryMiB ?? RULES.transcodeMemoryDefault) * ((r.spec as TranscoderSpec).concurrency ?? 1) * 2 ** 20;
        if (r.kind === 'cache') reserved += (((r.spec as { maxMemoryMiB?: number }).maxMemoryMiB ?? RULES.cacheMemoryDefault) + 64) * 2 ** 20;
        if (r.kind === 'vm' && (r.spec as VMSpec).power !== 'stopped') {
          reserved += (((r.spec as VMSpec).memoryMiB ?? RULES.vmMemoryDefault) + 64) * 2 ** 20;
          reservedCpus += (r.spec as VMSpec).cpus ?? RULES.vmCpusDefault;
        }
      }
      const vms = [...store.values()].filter((o) => o.r.kind === 'vm');
      const imageNames = [...new Set(vms.map((o) => (o.r.spec as VMSpec).image).filter((i) => !i.startsWith(RULES.snapshotImagePrefix)))];
      const snapshotBytes = [...store.values()].filter((o) => o.r.kind === 'snapshot').reduce((n, o) => n + Number(o.r.outputs.sizeBytes ?? 0), 0);
      const anyDomain = [...store.values()].some((o) => o.r.kind === 'domain' && o.r.status.phase !== 'deleting');
      const m: Machine = { name: admin?.machineName ?? '', hostname: 'enclave.local', version: '0.3.0-mock', os: 'darwin', arch: 'arm64', cpus: 8,
        memoryTotalBytes: 16 * 2 ** 30, memoryReservedBytes: reserved, engine: { driver: 'mock', available: true, version: 'mock', message: '' },
        publicAddress: settings.publicAddress, acmeEmail: settings.acmeEmail, inviteEmail: settings.inviteEmail ? { ...settings.inviteEmail } : null,
        edge: { available: anyDomain, message: anyDomain ? '' : 'The edge proxy starts with the first domain.' },
        vms: options.noVMs ? { available: false, message: "QEMU isn't installed. Install QEMU: brew install qemu" } : { available: true, message: 'VMs can run: QEMU 11.1 with HVF.', qemuVersion: '11.1', accelerator: 'hvf' },
        reservedCpus, snapshotBytes,
        broker: broker ? { available: true, message: '' } : { available: false, message: 'No queue or topic exists, so the broker isn\'t running.' },
        images: imageNames.map((name) => ({ name, arch: 'arm64', sizeBytes: 341_245_952, usedBy: vms.filter((o) => (o.r.spec as VMSpec).image === name).length })) };
      return wait(m);
    },
    updateMachine: async (input) => {
      const blocked = guard();
      if (blocked) return blocked;
      const clean = (v: string | null) => (v && v.trim() ? v.trim() : null);
      const details = [];
      if (clean(input.publicAddress) && /[\s/]/.test(clean(input.publicAddress) ?? '')) details.push({ field: 'publicAddress', code: 'INVALID_FORMAT' as const, message: 'Use an IP address or a hostname.' });
      if (clean(input.acmeEmail) && validateEmail(clean(input.acmeEmail) ?? '')) details.push({ field: 'acmeEmail', code: 'INVALID_FORMAT' as const, message: 'Enter an email address like you@example.com.' });
      const ie = input.inviteEmail ? { project: input.inviteEmail.project?.trim() || RULES.defaultProject, service: input.inviteEmail.service.trim(), from: input.inviteEmail.from.trim() } : undefined;
      if (ie) {
        if (validateName(ie.service)) details.push({ field: 'inviteEmail.service', code: 'REQUIRED' as const, message: 'Choose the email service that sends invites.' });
        else if (!within(ie.project, () => byName('email', ie.service))) details.push({ field: 'inviteEmail.service', code: 'NOT_FOUND' as const, message: `There's no email service named ${ie.service} in the project ${ie.project}.` });
        if (validateEmail(ie.from)) details.push({ field: 'inviteEmail.from', code: 'INVALID_FORMAT' as const, message: 'Enter the sender, like enclave@example.com.' });
      }
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      if (ie) settings.inviteEmail = ie;
      else if (input.inviteEmail === null) settings.inviteEmail = null;
      settings.publicAddress = clean(input.publicAddress);
      settings.acmeEmail = clean(input.acmeEmail);
      for (const s of store.values()) if (s.r.kind === 'domain' || s.r.kind === 'record') s.r.outputs = outputs(s.r);
      return api.getMachine();
    },
    getRootCA: () => {
      const blocked = guard();
      if (blocked) return blocked;
      const any = [...store.values()].some((o) => o.r.kind === 'domain' && (o.r.spec as DomainSpec).tls === 'internal');
      return any ? wait(MOCK_CA) : fail(err('NOT_FOUND', "There's no internal certificate authority yet. It's created with the first domain that uses internal TLS.", 404));
    },
    listResources: (kind, project) => {
      if (project && !projects.has(project)) return fail(err('NOT_FOUND', `There's no project named ${project}.`, 404));
      return guard() ?? wait([...store.values()].filter((s) => (!kind || s.r.kind === kind) && (!project || s.r.project === project)).map((s) => clone(s.r)));
    },
    getResource: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      return s ? wait(clone(s.r)) : notFound();
    },
    createResource: (kind, name, spec) => {
      const blocked = guard();
      if (blocked) return blocked;
      const full = normalize(kind, spec as unknown as Record<string, unknown>);
      const nameErr = validateName(name);
      const details = [...(nameErr ? [{ field: 'name', code: 'INVALID_FORMAT' as const, message: nameErr }] : []), ...checkSpec(kind, full)];
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      const taken = (k: Kind, n: string) => err('NAME_TAKEN', `A ${k} named ${n} already exists.`, 409, [{ field: 'name', code: 'NOT_ALLOWED', message: `A ${k} named ${n} already exists.` }]);
      if (byName(kind, name)) return fail(taken(kind, name));
      if (kind === 'domain' || kind === 'cdn') {
        const dup = hostnameTaken((full as unknown as DomainSpec).hostname);
        if (dup) return fail(dup);
      }
      // Managed passwords: <name>-password for databases, <name>-<kind>-password for the v0.6 kinds (L3-005 §8).
      const managed = kind === 'database' ? name + MANAGED.passwordSecretSuffix
        : ['queue', 'topic', 'search', 'email'].includes(kind) ? `${name}-${kind}${MANAGED.passwordSecretSuffix}` : '';
      if (managed && byName('secret', managed)) return fail(taken('secret', managed));
      const src = kind === 'secret' ? (spec as { source?: SecretSource }).source : undefined;
      const answer = src ? storeAnswer(byName('secretstore', src.store), src) : undefined;
      const secretValue = kind === 'secret' ? (answer && 'value' in answer ? answer.value : (spec as { value?: string }).value) : undefined;
      const s = insert(kind, name, full, null, secretValue);
      if (answer && 'refused' in answer) failSoon(s, answer.refused);
      if (managed) insert('secret', managed, {}, s.r.id, Math.random().toString(36).slice(2) + '-Hc7');
      if ((kind === 'queue' || kind === 'topic') && !byName('secret', MANAGED.brokerCredentials)) insert('secret', MANAGED.brokerCredentials, {}, MANAGED.systemOwner, 'mock');
      if (kind === 'queue') s.messages = [];
      if (kind === 'topic') Object.assign(s, { published: 0, deliveries: {} });
      if (kind === 'email') s.emails = [];
      if (kind === 'bucket' && !byName('secret', MANAGED.objectStoreCredentials)) insert('secret', MANAGED.objectStoreCredentials, {}, MANAGED.systemOwner, '{"accessKey":"mock","secretKey":"mock"}');
      return wait(clone(s.r));
    },
    updateResource: (id, patch, ifMatch) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      const r = s.r;
      if (r.status.phase === 'deleting') return fail(err('RESOURCE_BUSY', 'This resource is being deleted.', 409));
      if (ifMatch !== r.generation) return fail(err('PRECONDITION_FAILED', 'This resource changed since you read it. Reload and try again.', 412));
      if (r.ownerId) return fail(err('IMMUTABLE_FIELD', 'EnclaveStack manages this secret; it changes with its owner.', 422));
      const prior: Record<string, unknown> = r.kind === 'secret' ? ((r.spec as { source?: SecretSource }).source ? { ...r.spec } : { value: s.secret }) : { ...(r.spec as unknown as Record<string, unknown>) };
      // Fields present replace; null clears (L3-002 §2).
      const base = Object.fromEntries(Object.entries({ ...prior, ...patch }).filter(([, v]) => v !== null));
      const full = normalize(r.kind, base);
      if (r.kind === 'database' && (full as unknown as DatabaseSpec).engine !== (r.spec as DatabaseSpec).engine) {
        return fail(err('IMMUTABLE_FIELD', "The engine can't change. Create a new database instead.", 422, [{ field: 'spec.engine', code: 'IMMUTABLE', message: "The engine can't change. Create a new database instead." }]));
      }
      if (r.kind === 'database' && (full as unknown as DatabaseSpec).version !== (r.spec as DatabaseSpec).version) {
        return fail(err('IMMUTABLE_FIELD', 'Changing the version needs a new database.', 422, [{ field: 'spec.version', code: 'IMMUTABLE', message: 'Changing the version needs a new database.' }]));
      }
      if (r.kind === 'search' && (full as unknown as SearchSpec).version !== (r.spec as SearchSpec).version) {
        return fail(err('IMMUTABLE_FIELD', 'Changing the version needs a new search service.', 422, [{ field: 'spec.version', code: 'IMMUTABLE', message: 'Changing the version needs a new search service.' }]));
      }
      if (r.kind === 'cdn' && (full as unknown as CDNSpec).hostname !== (r.spec as CDNSpec).hostname) {
        return fail(err('IMMUTABLE_FIELD', "The hostname can't change. Create a new CDN instead.", 422, [{ field: 'spec.hostname', code: 'IMMUTABLE', message: "The hostname can't change. Create a new CDN instead." }]));
      }
      if (r.kind === 'domain' && (full as unknown as DomainSpec).hostname !== (r.spec as DomainSpec).hostname) {
        return fail(err('IMMUTABLE_FIELD', "The hostname can't change. Create a new domain instead.", 422, [{ field: 'spec.hostname', code: 'IMMUTABLE', message: "The hostname can't change. Create a new domain instead." }]));
      }
      if (r.kind === 'backup' && JSON.stringify((full as unknown as BackupSpec).source) !== JSON.stringify((r.spec as BackupSpec).source)) {
        const msg = "A backup's source can't change. Create a new backup instead.";
        return fail(err('IMMUTABLE_FIELD', msg, 422, [{ field: 'spec.source', code: 'IMMUTABLE', message: msg }]));
      }
      const immutable = (field: string, msg: string) => fail(err('IMMUTABLE_FIELD', msg, 422, [{ field, code: 'IMMUTABLE', message: msg }]));
      if (r.kind === 'vm') {
        const a = full as unknown as VMSpec;
        const b = r.spec as VMSpec;
        if (a.image !== b.image) return immutable('spec.image', "A VM's image can't change. Create a new VM instead.");
        if (JSON.stringify(a.sshKeys ?? []) !== JSON.stringify(b.sshKeys ?? []) || (a.sshKeySecret ?? '') !== (b.sshKeySecret ?? '')) {
          return immutable('spec.sshKeys', 'SSH keys are set at the first boot and can\'t change. Add keys inside the VM, or create a new VM.');
        }
        if ((a.userData ?? '') !== (b.userData ?? '')) return immutable('spec.userData', "The first-boot script ran already and can't change. Create a new VM to run another.");
        if ((a.diskGiB ?? 0) < (b.diskGiB ?? 0)) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'spec.diskGiB', code: 'GROW_ONLY', message: `The root disk can only grow; it is ${b.diskGiB ?? 0} GiB now.` }]));
      }
      if (r.kind === 'volume') {
        const a = full as unknown as VolumeSpec;
        const b = r.spec as VolumeSpec;
        if (a.type !== b.type || (a.snapshot ?? '') !== (b.snapshot ?? '')) return immutable('spec.type', "A volume's type and snapshot can't change. Create a new volume instead.");
        if ((a.sizeGiB ?? 0) < (b.sizeGiB ?? 0)) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'spec.sizeGiB', code: 'GROW_ONLY', message: `A disk can only grow; it is ${b.sizeGiB ?? 0} GiB now.` }]));
      }
      if (r.kind === 'snapshot' && (full as unknown as SnapshotSpec).source !== (r.spec as SnapshotSpec).source) return immutable('spec.source', 'A snapshot never changes. Take a new snapshot instead.');
      if (r.kind === 'app' && !(full as unknown as AppSpec).port) {
        const routed = domainsOf(r.name).find((d) => !(d.r.spec as DomainSpec).port);
        if (routed) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'spec.port', code: 'REQUIRED', message: `The domain ${routed.r.name} routes to this app's port. Keep a port, or set one on the domain first.` }]));
      }
      const details = checkSpec(r.kind, full);
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      let refused = '';
      if (r.kind === 'secret' && full.source) {
        const answer = storeAnswer(byName('secretstore', (full.source as SecretSource).store), full.source as SecretSource);
        if ('value' in answer) s.secret = answer.value;
        else refused = answer.refused;
        r.spec = { source: full.source } as SpecByKind[Kind];
      } else if (r.kind === 'secret') {
        s.secret = full.value as string;
        r.spec = {} as SpecByKind[Kind];
      }
      else r.spec = full as unknown as SpecByKind[Kind];
      r.generation++;
      r.updatedAt = now();
      addEvent(s, 'updated', `Spec changed to generation ${r.generation}.`);
      reconcile(s, r.status.observedGeneration > 0 ? 'updating' : 'provisioning');
      if (refused) failSoon(s, refused);
      return wait(clone(r));
    },
    deleteResource: (id, ifMatch) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      const r = s.r;
      if (ifMatch !== undefined && ifMatch !== r.generation) return fail(err('PRECONDITION_FAILED', 'This resource changed since you read it. Reload and try again.', 412));
      if (r.ownerId) {
        const owner = r.ownerId === MANAGED.systemOwner ? 'EnclaveStack' : `the ${store.get(r.ownerId)?.r.kind ?? ''} ${store.get(r.ownerId)?.r.name ?? ''}`;
        return fail(err('RESOURCE_IN_USE', `This secret is managed by ${owner} and is deleted with it.`, 409));
      }
      const jobUsers = [...store.values()].filter((o) => o.r.kind === 'job' && (o.r.spec as JobSpec).app === r.name);
      if (r.kind === 'app' && jobUsers.length) {
        return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
          jobUsers.map((j) => ({ field: `job/${j.r.name}`, code: 'NOT_ALLOWED' as const, message: `Run by the job ${j.r.name}.` }))));
      }
      if (r.kind === 'app' && domainsOf(r.name).length) {
        return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
          domainsOf(r.name).map((d) => ({ field: `domain/${d.r.name}`, code: 'NOT_ALLOWED' as const, message: `Served by the domain ${d.r.name}.` }))));
      }
      const backupUsers = backupsUsing(r);
      if (backupUsers.length) {
        return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
          backupUsers.map((b) => ({ field: `backup/${b.r.name}`, code: 'NOT_ALLOWED' as const, message: `Used by the backup ${b.r.name}.` }))));
      }
      const mounts = r.kind === 'volume' ? volumeUsers(r.name) : [];
      if (mounts.length) {
        return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
          mounts.map((u) => ({ field: `${u.r.kind}/${u.r.name}`, code: 'NOT_ALLOWED' as const, message: `Used by the ${u.r.kind === 'vm' ? 'VM' : u.r.kind} ${u.r.name}.` }))));
      }
      const v04 = v04Users(r);
      if (v04.length) {
        return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
          v04.map((u) => ({ field: `${u.r.kind}/${u.r.name}`, code: 'NOT_ALLOWED' as const, message: `Used by the ${u.r.kind === 'loadbalancer' ? 'load balancer' : u.r.kind} ${u.r.name}.` }))));
      }
      if (r.kind === 'secret') {
        const users = [...store.values()].filter((o) => o.r.kind === 'app' && (Object.values((o.r.spec as AppSpec).secretEnv ?? {}).includes(r.name)
          || (o.r.spec as AppSpec).source?.credentialsSecret === r.name));
        if (users.length) {
          return fail(err('RESOURCE_IN_USE', `${r.name} is still used. Remove it from these first.`, 409,
            users.map((u) => ({ field: `app/${u.r.name}`, code: 'NOT_ALLOWED' as const, message: `Used by the app ${u.r.name}.` }))));
        }
      }
      if (s.timer) clearTimeout(s.timer);
      r.status = { ...r.status, phase: 'deleting', lastTransitionAt: now() };
      addEvent(s, 'deleting', 'Deletion requested.');
      setTimeout(() => {
        store.delete(id);
        for (const [cid, child] of store) if (child.r.ownerId === id) store.delete(cid);
      }, stepMs);
      return wait(clone(r));
    },
    action: (id, action) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      const r = s.r;
      if (r.status.phase === 'deleting') return fail(err('RESOURCE_BUSY', 'This resource is being deleted.', 409));
      if (action === 'reconcile') {
        addEvent(s, 'action_reconcile', 'Reconcile requested.');
        reconcile(s, r.status.observedGeneration > 0 ? 'updating' : 'provisioning');
        return wait(clone(r));
      }
      if (action === 'refresh') {
        const src = (r.spec as { source?: SecretSource }).source;
        if (r.kind !== 'secret' || !src) return fail(err('ACTION_NOT_SUPPORTED', 'Only a secret read from a secret store can be refreshed.', 409));
        const answer = storeAnswer(byName('secretstore', src.store), src);
        addEvent(s, 'action_refresh', 'Refresh requested.');
        if ('refused' in answer) failSoon(s, answer.refused);
        else {
          if (answer.value !== s.secret) {
            s.secret = answer.value;
            addEvent(s, 'secret_synced', `New value from ${src.store}.`);
          }
          r.outputs = { ...r.outputs, syncedAt: now() };
        }
        return wait(clone(r));
      }
      if (action === 'start' || action === 'stop') {
        // Power is spec: a new generation every client sees (VM-06).
        if (r.kind !== 'vm') return fail(err('ACTION_NOT_SUPPORTED', `Only a VM can be ${action === 'start' ? 'started' : 'stopped'}; a ${r.kind} can't.`, 409));
        const power = action === 'start' ? 'running' : 'stopped';
        if ((r.spec as VMSpec).power === power) return wait(clone(r));
        r.spec = { ...(r.spec as VMSpec), power } as SpecByKind[Kind];
        r.generation++;
        addEvent(s, `action_${action}`, action === 'start' ? 'Start requested.' : 'Stop requested.');
        reconcile(s, 'updating');
        return wait(clone(r));
      }
      const ok = (action === 'restart' && ['app', 'database', 'cache', 'vm', 'search', 'email', 'cdn'].includes(r.kind)) || (action === 'redeploy' && r.kind === 'app')
        || (action === 'purge' && (r.kind === 'queue' || r.kind === 'cdn'));
      if (!ok) return fail(err('ACTION_NOT_SUPPORTED', `A ${r.kind} can't be ${{ restart: 'restarted', redeploy: 'redeployed', purge: 'purged' }[action as 'restart' | 'redeploy' | 'purge']}.`, 409));
      if (r.status.phase !== 'ready') return fail(err('ACTION_NOT_SUPPORTED', "Wait until it's ready, or use Retry if it failed.", 409));
      if (action === 'purge') {
        if (r.kind === 'queue') s.messages = [];
        else r.outputs = { ...r.outputs, cachedBytes: 0 };
        addEvent(s, 'action_purge', 'Purge requested.');
        addEvent(s, 'purged', r.kind === 'queue' ? 'Every message was deleted.' : 'The cache was emptied.');
        return wait(clone(r));
      }
      addEvent(s, `action_${action}`, action === 'restart' ? 'Restart requested.' : 'Redeploy requested.');
      if (action === 'redeploy') reconcile(s, 'updating');
      else setTimeout(() => addEvent(s, 'restarted', 'Restarted.'), stepMs);
      return wait(clone(r));
    },
    getEvents: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      return s ? wait(clone(s.events)) : notFound();
    },
    getLogs: (id, tail = 200, build = false) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (build) {
        if (s.r.kind !== 'app') return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no builds.`, 409));
        return wait((s.buildLog ?? []).slice(-tail).map((message) => ({ time: s.r.updatedAt, stream: 'stdout' as const, message })));
      }
      if (['bucket', 'secret', 'volume', 'snapshot', 'queue', 'topic'].includes(s.r.kind)) return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no logs.`, 409));
      if (s.r.kind === 'transcoder') return fail(err('ACTION_NOT_SUPPORTED', "A transcoder's output is kept with each run: see its runs.", 409));
      if (s.r.kind === 'vm') {
        const boot = ['UEFI firmware starting', `[    0.000000] Booting Linux on physical CPU 0x0`, `[   17.880000] cloud-init: Cloud-init finished`, `${s.r.name} login:`];
        return wait(boot.slice(-tail).map((message) => ({ time: s.r.updatedAt, stream: 'stdout' as const, message })));
      }
      if (s.r.kind === 'backup') return fail(err('ACTION_NOT_SUPPORTED', "A backup has no logs. Each run's result is in its runs.", 409));
      const t = Date.now();
      const lines: LogLine[] = [
        { time: new Date(t - 3000).toISOString(), stream: 'stdout', message: `${s.r.name} starting` },
        { time: new Date(t - 2000).toISOString(), stream: 'stdout', message: 'listening' },
        { time: new Date(t - 1000).toISOString(), stream: 'stderr', message: 'warning: running on the mock engine' },
      ];
      return wait(lines.slice(-tail));
    },
    revealSecret: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (s.r.kind !== 'secret') return fail(err('ACTION_NOT_SUPPORTED', 'Only secrets have a value to reveal.', 409));
      addEvent(s, 'secret_revealed', 'Value revealed.');
      return wait(s.secret ?? '');
    },

    runNow: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (s.r.kind === 'backup') {
        if (s.r.status.phase !== 'ready') return fail(err('ACTION_NOT_SUPPORTED', 'Wait until the backup is ready, or use Retry if it failed.', 409));
        if (s.runs?.some((x) => x.status === 'running')) return fail(err('RUN_IN_PROGRESS', 'A run of this backup is still going. Wait for it to finish.', 409));
        return wait(toRun(backupRun(s, 'manual')));
      }
      if (s.r.kind === 'transcoder') return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'key', code: 'REQUIRED', message: "Give the object's key to transcode, like videos/intro.mov." }]));
      if (s.r.kind !== 'job') return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} can't be run.`, 409));
      if (s.r.status.phase !== 'ready') return fail(err('ACTION_NOT_SUPPORTED', 'Wait until the job is ready, or use Retry if it failed.', 409));
      const j = s.r.spec as JobSpec;
      s.runs ??= [];
      if (j.overlap !== 'allow' && s.runs.some((x) => x.status === 'running')) {
        return fail(err('RUN_IN_PROGRESS', 'A run of this job is still going, and its overlap policy is forbid. Wait for it to finish.', 409));
      }
      const run: RunDetail = { id: 'run_' + Math.random().toString(36).slice(2, 18), trigger: 'manual', status: 'running', scheduledFor: null,
        startedAt: now(), finishedAt: null, exitCode: null, message: '', outputTail: '' };
      s.runs.unshift(run);
      addEvent(s, 'action_run', `Run ${run.id} requested.`);
      // Mock runs end after a step; commands containing "exit N" end with that code.
      setTimeout(() => {
        const cmd = (j.command ?? []).join(' ');
        const code = Number(/exit\s+(\d+)/.exec(cmd)?.[1] ?? 0);
        Object.assign(run, { status: code === 0 ? 'succeeded' : 'failed', finishedAt: now(), exitCode: code, message: `Exited with code ${code}.`,
          outputTail: `${s.r.name}: ${cmd || '(default command)'}\n` });
        s.r.outputs = { ...s.r.outputs, lastRunStatus: run.status, lastRunAt: run.finishedAt ?? now() };
        if (code !== 0) addEvent(s, 'run_failed', `Run ${run.id}: ${run.message}`);
      }, stepMs);
      return wait(toRun(run));
    },
    listRuns: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (s.r.kind !== 'job' && s.r.kind !== 'backup' && s.r.kind !== 'transcoder') return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no runs.`, 409));
      return wait((s.runs ?? []).map(toRun));
    },
    restore: (id, req) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      const details: FieldError[] = [];
      if (!req.runId) details.push({ field: 'runId', code: 'REQUIRED', message: 'Choose the backup run to restore.' });
      if (req.mode === 'new') {
        const n = validateName(req.name ?? '');
        if (n) details.push({ field: 'name', code: 'INVALID_FORMAT', message: n });
      } else if (req.mode === 'replace') {
        if (!req.confirmName) details.push({ field: 'confirmName', code: 'REQUIRED', message: 'Type the name of the resource whose data will be replaced.' });
      } else details.push({ field: 'mode', code: 'NOT_ALLOWED', message: 'Choose new or replace.' });
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      if (s.r.status.phase === 'deleting') return fail(err('RESOURCE_BUSY', 'This resource is being deleted.', 409));
      if (s.r.kind !== 'backup') return fail(err('ACTION_NOT_SUPPORTED', 'Only backups can be restored.', 409));
      const run = s.runs?.find((x) => x.id === req.runId);
      if (!run) return fail(err('RUN_NOT_RESTORABLE', "That backup run doesn't exist, or was removed by retention.", 409));
      if (run.status !== 'succeeded') return fail(err('RUN_NOT_RESTORABLE', 'Only a successful backup run can be restored.', 409));
      if (!run.artifact) return fail(err('RUN_NOT_RESTORABLE', "This run's backup was removed by retention. Choose a newer run.", 409));
      const b = s.r.spec as BackupSpec;
      const kind: Kind = b.source.cache ? 'cache' : 'database';
      const srcName = b.source.database ?? b.source.cache ?? '';
      const src = byName(kind, srcName);
      if (!src) return fail(err('RUN_NOT_RESTORABLE', `The ${kind} ${srcName} no longer exists. Restore into a new one instead.`, 409));
      if (req.mode === 'new') {
        return api.createResource(kind, req.name ?? '', clone(src.r.spec) as SpecByKind[typeof kind], s.r.project).then((created) => {
          const target = store.get(created.id);
          if (target) addEvent(target, 'restore_started', `Restoring from backup ${s.r.name} run ${run.id}.`);
          return created;
        });
      }
      if (req.confirmName !== src.r.name) {
        const msg = `Type ${src.r.name} to confirm that its data will be replaced.`;
        return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'confirmName', code: 'NOT_ALLOWED', message: msg }]));
      }
      if (src.r.status.phase !== 'ready') return fail(err('ACTION_NOT_SUPPORTED', `Wait until the ${kind} ${src.r.name} is ready.`, 409));
      addEvent(src, 'restore_requested', `Replace its data with backup ${s.r.name} run ${run.id}. A safety backup is taken first.`);
      backupRun(s, 'pre_restore');
      reconcile(src, 'updating');
      src.r.status.message = `Restoring from backup ${s.r.name}.`;
      return wait(clone(src.r));
    },
    getRun: (id, runId) => {
      const blocked = guard();
      if (blocked) return blocked;
      const run = store.get(id)?.runs?.find((x) => x.id === runId);
      return run ? wait({ ...run }) : fail(err('NOT_FOUND', "That run doesn't exist, or was removed by retention.", 404));
    },

    // v0.6 data endpoints (L3-002 §9.3), in memory.
    sendMessage: (id, message) => {
      const s = dataOf(id, ['queue', 'topic']);
      if (s instanceof Promise) return s;
      const limit = s.r.kind === 'queue' ? ((s.r.spec as QueueSpec).maxMessageBytes ?? RULES.messageBytesDefault) : RULES.messageBytesMax;
      if (new TextEncoder().encode(message.body).length > limit) {
        return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'body', code: 'TOO_LONG', message: `The body can be at most ${limit} bytes here.` }]));
      }
      if (s.r.kind === 'topic') {
        s.published = (s.published ?? 0) + 1;
        for (const sub of (s.r.spec as TopicSpec).subscriptions ?? []) {
          const target = sub.queue ? `queue/${sub.queue}` : (sub.url ?? '');
          if (sub.queue) enqueue(byName('queue', sub.queue), message);
          s.deliveries = { ...s.deliveries, [target]: (s.deliveries?.[target] ?? 0) + 1 };
        }
        return wait({ id: String(s.published), duplicate: false });
      }
      const dup = message.deduplicationId ? s.messages?.find((m) => m.attributes['__dedup'] === message.deduplicationId) : undefined;
      if (dup) return wait({ id: dup.id, duplicate: true });
      return wait({ id: enqueue(s, message), duplicate: false });
    },
    listMessages: (id, limit = RULES.peekLimitDefault) => {
      const s = dataOf(id, ['queue']);
      if (s instanceof Promise) return s;
      const msgs = s.messages ?? [];
      const inFlight = msgs.filter((m) => m.invisibleUntil > Date.now()).length;
      const oldest = msgs[0] ? Math.floor((Date.now() - Date.parse(msgs[0].sentAt)) / 1000) : null;
      return wait({ stats: { messages: msgs.length - inFlight, inFlight, oldestAgeSeconds: oldest, deadLettered: 0 },
        items: msgs.slice(0, limit).map((m) => ({ id: m.id, body: m.body, attributes: publicAttrs(m.attributes), sentAt: m.sentAt })) });
    },
    receiveMessages: (id, max = 1) => {
      const s = dataOf(id, ['queue']);
      if (s instanceof Promise) return s;
      const visibility = ((s.r.spec as QueueSpec).visibilityTimeoutSeconds ?? RULES.queueVisibilityDefault) * 1000;
      const got = (s.messages ?? []).filter((m) => m.invisibleUntil <= Date.now()).slice(0, max);
      for (const m of got) {
        m.receiveCount++;
        m.invisibleUntil = Date.now() + visibility;
        m.receipt = `mock-${m.id}-${m.receiveCount}`;
      }
      return wait(got.map((m) => ({ id: m.id, body: m.body, attributes: publicAttrs(m.attributes), sentAt: m.sentAt, receipt: m.receipt, receiveCount: m.receiveCount })));
    },
    deleteMessages: (id, receipts) => {
      const s = dataOf(id, ['queue']);
      if (s instanceof Promise) return s;
      let deleted = 0;
      const failed: { receipt: string; message: string }[] = [];
      for (const rc of receipts) {
        const i = (s.messages ?? []).findIndex((m) => m.receipt === rc);
        if (i < 0) failed.push({ receipt: rc, message: "This receipt isn't one of this queue's, or its message is already deleted." });
        else {
          s.messages?.splice(i, 1);
          deleted++;
        }
      }
      return wait({ deleted, failed });
    },
    getDeliveries: (id) => {
      const s = dataOf(id, ['topic']);
      if (s instanceof Promise) return s;
      return wait({ published: s.published ?? 0, items: ((s.r.spec as TopicSpec).subscriptions ?? []).map((sub) => {
        const target = sub.queue ? `queue/${sub.queue}` : (sub.url ?? '');
        return { target, delivered: s.deliveries?.[target] ?? 0, pending: 0, failed: 0, lastError: '' };
      }) });
    },
    sendEmail: (id, email) => {
      const s = dataOf(id, ['email']);
      if (s instanceof Promise) return s;
      const spec = s.r.spec as EmailSpec;
      const details: FieldError[] = [];
      if (validateEmail(email.from)) details.push({ field: 'from', code: 'INVALID_FORMAT', message: 'Use an address like name@example.com.' });
      else if (spec.domain && email.from.split('@')[1]?.toLowerCase() !== spec.domain) details.push({ field: 'from', code: 'NOT_ALLOWED', message: `This email service sends from @${spec.domain} only.` });
      if (!email.to.length) details.push({ field: 'to', code: 'REQUIRED', message: 'Add at least one recipient.' });
      if (!email.subject.trim()) details.push({ field: 'subject', code: 'REQUIRED', message: 'Enter a subject.' });
      if (!email.text && !email.html) details.push({ field: 'text', code: 'REQUIRED', message: 'Write a text or an HTML body.' });
      if (details.length) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, details));
      const e: Email = { id: 'msg' + String((s.emails ?? []).length + 1).padStart(6, '0'), from: email.from, to: [...email.to], subject: email.subject, sentAt: now(),
        sizeBytes: (email.text ?? '').length + (email.html ?? '').length + 400, relay: spec.mode === 'relay' ? 'relayed' : 'captured',
        cc: email.cc ?? [], bcc: email.bcc ?? [], replyTo: email.replyTo ?? '', text: email.text ?? '', html: email.html ?? '', headers: { Subject: [email.subject], From: [email.from] } };
      (s.emails ??= []).push(e);
      return wait({ id: e.id });
    },
    listEmails: (id, limit = RULES.emailListDefault) => {
      const s = dataOf(id, ['email']);
      if (s instanceof Promise) return s;
      const all = [...(s.emails ?? [])].reverse();
      return wait({ total: all.length, items: all.slice(0, limit).map(({ id: eid, from, to, subject, sentAt, sizeBytes, relay }) => ({ id: eid, from, to, subject, sentAt, sizeBytes, relay })) });
    },
    getEmail: (id, emailId) => {
      const s = dataOf(id, ['email']);
      if (s instanceof Promise) return s;
      const e = s.emails?.find((x) => x.id === emailId);
      return e ? wait(clone(e)) : fail(err('NOT_FOUND', "That message doesn't exist, or the service no longer keeps it.", 404));
    },
    getMetrics: (id, metrics, from, to) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      return wait(mockMetrics(s.r.id, s.r.kind, metrics, from, to, Math.floor(Date.parse(s.r.createdAt) / 1000) - 6 * 3600));
    },
    getMachineMetrics: (metrics, from, to) => guard() ?? wait(mockMetrics('machine', 'machine', metrics, from, to, 0)),
    test: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      const r = s.r;
      const deliver = (channel: Stored, event: NotificationRecord['event'], alert: string): { target: string; ok: boolean; message: string } => {
        const c = channel.r.spec as ChannelSpec;
        const target = c.webhook?.url ?? c.email?.to.join(', ') ?? c.type;
        const ok = !target.includes('fail');
        const n: NotificationRecord = { id: 'ntf_' + Math.random().toString(36).slice(2, 12), event, alert, subject: alert ? `alert/${alert}` : `channel/${channel.r.name}`,
          status: ok ? 'sent' : 'failed', attempts: 1, lastError: ok ? '' : 'connection refused', createdAt: now(), sentAt: ok ? now() : null };
        const list = deliveries.get(channel.r.id) ?? [];
        deliveries.set(channel.r.id, [n, ...list].slice(0, 50));
        channel.r.outputs = { ...channel.r.outputs, lastDeliveryStatus: n.status, lastDeliveryAt: now() };
        return { target: channel.r.name, ok, message: ok ? `Delivered to ${target}.` : `Couldn't deliver to ${target}: connection refused.` };
      };
      return within(r.project, () => {
        if (r.kind === 'channel') {
          const item = deliver(s, 'test', '');
          return wait<TestResult>({ ok: item.ok, items: [item] });
        }
        if (r.kind === 'alert') {
          const items = (r.spec as AlertSpec).channels.flatMap((c) => {
            const ch = byName('channel', c);
            return ch ? [deliver(ch, 'test', r.name)] : [{ target: c, ok: false, message: `There's no channel named ${c}.` }];
          });
          return wait<TestResult>({ ok: items.every((i) => i.ok), items });
        }
        if (r.kind === 'secretstore') {
          const sp = r.spec as SecretStoreSpec;
          const ok = !JSON.stringify(sp[sp.provider] ?? {}).includes('fail');
          r.outputs = { ...r.outputs, checkedAt: now() };
          return wait<TestResult>({ ok, items: [{ target: r.name, ok, message: ok ? `Signed in to ${sp.provider}.` : `The ${sp.provider} store refused: permission denied.` }] });
        }
        return fail(err('ACTION_NOT_SUPPORTED', `A ${r.kind} has nothing to test.`, 409));
      });
    },
    listIncidents: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (s.r.kind !== 'alert') return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no incidents.`, 409));
      return wait(clone(incidents.get(id) ?? []));
    },
    listNotifications: (id) => {
      const blocked = guard();
      if (blocked) return blocked;
      const s = store.get(id);
      if (!s) return notFound();
      if (s.r.kind !== 'channel') return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no deliveries.`, 409));
      return wait(clone(deliveries.get(id) ?? []));
    },
    transcode: (id, key) => {
      const s = dataOf(id, ['transcoder']);
      if (s instanceof Promise) return s;
      const t = s.r.spec as TranscoderSpec;
      const prefix = t.input.prefix ?? '';
      if (!key) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'key', code: 'REQUIRED', message: "Give the object's key, like videos/intro.mov." }]));
      if (!key.startsWith(prefix) || key.endsWith('/')) return fail(err('VALIDATION_FAILED', 'Check the highlighted fields.', 422, [{ field: 'key', code: 'NOT_ALLOWED', message: `Choose a file inside the input folder ${prefix}.` }]));
      const run: RunDetail = { id: 'run_' + Math.random().toString(36).slice(2, 18), trigger: 'manual', status: 'running', scheduledFor: null, startedAt: now(),
        finishedAt: null, exitCode: null, message: '', outputTail: '', key };
      (s.runs ??= []).unshift(run);
      addEvent(s, 'action_run', `Transcoding ${key} requested.`);
      // Mock conversions end after a step; a key containing "fail" isn't media.
      setTimeout(() => {
        const base = key.slice(prefix.length).replace(/\.[^./]*$/, '');
        const outPrefix = t.output.prefix ?? RULES.transcodeOutputDefault;
        const files = t.presets.flatMap((p) => p === 'hls-720p' ? [`${p}/index.m3u8`, `${p}/segment-000.ts`]
          : [`${p}.${{ 'mp4-480p': 'mp4', 'mp4-720p': 'mp4', 'mp4-1080p': 'mp4', 'webm-720p': 'webm', mp3: 'mp3', thumbnail: 'jpg' }[p as Exclude<typeof p, 'hls-720p'>]}`]);
        if (key.includes('fail')) Object.assign(run, { status: 'failed', exitCode: 1, finishedAt: now(), message: `FFmpeg couldn't convert ${key}: Invalid data found when processing input`,
          outputTail: `== ${t.presets[0] ?? ''}\n/in/input: Invalid data found when processing input\n` });
        else Object.assign(run, { status: 'succeeded', exitCode: 0, finishedAt: now(), message: `Converted ${key} into ${files.length} files.`,
          outputs: files.map((f) => `${outPrefix}${base}/${f}`), outputTail: t.presets.map((p) => `== ${p}`).join('\n') + '\n' });
        s.r.outputs = { ...s.r.outputs, lastRunStatus: run.status, lastRunAt: run.finishedAt ?? now() };
      }, stepMs);
      return wait(toRun(run));
    },
  };

  /** A ready resource of one of kinds, for a data endpoint; or the refusal to return. */
  function dataOf(id: string, kinds: Kind[]): Stored | Promise<never> {
    const blocked = guard();
    if (blocked) return blocked;
    const s = store.get(id);
    if (!s) return notFound();
    if (!kinds.includes(s.r.kind)) return fail(err('ACTION_NOT_SUPPORTED', `A ${s.r.kind} has no messages here.`, 409));
    if (s.r.status.phase !== 'ready' && s.r.status.phase !== 'updating') return fail(err('ACTION_NOT_SUPPORTED', `Wait until the ${s.r.kind} is ready, or use Retry if it failed.`, 409));
    return s;
  }

  function enqueue(q: Stored | undefined, m: { body: string; attributes?: Record<string, string>; deduplicationId?: string }): string {
    if (!q) return '';
    const msgs = (q.messages ??= []);
    const seq = String(Number(msgs.at(-1)?.id ?? q.published ?? 0) + 1);
    q.published = Number(seq);
    msgs.push({ id: seq, body: m.body, attributes: { ...(m.attributes ?? {}), ...(m.deduplicationId ? { __dedup: m.deduplicationId } : {}) }, sentAt: now(),
      receipt: '', receiveCount: 0, invisibleUntil: 0 });
    return seq;
  }

  // Names inside a request resolve in the resource's own project (ADR-035).
  const inner = { create: api.createResource, update: api.updateResource, remove: api.deleteResource, act: api.action };
  api.createResource = (kind, name, spec, project = RULES.defaultProject) => {
    if (signedIn && !projects.has(project)) return fail(err('NOT_FOUND', `There's no project named ${project}.`, 404));
    return (signedIn && refuse(project, 'editor')) || within(project, () => inner.create(kind, name, spec));
  };
  const projectOf = (id: string) => store.get(id)?.r.project ?? RULES.defaultProject;
  const gated = (id: string) => (signedIn && store.has(id) ? refuse(projectOf(id), 'editor') : null);
  api.updateResource = (id, patch, ifMatch) => gated(id) ?? within(projectOf(id), () => inner.update(id, patch, ifMatch));
  api.deleteResource = (id, ifMatch) => gated(id) ?? within(projectOf(id), () => inner.remove(id, ifMatch));
  api.action = (id, action) => gated(id) ?? within(projectOf(id), () => inner.act(id, action));
  const reveal = api.revealSecret;
  api.revealSecret = (id) => gated(id) ?? reveal(id);
  const list = api.listResources;
  api.listResources = (kind, project) => list(kind, project).then((rs) => rs.filter((r) => roleIn(session(), r.project) !== null));

  return api;
}

/** Attributes as the API shows them: without the mock's own bookkeeping. */
function publicAttrs(a: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(a).filter(([k]) => k !== '__dedup'));
}

function safeSessionStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

function load(storage: Storage | null): { admin: Admin | null; signedIn: boolean } | null {
  try {
    const raw = storage?.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as { admin: Admin | null; signedIn: boolean }) : null;
  } catch {
    return null;
  }
}
