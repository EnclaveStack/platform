import { KINDS, RULES } from './generated';
import type { CatalogueEntry, FieldError, Kind, MachineSummary, Placed, TemplateMember } from './types';

// The mock's machines and catalogue (v0.8, L3-002 §11): the built-in entries as the server lists them,
// placement by the server's rule, and a mock rendering of templates into members.

const GiB = 2 ** 30;

const domainParam = (required: boolean, description: string) => ({ name: 'domain', title: 'Domain', description, type: 'hostname' as const, ...(required ? { required: true } : {}) });
const member = (kind: Kind, name: string, optional = false): TemplateMember => ({ kind, name: name ? `<stack>-${name}` : '<stack>', optional });

/** The six built-in entries (internal/catalogue/builtin), as GET /catalogue lists them: by name. */
export const BUILTIN_CATALOGUE: CatalogueEntry[] = [
  { name: 'wordpress', title: 'WordPress', description: 'A WordPress site with its MySQL database and a volume for uploads, themes and plugins.',
    parameters: [domainParam(false, 'Serve the site at this host name, with HTTPS.'), { name: 'memoryMiB', title: 'Memory (MiB)', type: 'number' as const, default: 512 }],
    members: [member('database', 'db'), member('volume', 'content'), member('app', ''), member('domain', 'site', true)], outputs: ['url', 'admin'] },
  { name: 'ghost', title: 'Ghost', description: 'A Ghost publication with its MySQL database and a volume for images and themes.',
    parameters: [domainParam(true, 'Ghost needs the address readers open it at.')],
    members: [member('database', 'db'), member('volume', 'content'), member('app', ''), member('domain', 'site')], outputs: ['url', 'admin'] },
  { name: 'n8n', title: 'n8n', description: 'n8n workflow automation with its PostgreSQL database and a volume for its settings.',
    parameters: [domainParam(false, 'Serve n8n and its webhooks at this host name, with HTTPS.')],
    members: [member('database', 'db'), member('secret', 'key'), member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'uptime-kuma', title: 'Uptime Kuma', description: 'A status page and uptime monitor, keeping its data in a volume.',
    parameters: [domainParam(false, 'Serve the status page at this host name, with HTTPS.')],
    members: [member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'gitea', title: 'Gitea', description: 'Git hosting with its PostgreSQL database and a volume for repositories.',
    parameters: [domainParam(false, 'Serve Gitea at this host name, with HTTPS.')],
    members: [member('database', 'db'), member('volume', 'data'), member('app', ''), member('domain', 'site', true)], outputs: ['url'] },
  { name: 'plausible', title: 'Plausible Analytics', description: 'Privacy-friendly web analytics, with PostgreSQL for settings and ClickHouse for events.',
    parameters: [domainParam(true, 'Plausible needs the address it is opened at.')],
    members: [member('database', 'db'), member('database', 'events'), member('secret', 'key'), member('secret', 'database-url'), member('secret', 'events-url'), member('app', ''), member('domain', 'site')],
    outputs: ['url'] },
].map((e) => ({ ...e, version: '1.0.0', versions: ['1.0.0'], builtIn: true })).sort((a, b) => a.name.localeCompare(b.name));

/** A member as the mock creates it. */
export interface MockMember {
  kind: Kind;
  name: string;
  spec: Record<string, unknown>;
  /** A secret member's value. */
  value?: string;
}

/** The members of a stack of entry, with the template's outputs; or the refusal's details. */
export function renderStack(entry: CatalogueEntry, stack: string, params: Record<string, unknown>, secrets: Record<string, string>):
  { members: MockMember[]; outputs: Record<string, string> } | FieldError[] {
  const details: FieldError[] = [];
  for (const k of Object.keys(params)) {
    if (!entry.parameters.some((p) => p.name === k && p.type !== 'secret')) details.push({ field: `spec.parameters.${k}`, code: 'NOT_ALLOWED', message: `${entry.title} has no parameter ${k}.` });
  }
  for (const p of entry.parameters) {
    const v = p.type === 'secret' ? secrets[p.name] : params[p.name];
    const field = `spec.${p.type === 'secret' ? 'secretParameters' : 'parameters'}.${p.name}`;
    if (p.required && (v === undefined || v === '')) details.push({ field, code: 'REQUIRED', message: `${p.title} is required.` });
    else if (v !== undefined && v !== '' && p.type === 'number' && !Number.isFinite(Number(v))) details.push({ field, code: 'INVALID_FORMAT', message: `${p.title} is a number.` });
    else if (v !== undefined && v !== '' && p.type === 'hostname' && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(String(v).toLowerCase())) {
      details.push({ field, code: 'INVALID_FORMAT', message: `${p.title} is a host name, like blog.example.com.` });
    }
  }
  if (stack.length + 1 + Math.max(0, ...entry.members.map((m) => m.name.replace('<stack>', '').length)) > RULES.nameMax) {
    details.push({ field: 'name', code: 'OUT_OF_RANGE', message: `Use at most ${RULES.nameMax - Math.max(...entry.members.map((m) => m.name.replace('<stack>', '').length))} characters: the stack's members add to the name.` });
  }
  if (details.length) return details;
  const value = (name: string) => {
    const p = entry.parameters.find((x) => x.name === name);
    return params[name] ?? p?.default;
  };
  const domain = String(value('domain') ?? '').toLowerCase();
  const members: MockMember[] = [];
  for (const m of entry.members) {
    if (m.optional && !domain) continue;
    const name = m.name.replace('<stack>', stack);
    const local = m.name.replace('<stack>-', '').replace('<stack>', '');
    switch (m.kind) {
      case 'database':
        members.push({ kind: m.kind, name, spec: local === 'events' ? { engine: 'clickhouse', version: '26.3' } : entry.name === 'wordpress' || entry.name === 'ghost' ? { engine: 'mysql', version: '8.4' } : { engine: 'postgres', version: '16' } });
        break;
      case 'volume':
        members.push({ kind: m.kind, name, spec: { type: 'filesystem' } });
        break;
      case 'secret':
        members.push({ kind: m.kind, name, spec: {}, value: Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2) });
        break;
      case 'domain':
        members.push({ kind: m.kind, name, spec: { hostname: domain, app: stack, tls: 'auto', redirectHttp: true } });
        break;
      case 'app': {
        const volume = entry.members.find((x) => x.kind === 'volume')?.name.replace('<stack>', stack);
        members.push({ kind: m.kind, name, spec: { image: `${entry.name}:latest`, port: 80, memoryMiB: Number(value('memoryMiB') ?? RULES.appMemoryDefault), strategy: 'recreate', replicas: 1,
          ...(volume ? { volumes: [{ volume, path: '/data' }] } : {}) } });
        break;
      }
      default:
        members.push({ kind: m.kind, name, spec: {} });
    }
  }
  for (const [k, v] of Object.entries(secrets)) members.unshift({ kind: 'secret', name: `${stack}-${k.toLowerCase()}`, spec: {}, value: v });
  const outputs: Record<string, string> = {};
  for (const o of entry.outputs) outputs[o] = o === 'admin' ? `https://${domain}${entry.name === 'ghost' ? '/ghost/' : '/wp-admin/'}` : o === 'app' ? stack : `https://${domain}`;
  return { members, outputs };
}

/** Reads a template of one's own the way the mock can: its name, title, version, description and members. */
export function parseTemplate(body: string): CatalogueEntry | FieldError[] {
  const top = (key: string) => body.match(new RegExp(`^${key}:\\s*(.+)$`, 'm'))?.[1]?.trim().replace(/^["']|["']$/g, '');
  const lineOf = (key: string) => body.split('\n').findIndex((l) => l.startsWith(`${key}:`)) + 1;
  const details: FieldError[] = [];
  const name = top('name');
  const version = top('version');
  if (!body.trim()) details.push({ field: 'body', code: 'REQUIRED', message: 'Give the template, in YAML or JSON.' });
  else {
    if (!name) details.push({ field: 'body.line 1: name', code: 'REQUIRED', message: 'A template has a name.' });
    if (!version || !/^\d+\.\d+\.\d+$/.test(version)) details.push({ field: `body.line ${lineOf('version') || 1}: version`, code: 'INVALID_FORMAT', message: 'The version is MAJOR.MINOR.PATCH, like 1.0.0.' });
  }
  const kinds = [...body.matchAll(/kind:\s*([a-z]+)\s*,?\s*(?:\n\s*)?name:\s*"?([a-z0-9-]*)"?/g)];
  body.split('\n').forEach((l, i) => {
    const k = l.match(/kind:\s*([a-z]+)/)?.[1];
    if (k && !(KINDS as readonly string[]).includes(k)) details.push({ field: `body.line ${i + 1}: resources.kind`, code: 'NOT_ALLOWED', message: `${k} isn't a kind.` });
  });
  if (!details.length && kinds.length === 0) details.push({ field: 'body.line 1: resources', code: 'REQUIRED', message: 'A template has at least one resource.' });
  if (details.length) return details;
  return { name: name ?? '', title: top('title') ?? name ?? '', description: top('description') ?? '', version: version ?? '', versions: [version ?? ''], builtIn: false,
    parameters: [], members: kinds.map(([, kind, local]) => member(kind as Kind, local ?? '')), outputs: [] };
}

/** The main machine, as GET /machines lists it. */
export function mainMachine(title: string, reservedBytes: number, resources: number): MachineSummary {
  return { id: RULES.mainMachine, name: RULES.mainMachine, title, main: true, state: 'online', since: new Date(Date.now() - 3 * 86_400_000).toISOString(), labels: {},
    schedulable: true, address: null, os: 'darwin', arch: 'arm64', agentVersion: '0.8.0-mock', engine: { driver: 'mock', available: true, version: 'mock', message: '' },
    cpus: 8, memoryTotalBytes: 16 * GiB, memoryReservedBytes: reservedBytes, vms: { available: true, message: 'VMs can run: QEMU 11.1 with HVF.' }, lastSeenAt: new Date().toISOString(),
    resources, versionMismatch: false, links: [], message: '' };
}

/** A machine waiting for its agent. */
export function waitingMachine(id: string, name: string, labels: Record<string, string>): MachineSummary {
  return { id, name, title: name, main: false, state: 'waiting', since: new Date().toISOString(), labels, schedulable: true, address: null, os: '', arch: '', agentVersion: '',
    engine: { driver: '', available: false, version: '', message: '' }, cpus: 0, memoryTotalBytes: 0, memoryReservedBytes: 0, vms: { available: false, message: 'Not joined yet.' },
    lastSeenAt: null, resources: 0, versionMismatch: false, links: [], message: 'Waiting for its agent to join.' };
}

/** A waiting machine once its agent joins: a Linux box with 8 GiB. */
export function joined(m: MachineSummary, address: string): MachineSummary {
  return { ...m, state: 'online', since: new Date().toISOString(), address, os: 'linux', arch: 'amd64', agentVersion: '0.8.0-mock',
    engine: { driver: 'docker', available: true, version: '29.0.1', message: '' }, cpus: 4, memoryTotalBytes: 8 * GiB, vms: { available: true, message: 'VMs can run: QEMU 11.1 with KVM.' },
    lastSeenAt: new Date().toISOString(), message: '' };
}

/** The server's placement rule (ADR-044): the named machine, else the one with the most free memory among online, schedulable machines with the labels, main on a tie. */
export function choose(p: Placed, machines: MachineSummary[], needBytes: number): { machine: string } | { reason: string } {
  if (p.machine) {
    const m = machines.find((x) => x.name === p.machine);
    if (!m) return { reason: `There's no machine named ${p.machine}.` };
    return m.state === 'online' ? { machine: m.name } : { reason: `${m.name} is ${m.state}.` };
  }
  const labels = Object.entries(p.machineLabels ?? {});
  const fit = machines.filter((m) => m.state === 'online' && m.schedulable && labels.every(([k, v]) => m.labels[k] === v));
  if (!fit.length) return { reason: labels.length ? `No machine has the labels ${labels.map(([k, v]) => `${k}=${v}`).join(', ')}.` : 'No machine can take it.' };
  const room = fit.filter((m) => m.memoryTotalBytes - m.memoryReservedBytes >= needBytes);
  if (!room.length) return { reason: `No machine has ${Math.round(needBytes / 2 ** 20)} MiB free.` };
  room.sort((a, b) => (b.memoryTotalBytes - b.memoryReservedBytes) - (a.memoryTotalBytes - a.memoryReservedBytes) || Number(b.main) - Number(a.main));
  return { machine: room[0]?.name ?? RULES.mainMachine };
}
