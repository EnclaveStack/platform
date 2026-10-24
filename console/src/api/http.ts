import type { EnclaveApi } from './client';
import { HEADERS, type ErrorCode } from './generated';
import {
  ApiError,
  type AddedMachine,
  type AuditEntry,
  type CatalogueEntry,
  type DrainResult,
  type MachineSummary,
  type Incident,
  type Invitation,
  type InviteInfo,
  type MetricData,
  type NotificationRecord,
  type Project,
  type TestResult,
  type UserSummary,
  type CreatedToken,
  type DeletedMessages,
  type DeliveryStats,
  type Email,
  type EmailList,
  type Event,
  type FieldError,
  type LogLine,
  type Machine,
  type MachineSettings,
  type SetupStatus,
  type MessageList,
  type ObjectInfo,
  type ObjectList,
  type ReceivedMessage,
  type Resource,
  type ResourceList,
  type Run,
  type RunDetail,
  type SentMessage,
  type Session,
  type Token,
} from './types';

// The HTTP adapter for L3-002. Same-origin only: the control plane serves the console and /api/v1
// together, so the session cookie travels with every request and no CORS is involved. Every write
// carries the CSRF header; creates carry an Idempotency-Key so a retry can't duplicate (ADR-010).
// Transient failures are retried like the Go SDK (L3-007 §1).

export interface HttpOptions {
  fetch?: typeof fetch;
  sleep?: (ms: number) => Promise<void>;
  maxAttempts?: number;
  baseDelayMs?: number;
}

const RETRY_STATUSES = new Set([429, 502, 503, 504]);

export function newIdempotencyKey(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return 'es-' + Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

interface Call {
  method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  path: string;
  body?: unknown;
  ifMatch?: number;
  idempotent?: boolean;
  /** Return the body as text (e.g. a PEM certificate) instead of parsing JSON. */
  text?: boolean;
  /** A file sent as it is (an object upload), with headers of its own. */
  raw?: Blob;
  headers?: Record<string, string>;
}

export function createHttpApi(base: string, opts: HttpOptions = {}): EnclaveApi {
  const doFetch = opts.fetch ?? ((...args: Parameters<typeof fetch>) => fetch(...args));
  const sleep = opts.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const maxAttempts = opts.maxAttempts ?? 3;
  const baseDelay = opts.baseDelayMs ?? 250;

  async function call<T>(c: Call): Promise<T> {
    const headers: Record<string, string> = { Accept: c.text ? '*/*' : 'application/json' };
    if (c.body !== undefined) headers['Content-Type'] = 'application/json';
    if (c.raw !== undefined) Object.assign(headers, { 'Content-Type': 'application/octet-stream' }, c.headers);
    if (c.method !== 'GET') headers[HEADERS.csrf] = '1';
    if (c.ifMatch !== undefined) headers['If-Match'] = `"${c.ifMatch}"`;
    const key = c.method === 'POST' && c.idempotent ? newIdempotencyKey() : undefined;
    if (key) headers[HEADERS.idempotencyKey] = key;
    const safe = c.method === 'GET' || c.method === 'DELETE' || key !== undefined || (c.method === 'PATCH' && c.ifMatch !== undefined);

    let last: ApiError | undefined;
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      let res: Response;
      try {
        res = await doFetch(base + c.path, {
          method: c.method,
          headers,
          credentials: 'same-origin',
          body: c.raw ?? (c.body === undefined ? undefined : JSON.stringify(c.body)),
        });
      } catch {
        last = new ApiError('NETWORK', "Can't reach this machine. Check that EnclaveStack is running.", 0);
        if (safe && attempt < maxAttempts) {
          await sleep(backoff(attempt, baseDelay));
          continue;
        }
        throw last;
      }
      if (res.status === 204) return undefined as T;
      if (c.text && res.ok) return (await res.text()) as T;
      const payload: unknown = await res.json().catch(() => null);
      if (res.ok) return payload as T;
      last = toApiError(res, payload);
      if (safe && RETRY_STATUSES.has(res.status) && attempt < maxAttempts) {
        await sleep(last.retryAfterSeconds > 0 ? Math.min(last.retryAfterSeconds, 30) * 1000 : backoff(attempt, baseDelay));
        continue;
      }
      throw last;
    }
    throw last ?? new ApiError('NETWORK', 'Request failed.', 0);
  }

  const enc = encodeURIComponent;
  /** Each folder of a key escaped, the "/" between them kept (L3-002 §14.1). */
  const objectPath = (id: string, key: string) => `/resources/${enc(id)}/objects/${key.split('/').map(enc).join('/')}`;
  return {
    getSetupStatus: () => call<SetupStatus>({ method: 'GET', path: '/setup' }),
    setup: (input) => call<Session>({ method: 'POST', path: '/setup', body: input }),
    signIn: (email, password) => call<Session>({ method: 'POST', path: '/session', body: { email, password } }),
    getSession: async () => {
      try {
        return await call<Session>({ method: 'GET', path: '/session' });
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }
    },
    signOut: async () => {
      await call<unknown>({ method: 'DELETE', path: '/session' });
    },
    listTokens: async () => (await call<{ items: Token[] }>({ method: 'GET', path: '/tokens' })).items,
    createToken: (input) => call<CreatedToken>({ method: 'POST', path: '/tokens', body: input, idempotent: true }),
    revokeToken: async (id) => {
      await call<unknown>({ method: 'DELETE', path: `/tokens/${enc(id)}` });
    },
    changePassword: async (currentPassword, newPassword) => {
      await call<unknown>({ method: 'PUT', path: '/session/password', body: { currentPassword, newPassword } });
    },
    getInvite: (code) => call<InviteInfo>({ method: 'GET', path: `/invite?code=${enc(code)}` }),
    acceptInvite: (code, password) => call<Session>({ method: 'POST', path: '/invite', body: { code, password } }),
    listProjects: async () => (await call<{ items: Project[] }>({ method: 'GET', path: '/projects' })).items,
    createProject: (name, description) => call<Project>({ method: 'POST', path: '/projects', body: { name, description } }),
    updateProject: (name, description) => call<Project>({ method: 'PATCH', path: `/projects/${enc(name)}`, body: { description } }),
    deleteProject: async (name) => {
      await call<unknown>({ method: 'DELETE', path: `/projects/${enc(name)}` });
    },
    listUsers: async () => (await call<{ items: UserSummary[] }>({ method: 'GET', path: '/users' })).items,
    inviteUser: (input) => call<Invitation>({ method: 'POST', path: '/users', body: input }),
    updateUser: (id, input) => call<UserSummary>({ method: 'PATCH', path: `/users/${enc(id)}`, body: input }),
    removeUser: async (id) => {
      await call<unknown>({ method: 'DELETE', path: `/users/${enc(id)}` });
    },
    resetUser: (id) => call<Invitation>({ method: 'POST', path: `/users/${enc(id)}/reset` }),
    listAudit: (cursor) => call<{ items: AuditEntry[]; nextCursor: string | null }>({ method: 'GET', path: `/audit?limit=50${cursor ? `&cursor=${enc(cursor)}` : ''}` }),
    getMachine: () => call<Machine>({ method: 'GET', path: '/machine' }),
    updateMachine: (settings: MachineSettings) => call<Machine>({ method: 'PATCH', path: '/machine', body: settings }),
    getRootCA: () => call<string>({ method: 'GET', path: '/machine/ca', text: true }),
    listResources: async (kind, project) => {
      const items: Resource[] = [];
      let cursor: string | null = null;
      do {
        const q = new URLSearchParams({ limit: '200' });
        if (kind) q.set('kind', kind);
        if (project) q.set('project', project);
        if (cursor) q.set('cursor', cursor);
        const page: ResourceList = await call<ResourceList>({ method: 'GET', path: `/resources?${q.toString()}` });
        items.push(...page.items);
        cursor = page.nextCursor;
      } while (cursor);
      return items;
    },
    getResource: (id) => call<Resource>({ method: 'GET', path: `/resources/${enc(id)}` }),
    createResource: (kind, name, spec, project) =>
      call<Resource>({ method: 'POST', path: '/resources', body: { kind, name, ...(project ? { project } : {}), spec }, idempotent: true }),
    updateResource: (id, patch, ifMatch) => call<Resource>({ method: 'PATCH', path: `/resources/${enc(id)}`, body: { spec: patch }, ifMatch }),
    deleteResource: (id, ifMatch) => call<Resource>({ method: 'DELETE', path: `/resources/${enc(id)}`, ifMatch }),
    action: (id, action) => call<Resource>({ method: 'POST', path: `/resources/${enc(id)}/actions/${action}` }),
    getEvents: async (id) => (await call<{ items: Event[] }>({ method: 'GET', path: `/resources/${enc(id)}/events?limit=100` })).items,
    getLogs: async (id, tail = 200, build = false) =>
      (await call<{ items: LogLine[] }>({ method: 'GET', path: `/resources/${enc(id)}/logs?tail=${tail}${build ? '&build=true' : ''}` })).items,
    revealSecret: async (id) => (await call<{ value: string }>({ method: 'GET', path: `/resources/${enc(id)}/value` })).value,
    runNow: (id) => call<Run>({ method: 'POST', path: `/resources/${enc(id)}/actions/run` }),
    restore: (id, req) => call<Resource>({ method: 'POST', path: `/resources/${enc(id)}/actions/restore`, body: req }),
    listRuns: async (id) => (await call<{ items: Run[] }>({ method: 'GET', path: `/resources/${enc(id)}/runs?limit=50` })).items,
    getRun: (id, runId) => call<RunDetail>({ method: 'GET', path: `/resources/${enc(id)}/runs/${enc(runId)}` }),
    sendMessage: (id, message) => call<SentMessage>({ method: 'POST', path: `/resources/${enc(id)}/messages`, body: message }),
    listMessages: (id, limit = 20) => call<MessageList>({ method: 'GET', path: `/resources/${enc(id)}/messages?limit=${limit}` }),
    receiveMessages: async (id, max = 1, waitSeconds = 0) =>
      (await call<{ items: ReceivedMessage[] }>({ method: 'POST', path: `/resources/${enc(id)}/messages/receive`, body: { max, waitSeconds } })).items,
    listObjects: (id, prefix, cursor) => call<ObjectList>({ method: 'GET',
      path: `/resources/${enc(id)}/objects?prefix=${enc(prefix)}${cursor ? `&cursor=${enc(cursor)}` : ''}` }),
    uploadObject: (id, key, file, tags = {}) => call<ObjectInfo>({ method: 'PUT', path: objectPath(id, key), raw: file,
      headers: Object.fromEntries(Object.entries(tags).map(([k, v]) => [`${HEADERS.objectTagPrefix}${k}`, v])) }),
    objectUrl: (id, key) => `${base}${objectPath(id, key)}`,
    deleteObject: async (id, key) => {
      await call<unknown>({ method: 'DELETE', path: objectPath(id, key) });
    },
    deleteMessages: (id, receipts) => call<DeletedMessages>({ method: 'POST', path: `/resources/${enc(id)}/messages/delete`, body: { receipts } }),
    getDeliveries: (id) => call<DeliveryStats>({ method: 'GET', path: `/resources/${enc(id)}/deliveries` }),
    sendEmail: (id, email) => call<{ id: string }>({ method: 'POST', path: `/resources/${enc(id)}/emails`, body: email }),
    listEmails: (id, limit = 50) => call<EmailList>({ method: 'GET', path: `/resources/${enc(id)}/emails?limit=${limit}` }),
    getEmail: (id, emailId) => call<Email>({ method: 'GET', path: `/resources/${enc(id)}/emails/${enc(emailId)}` }),
    transcode: (id, key) => call<Run>({ method: 'POST', path: `/resources/${enc(id)}/actions/run`, body: { key } }),
    getMetrics: (id, metrics, from, to) => call<MetricData>({ method: 'GET', path: `/resources/${enc(id)}/metrics?${metricQuery(metrics, from, to)}` }),
    getMachineMetrics: (metrics, from, to) => call<MetricData>({ method: 'GET', path: `/machine/metrics?${metricQuery(metrics, from, to)}` }),
    test: (id) => call<TestResult>({ method: 'POST', path: `/resources/${enc(id)}/actions/test` }),
    listIncidents: async (id) => (await call<{ items: Incident[] }>({ method: 'GET', path: `/resources/${enc(id)}/incidents?limit=50` })).items,
    listNotifications: async (id) => (await call<{ items: NotificationRecord[] }>({ method: 'GET', path: `/resources/${enc(id)}/notifications?limit=50` })).items,
    listMachines: async () => (await call<{ items: MachineSummary[] }>({ method: 'GET', path: '/machines' })).items,
    getMachineByName: (name) => call<MachineSummary>({ method: 'GET', path: `/machines/${enc(name)}` }),
    addMachine: (name, labels) => call<AddedMachine>({ method: 'POST', path: '/machines', body: { name, ...(labels ? { labels } : {}) } }),
    updateMachineByName: (name, input) => call<MachineSummary>({ method: 'PATCH', path: `/machines/${enc(name)}`, body: input }),
    removeMachine: async (name, force = false) => {
      await call<unknown>({ method: 'DELETE', path: `/machines/${enc(name)}${force ? '?force=true' : ''}` });
    },
    newJoinCode: (name) => call<AddedMachine>({ method: 'POST', path: `/machines/${enc(name)}/join-code` }),
    drainMachine: (name) => call<DrainResult>({ method: 'POST', path: `/machines/${enc(name)}/drain` }),
    getMachineEvents: async (name) => (await call<{ items: Event[] }>({ method: 'GET', path: `/machines/${enc(name)}/events` })).items,
    getMachineMetricsOf: (name, metrics, from, to) => call<MetricData>({ method: 'GET', path: `/machines/${enc(name)}/metrics?${metricQuery(metrics, from, to)}` }),
    listCatalogue: async () => (await call<{ items: CatalogueEntry[] }>({ method: 'GET', path: '/catalogue' })).items,
    getCatalogueEntry: (name, version) => call<CatalogueEntry>({ method: 'GET', path: `/catalogue/${enc(name)}${version ? `?version=${enc(version)}` : ''}` }),
    addTemplate: (body) => call<CatalogueEntry>({ method: 'POST', path: '/catalogue', body: { body } }),
    removeTemplate: async (name) => {
      await call<unknown>({ method: 'DELETE', path: `/catalogue/${enc(name)}` });
    },
  };
}

/** ?metric=…&from=…&to=… with RFC 3339 times (L3-002 §10.4). */
export function metricQuery(metrics: string[], from: number, to: number): string {
  const q = new URLSearchParams();
  for (const m of metrics) q.append('metric', m);
  q.set('from', new Date(from * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z'));
  q.set('to', new Date(to * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z'));
  return q.toString();
}

function backoff(attempt: number, baseMs: number): number {
  return baseMs * 2 ** (attempt - 1) * (0.8 + Math.random() * 0.4);
}

export function toApiError(res: Response, payload: unknown): ApiError {
  const err = (payload as { error?: { code?: string; message?: string; requestId?: string; details?: FieldError[] } } | null)?.error;
  const retryAfter = Number(res.headers.get('Retry-After') ?? '0') || 0;
  const requestId = err?.requestId ?? res.headers.get(HEADERS.requestId) ?? '';
  if (err?.code) {
    return new ApiError(err.code as ErrorCode, err.message ?? `Request failed (${res.status})`, res.status, { requestId, details: err.details, retryAfterSeconds: retryAfter });
  }
  const fallback: ErrorCode = res.status === 404 ? 'NOT_FOUND' : res.status === 401 ? 'UNAUTHENTICATED' : 'INTERNAL';
  return new ApiError(fallback, `Unexpected response (${res.status}).`, res.status, { requestId, retryAfterSeconds: retryAfter });
}
